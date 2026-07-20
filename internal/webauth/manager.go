package webauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Cookie names. The session cookie carries the plaintext session token; the
// state cookie carries the sha256 of the pending OAuth state token so the
// callback can verify the round-trip (CSRF) without a server-side pending set.
const (
	sessionCookie = "kitsoki_session"
	stateCookie   = "kitsoki_oauth_state"
	deviceCookie  = "kitsoki_device_state"
)

// stateTTL bounds how long a login attempt may sit between /auth/github/start
// and the callback.
const stateTTL = 10 * time.Minute

const maxPendingDeviceLogins = 128

// DefaultSessionTTL is the cookie-session lifetime when auth.session_ttl is
// not configured: 30 days.
const DefaultSessionTTL = 30 * 24 * time.Hour

// actorHeader mirrors internal/runstatus/server's X-Kitsoki-Actor constant:
// the gate overwrites it with the session user's GitHub login so the server's
// existing actor/author seam attributes turns to the authenticated principal
// (and a client-supplied value can never spoof identity).
const actorHeader = "X-Kitsoki-Actor"

// Config is the resolved runtime auth configuration the Manager serves with.
// The off/required/auto decision has already been collapsed by the caller
// (cmd/kitsoki/web.go) — a Manager only exists when auth is required.
type Config struct {
	// PublicURL is the externally visible base URL (https://kitsoki.example.com).
	// It anchors the OAuth redirect_uri and forces Secure cookies when https.
	// Empty ⇒ derive scheme/host from each request (plain-http dev setups).
	PublicURL string
	// Admins lists GitHub logins that are auto-provisioned as admins on
	// sign-in, without an invite.
	Admins []string
	// SessionTTL is the browser-session lifetime; zero ⇒ DefaultSessionTTL.
	SessionTTL time.Duration
	// DeviceFlow selects GitHub's Device Flow instead of the callback-based
	// web flow. It requires only a client ID and is intended for deployments
	// whose GitHub App already has Device Flow enabled.
	DeviceFlow bool
	// ServiceTokens maps a service name to its resolved bearer-token value.
	// A request carrying `Authorization: Bearer <token>` that matches one of
	// these (constant-time, by sha256) passes the gate with actor
	// "service:<name>" and no session — the headless-automation path for
	// same-host services (colony runners, cron drivers) that cannot complete
	// a browser login. Tokens are resolved from operator-named env vars at
	// startup (see webconfig.AuthConfig.ServiceTokens) and never persisted.
	ServiceTokens map[string]string
}

// Manager owns the /auth/* HTTP surface and the request gate. Build one with
// NewManager, register its routes with Mount, and wrap the rest of the mux
// with Wrap.
type Manager struct {
	store *Store
	gh    *GitHubClient
	cfg   Config

	// serviceTokenHashes holds sha256(token) per service name so request
	// tokens compare in constant time against fixed-length digests.
	serviceTokenHashes map[string][32]byte

	deviceMu sync.Mutex
	devices  map[string]*pendingDeviceLogin
}

type pendingDeviceLogin struct {
	deviceCode    string
	pollTokenHash string
	payload       statePayload
	expiresAt     time.Time
	nextPollAt    time.Time
	interval      time.Duration
	inFlight      bool
}

func NewManager(store *Store, gh *GitHubClient, cfg Config) *Manager {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	hashes := make(map[string][32]byte, len(cfg.ServiceTokens))
	for name, token := range cfg.ServiceTokens {
		if name == "" || token == "" {
			continue
		}
		hashes[name] = sha256.Sum256([]byte(token))
	}
	return &Manager{store: store, gh: gh, cfg: cfg, serviceTokenHashes: hashes, devices: make(map[string]*pendingDeviceLogin)}
}

// Mount registers the /auth/* routes. They are also skipped by Wrap, so login
// is reachable while everything else is gated.
func (m *Manager) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/auth/login", m.handleLogin)
	mux.HandleFunc("/auth/invite", m.handleInvite)
	mux.HandleFunc("/auth/github/start", m.handleStart)
	mux.HandleFunc("/auth/github/callback", m.handleCallback)
	mux.HandleFunc("/auth/github/device/start", m.handleDeviceStart)
	mux.HandleFunc("/auth/github/device/poll", m.handleDevicePoll)
	mux.HandleFunc("/auth/logout", m.handleLogout)
	mux.HandleFunc("/auth/me", m.handleMe)
	mux.HandleFunc("/auth/check", m.handleCheck)
}

// Wrap is the gate: every non-/auth/ request needs a live session cookie.
// Authenticated requests proceed with X-Kitsoki-Actor rewritten to the
// session user's GitHub login. Unauthenticated page loads (GET/HEAD accepting
// text/html) are redirected to the login page with the original URL as ?next=;
// everything else — the SPA's JSON fetches and its EventSource streams — gets
// a 401 JSON body so clients fail fast instead of hanging.
func (m *Manager) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		user, ok := m.sessionUser(r)
		if !ok {
			if service, ok := m.serviceActor(r); ok {
				r.Header.Set(actorHeader, service)
				next.ServeHTTP(w, r)
				return
			}
			// Never let a spoofed identity header through an unauthenticated
			// rejection path either.
			r.Header.Del(actorHeader)
			if wantsHTML(r) {
				http.Redirect(w, r, loginURL(r.URL), http.StatusFound)
				return
			}
			writeUnauthenticated(w)
			return
		}
		r.Header.Set(actorHeader, user.GitHubLogin)
		next.ServeHTTP(w, r)
	})
}

// serviceActor resolves an `Authorization: Bearer <token>` header to a
// configured service identity ("service:<name>"). Comparison is by sha256
// digest in constant time, so neither token length nor content leaks through
// timing. No session is created — every request re-presents the token.
func (m *Manager) serviceActor(r *http.Request) (string, bool) {
	if len(m.serviceTokenHashes) == 0 {
		return "", false
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	if !ok || token == "" {
		return "", false
	}
	got := sha256.Sum256([]byte(token))
	for name, want := range m.serviceTokenHashes {
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			return "service:" + name, true
		}
	}
	return "", false
}

// sessionUser resolves the request's session cookie to a live user.
func (m *Manager) sessionUser(r *http.Request) (User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return User{}, false
	}
	u, ok, err := m.store.SessionUser(r.Context(), c.Value)
	if err != nil || !ok {
		return User{}, false
	}
	return u, true
}

// ── /auth/login ───────────────────────────────────────────────────────────

func (m *Manager) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	startQ := url.Values{}
	if next := sanitizeNext(q.Get("next")); next != "" {
		startQ.Set("next", next)
	}
	if invite := q.Get("invite"); invite != "" {
		startQ.Set("invite", invite)
	}
	startPath := "/auth/github/start?" + startQ.Encode()
	if m.cfg.DeviceFlow {
		startPath = "/auth/github/device/start?" + startQ.Encode()
	}
	renderLoginPage(w, loginPageData{
		StartURL:   startPath,
		Invited:    q.Get("invite") != "",
		Error:      q.Get("err"),
		DeviceFlow: m.cfg.DeviceFlow,
	})
}

// ── /auth/invite ──────────────────────────────────────────────────────────

// handleInvite is the landing for the one-time link the operator shares. A
// live code forwards to the login page carrying the code; anything else gets
// one non-committal message (no live/redeemed/unknown distinction).
func (m *Manager) handleInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		renderMessagePage(w, http.StatusNotFound, "This invitation link is not valid.")
		return
	}
	if _, err := m.store.LookupInvite(r.Context(), code); err != nil {
		renderMessagePage(w, http.StatusNotFound, "This invitation link is not valid.")
		return
	}
	http.Redirect(w, r, "/auth/login?invite="+url.QueryEscape(code), http.StatusFound)
}

// ── /auth/github/start ────────────────────────────────────────────────────

// statePayload rides inside the OAuth state parameter (after the random
// token), carrying the login attempt's context across the GitHub round-trip.
type statePayload struct {
	Next   string `json:"next,omitempty"`
	Invite string `json:"invite,omitempty"`
}

func (m *Manager) handleStart(w http.ResponseWriter, r *http.Request) {
	if m.cfg.DeviceFlow {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, hash, err := NewToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	payload := statePayload{
		Next:   sanitizeNext(r.URL.Query().Get("next")),
		Invite: r.URL.Query().Get("invite"),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := token + "." + base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    hash,
		Path:     "/auth/",
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   m.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, m.gh.AuthCodeURL(state, m.redirectURI(r)), http.StatusFound)
}

// ── GitHub Device Flow ────────────────────────────────────────────────────

// handleDeviceStart creates one short-lived GitHub device authorization and
// renders the one-time code. The GitHub device_code never reaches the browser;
// it is kept in a bounded in-memory set and addressed by an opaque HttpOnly
// cookie plus a second page-bound polling token.
func (m *Manager) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	if !m.cfg.DeviceFlow {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now()
	m.deviceMu.Lock()
	m.pruneDevicesLocked(now)
	full := len(m.devices) >= maxPendingDeviceLogins
	m.deviceMu.Unlock()
	if full {
		renderMessagePage(w, http.StatusServiceUnavailable, "Sign-in is temporarily busy. Try again shortly.")
		return
	}

	auth, err := m.gh.StartDeviceAuthorization(r.Context())
	if err != nil {
		renderMessagePage(w, http.StatusBadGateway, "GitHub sign-in is temporarily unavailable. Try again.")
		return
	}
	attemptToken, attemptHash, err := NewToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pollToken, pollHash, err := NewToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pending := &pendingDeviceLogin{
		deviceCode:    auth.DeviceCode,
		pollTokenHash: pollHash,
		payload: statePayload{
			Next:   sanitizeNext(r.URL.Query().Get("next")),
			Invite: r.URL.Query().Get("invite"),
		},
		expiresAt:  now.Add(auth.ExpiresIn),
		nextPollAt: now,
		interval:   auth.Interval,
	}
	m.deviceMu.Lock()
	m.pruneDevicesLocked(now)
	if len(m.devices) >= maxPendingDeviceLogins {
		m.deviceMu.Unlock()
		renderMessagePage(w, http.StatusServiceUnavailable, "Sign-in is temporarily busy. Try again shortly.")
		return
	}
	m.devices[attemptHash] = pending
	m.deviceMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookie,
		Value:    attemptToken,
		Path:     "/auth/",
		MaxAge:   int(auth.ExpiresIn.Seconds()),
		HttpOnly: true,
		Secure:   m.secureCookies(r),
		SameSite: http.SameSiteStrictMode,
	})
	renderDevicePage(w, devicePageData{
		UserCode:        auth.UserCode,
		VerificationURI: auth.VerificationURI,
		PollToken:       pollToken,
		PollIntervalMS:  int64(auth.Interval / time.Millisecond),
	})
}

func (m *Manager) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	if !m.cfg.DeviceFlow {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(deviceCookie)
	if err != nil || cookie.Value == "" {
		writeDeviceJSON(w, http.StatusGone, "expired", "This sign-in attempt expired. Start again.", 0, "")
		return
	}
	key := HashToken(cookie.Value)
	now := time.Now()
	m.deviceMu.Lock()
	m.pruneDevicesLocked(now)
	pending, ok := m.devices[key]
	if !ok {
		m.deviceMu.Unlock()
		m.clearDeviceCookie(w, r)
		writeDeviceJSON(w, http.StatusGone, "expired", "This sign-in attempt expired. Start again.", 0, "")
		return
	}
	if HashToken(r.Header.Get("X-Kitsoki-Device-Token")) != pending.pollTokenHash {
		m.deviceMu.Unlock()
		writeDeviceJSON(w, http.StatusForbidden, "forbidden", "Invalid sign-in poll.", 0, "")
		return
	}
	if pending.inFlight || now.Before(pending.nextPollAt) {
		retry := pending.nextPollAt.Sub(now)
		if retry < time.Second {
			retry = time.Second
		}
		m.deviceMu.Unlock()
		writeDeviceJSON(w, http.StatusAccepted, "pending", "", retry, "")
		return
	}
	pending.inFlight = true
	deviceCode := pending.deviceCode
	m.deviceMu.Unlock()

	result, pollErr := m.gh.PollDeviceAuthorization(r.Context(), deviceCode)
	now = time.Now()
	m.deviceMu.Lock()
	current, stillPending := m.devices[key]
	if !stillPending || current != pending {
		m.deviceMu.Unlock()
		m.clearDeviceCookie(w, r)
		writeDeviceJSON(w, http.StatusGone, "expired", "This sign-in attempt expired. Start again.", 0, "")
		return
	}
	pending.inFlight = false
	if pollErr != nil {
		delete(m.devices, key)
		m.deviceMu.Unlock()
		m.clearDeviceCookie(w, r)
		writeDeviceJSON(w, http.StatusBadGateway, "error", "GitHub sign-in failed. Start again.", 0, "")
		return
	}
	switch result.State {
	case DevicePollPending:
		pending.nextPollAt = now.Add(pending.interval)
		retry := pending.interval
		m.deviceMu.Unlock()
		writeDeviceJSON(w, http.StatusAccepted, "pending", "", retry, "")
		return
	case DevicePollSlowDown:
		if result.Interval > pending.interval {
			pending.interval = result.Interval
		} else {
			pending.interval += 5 * time.Second
		}
		pending.nextPollAt = now.Add(pending.interval)
		retry := pending.interval
		m.deviceMu.Unlock()
		writeDeviceJSON(w, http.StatusAccepted, "pending", "", retry, "")
		return
	case DevicePollComplete:
		delete(m.devices, key)
		payload := pending.payload
		m.deviceMu.Unlock()
		m.clearDeviceCookie(w, r)
		gh, err := m.gh.FetchUser(r.Context(), result.AccessToken)
		if err != nil {
			writeDeviceJSON(w, http.StatusBadGateway, "error", "Could not read your GitHub profile. Start again.", 0, "")
			return
		}
		user, err := m.resolveUser(r.Context(), gh, payload.Invite)
		if errors.Is(err, errNotInvited) {
			writeDeviceJSON(w, http.StatusForbidden, "forbidden", "This GitHub account has no invitation. Ask the operator for an invite link.", 0, "")
			return
		}
		if err != nil {
			writeDeviceJSON(w, http.StatusInternalServerError, "error", "Sign-in could not be completed. Start again.", 0, "")
			return
		}
		if err := m.issueSession(w, r, user); err != nil {
			writeDeviceJSON(w, http.StatusInternalServerError, "error", "Sign-in could not be completed. Start again.", 0, "")
			return
		}
		next := payload.Next
		if next == "" {
			next = "/"
		}
		writeDeviceJSON(w, http.StatusOK, "complete", "", 0, next)
		return
	default:
		delete(m.devices, key)
		m.deviceMu.Unlock()
		m.clearDeviceCookie(w, r)
		writeDeviceJSON(w, http.StatusBadGateway, "error", "GitHub sign-in failed. Start again.", 0, "")
	}
}

func (m *Manager) pruneDevicesLocked(now time.Time) {
	for key, pending := range m.devices {
		if !now.Before(pending.expiresAt) {
			delete(m.devices, key)
		}
	}
}

func (m *Manager) clearDeviceCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookie,
		Value:    "",
		Path:     "/auth/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secureCookies(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func writeDeviceJSON(w http.ResponseWriter, status int, state, message string, retry time.Duration, next string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	out := struct {
		Status       string `json:"status"`
		Message      string `json:"message,omitempty"`
		RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
		Next         string `json:"next,omitempty"`
	}{Status: state, Message: message, RetryAfterMS: int64(retry / time.Millisecond), Next: next}
	_ = json.NewEncoder(w).Encode(out)
}

// ── /auth/github/callback ─────────────────────────────────────────────────

func (m *Manager) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, ok := m.verifyState(w, r)
	if !ok {
		m.redirectLoginErr(w, r, payload, "Sign-in expired or was tampered with. Try again.")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		m.redirectLoginErr(w, r, payload, "GitHub did not complete the sign-in. Try again.")
		return
	}
	token, err := m.gh.Exchange(r.Context(), code, m.redirectURI(r))
	if err != nil {
		m.redirectLoginErr(w, r, payload, "GitHub sign-in failed. Try again.")
		return
	}
	gh, err := m.gh.FetchUser(r.Context(), token)
	if err != nil {
		m.redirectLoginErr(w, r, payload, "Could not read your GitHub profile. Try again.")
		return
	}

	user, err := m.resolveUser(r.Context(), gh, payload.Invite)
	if err != nil {
		if errors.Is(err, errNotInvited) {
			renderMessagePage(w, http.StatusForbidden, "This GitHub account has no invitation. Ask the operator for an invite link.")
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := m.issueSession(w, r, user); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	next := payload.Next
	if next == "" {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func (m *Manager) issueSession(w http.ResponseWriter, r *http.Request, user User) error {
	sessionToken, err := m.store.CreateSession(r.Context(), user.ID, m.cfg.SessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(m.cfg.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   m.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// errNotInvited marks a GitHub account with no user row, no valid invite, and
// no auth.admins entry.
var errNotInvited = errors.New("webauth: github account not invited")

// resolveUser maps an authenticated GitHub identity to a kitsoki user:
// returning users sign straight in, a valid invite code is redeemed, and
// configured admins are auto-provisioned. Everyone else is errNotInvited.
// An invite presented by an already-known user is redeemed anyway (consuming
// it, possibly promoting to admin) so a re-used admin invite link behaves
// predictably.
func (m *Manager) resolveUser(ctx context.Context, gh GitHubUser, inviteCode string) (User, error) {
	if inviteCode != "" {
		u, err := m.store.RedeemInvite(ctx, inviteCode, gh)
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, ErrInviteNotFound) {
			return User{}, err
		}
		// Stale/consumed invite: fall through to the returning-user paths.
	}
	if u, ok, err := m.store.UserByGitHubID(ctx, gh.ID); err != nil {
		return User{}, err
	} else if ok {
		if err := m.store.TouchLogin(ctx, u.ID, gh); err != nil {
			return User{}, err
		}
		u.GitHubLogin = gh.Login
		return u, nil
	}
	for _, admin := range m.cfg.Admins {
		if strings.EqualFold(admin, gh.Login) {
			return m.store.EnsureAdmin(ctx, gh)
		}
	}
	return User{}, errNotInvited
}

// verifyState checks the callback's state parameter against the hash cookie
// set by handleStart and clears the cookie. The decoded payload is returned
// even on failure so the error redirect can preserve next/invite context.
func (m *Manager) verifyState(w http.ResponseWriter, r *http.Request) (statePayload, bool) {
	state := r.URL.Query().Get("state")
	token, encoded, _ := strings.Cut(state, ".")
	var payload statePayload
	if raw, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
		_ = json.Unmarshal(raw, &payload)
	}
	c, err := r.Cookie(stateCookie)
	// Expire the state cookie regardless of outcome: one attempt per state.
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    "",
		Path:     "/auth/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
	if err != nil || token == "" || c.Value != HashToken(token) {
		return payload, false
	}
	return payload, true
}

func (m *Manager) redirectLoginErr(w http.ResponseWriter, r *http.Request, payload statePayload, msg string) {
	q := url.Values{}
	q.Set("err", msg)
	if payload.Next != "" {
		q.Set("next", payload.Next)
	}
	if payload.Invite != "" {
		q.Set("invite", payload.Invite)
	}
	http.Redirect(w, r, "/auth/login?"+q.Encode(), http.StatusFound)
}

// ── /auth/logout ──────────────────────────────────────────────────────────

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = m.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secureCookies(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}

// ── /auth/me ──────────────────────────────────────────────────────────────

// handleMe reports the current session's identity as JSON — the SPA's "who am
// I" call and the EventSource 401 probe (EventSource cannot observe HTTP
// status, so the frontend probes this endpoint when a stream errors).
func (m *Manager) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := m.sessionUser(r)
	if !ok {
		writeUnauthenticated(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"login": user.GitHubLogin,
		"name":  user.DisplayName,
		"role":  user.Role,
	})
}

// ── /auth/check ──────────────────────────────────────────────

// handleCheck is the reverse-proxy authentication seam. Caddy's
// forward_auth directive clones the original request as a GET, adds
// X-Forwarded-Method and X-Forwarded-Uri, and considers any 2xx response
// authenticated. A live session therefore gets a small 200 response carrying
// the authoritative actor header for the upstream. An unauthenticated browser
// page load is redirected into the normal Kitsoki login flow while API/SSE
// traffic receives the same fail-fast 401 JSON as Manager.Wrap.
//
// Keeping this check inside Manager is important: the proxy never learns the
// session-store schema and never accepts a client-supplied identity header.
func (m *Manager) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	user, ok := m.sessionUser(r)
	if !ok {
		if service, ok := m.serviceActor(r); ok {
			w.Header().Set(actorHeader, service)
			w.WriteHeader(http.StatusOK)
			return
		}
		r.Header.Del(actorHeader)
		method := r.Header.Get("X-Forwarded-Method")
		if method == "" {
			method = r.Method
		}
		if (method == http.MethodGet || method == http.MethodHead) && strings.Contains(r.Header.Get("Accept"), "text/html") {
			next := sanitizeNext(r.Header.Get("X-Forwarded-Uri"))
			http.Redirect(w, r, loginPath(next), http.StatusFound)
			return
		}
		writeUnauthenticated(w)
		return
	}
	w.Header().Set(actorHeader, user.GitHubLogin)
	w.WriteHeader(http.StatusOK)
}

// ── helpers ───────────────────────────────────────────────────────────────

// redirectURI is the OAuth callback URL registered with the GitHub App:
// <public_url>/auth/github/callback, or derived from the request when no
// public URL is configured.
func (m *Manager) redirectURI(r *http.Request) string {
	if m.cfg.PublicURL != "" {
		return strings.TrimRight(m.cfg.PublicURL, "/") + "/auth/github/callback"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/auth/github/callback"
}

// secureCookies marks cookies Secure when the deployment is https — either
// this hop (r.TLS) or the configured public URL (TLS terminated upstream).
func (m *Manager) secureCookies(r *http.Request) bool {
	if r != nil && r.TLS != nil {
		return true
	}
	return strings.HasPrefix(strings.ToLower(m.cfg.PublicURL), "https://")
}

// sanitizeNext confines a post-login redirect target to a same-origin path,
// rejecting absolute URLs and scheme-relative ("//host") escapes.
func sanitizeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	return next
}

// loginURL builds the login redirect for an unauthenticated page load,
// preserving the original URL as ?next=.
func loginURL(u *url.URL) string {
	next := u.Path
	if u.RawQuery != "" {
		next += "?" + u.RawQuery
	}
	return loginPath(next)
}

func loginPath(next string) string {
	if next = sanitizeNext(next); next == "" || next == "/" {
		return "/auth/login"
	}
	return "/auth/login?next=" + url.QueryEscape(next)
}

// wantsHTML distinguishes a browser page load (redirect to login) from the
// SPA's fetch/EventSource traffic (401 JSON). fetch to /rpc sends no
// text/html Accept; EventSource sends text/event-stream.
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeUnauthenticated(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(w, `{"error":"unauthenticated"}`)
}
