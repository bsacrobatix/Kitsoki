package webauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noRedirectClient returns an *http.Client that stops at the first redirect
// so tests can inspect the Location header and any Set-Cookie directly.
func noRedirectClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// buildTestManager wires a Manager backed by a temp SQLite store and a fake
// GitHub server, plus a stub protected handler so Wrap's gate can be
// exercised end to end.
func buildTestManager(t *testing.T, admins []string) (*httptest.Server, *httptest.Server, *Manager) {
	t.Helper()
	store := newTestStore(t)
	ghUser := GitHubUser{ID: 55, Login: "carol", Name: "Carol"}
	gh := fakeGitHub(t, "", "", ghUser, http.StatusOK)

	mgr := NewManager(store, clientFor(gh), Config{Admins: admins})

	// Mirror server.Handler's composition: Mount registers /auth/* on the
	// SAME mux that Wrap's bypass delegates to for those paths, and every
	// other route (including this stub) goes through the gate.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Actor", r.Header.Get(actorHeader))
		w.WriteHeader(http.StatusOK)
	})
	mgr.Mount(mux)
	handler := mgr.Wrap(mux)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, gh, mgr
}

func TestManager_UnauthenticatedHTMLRedirectsToLogin(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildTestManager(t, nil)
	client := noRedirectClient(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/some/page", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "/auth/login", loc.Path)
	assert.Equal(t, "/some/page", loc.Query().Get("next"))
}

func TestManager_UnauthenticatedRPCGets401JSON(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildTestManager(t, nil)
	client := noRedirectClient(t)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/rpc", nil)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestManager_UnauthenticatedSSEGets401(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildTestManager(t, nil)
	client := noRedirectClient(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/rpc/events", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestManager_ForwardAuthRedirectsHTMLToLoginWithOriginalURI(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildTestManager(t, nil)
	client := noRedirectClient(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/auth/check", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Forwarded-Method", http.MethodGet)
	req.Header.Set("X-Forwarded-Uri", "/portfolio?repo=pog")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "/auth/login", loc.Path)
	assert.Equal(t, "/portfolio?repo=pog", loc.Query().Get("next"))
}

func TestManager_ForwardAuthRejectsAPIWith401(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildTestManager(t, nil)
	client := noRedirectClient(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/auth/check", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Forwarded-Method", http.MethodPost)
	req.Header.Set("X-Forwarded-Uri", "/api/streams/rpc")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestManager_FullInviteLoginFlow(t *testing.T) {
	t.Parallel()
	srv, _, mgr := buildTestManager(t, nil)
	ctx := context.Background()

	_, code, err := mgr.store.CreateInvite(ctx, "Carol", RoleUser)
	require.NoError(t, err)

	client := noRedirectClient(t)

	// 1. Invite landing redirects to login carrying the code.
	resp, err := client.Get(srv.URL + "/auth/invite?code=" + url.QueryEscape(code))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "/auth/login", loc.Path)
	assert.Equal(t, code, loc.Query().Get("invite"))

	// 2. Login page GET (just confirm 200, HTML card).
	resp, err = client.Get(srv.URL + "/auth/login?invite=" + url.QueryEscape(code))
	require.NoError(t, err)
	body := readAll(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "Sign in with GitHub")

	// 3. Start → redirect to (fake) GitHub, with a state cookie set.
	resp, err = client.Get(srv.URL + "/auth/github/start?invite=" + url.QueryEscape(code))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	ghLoc, err := resp.Location()
	require.NoError(t, err)
	state := ghLoc.Query().Get("state")
	require.NotEmpty(t, state)

	// 4. Callback with a fake code and the real state — should redeem the
	// invite, set the session cookie, and land on "/" (no explicit next).
	cbURL := srv.URL + "/auth/github/callback?code=fake-code&state=" + url.QueryEscape(state)
	resp, err = client.Get(cbURL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	final, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "/", final.Path)

	var setCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			setCookie = c
		}
	}
	require.NotNil(t, setCookie, "callback must set the session cookie")
	assert.NotEmpty(t, setCookie.Value)
	assert.True(t, setCookie.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, setCookie.SameSite)

	var sessionCookieVal string
	for _, c := range client.Jar.Cookies(mustParseURL(t, srv.URL)) {
		if c.Name == sessionCookie {
			sessionCookieVal = c.Value
		}
	}
	assert.NotEmpty(t, sessionCookieVal, "jar must have picked up the session cookie for subsequent requests")

	// 5. Gated request now succeeds and carries the resolved GitHub login as
	// the actor header — even if the client tries to spoof a different one.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/some/protected", nil)
	require.NoError(t, err)
	req.Header.Set(actorHeader, "someone-else")
	resp, err = client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "carol", resp.Header.Get("X-Seen-Actor"), "spoofed header must be overwritten with the session identity")

	// The reverse-proxy check accepts the same session and returns the actor as
	// a response header for Caddy's copy_headers directive.
	checkReq, err := http.NewRequest(http.MethodGet, srv.URL+"/auth/check", nil)
	require.NoError(t, err)
	checkReq.Header.Set(actorHeader, "someone-else")
	resp, err = client.Do(checkReq)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "carol", resp.Header.Get(actorHeader))

	// 6. /auth/me reports the identity.
	resp, err = client.Get(srv.URL + "/auth/me")
	require.NoError(t, err)
	meBody := readAll(t, resp)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var me map[string]string
	require.NoError(t, json.Unmarshal(meBody, &me))
	assert.Equal(t, "carol", me["login"])
	assert.Equal(t, RoleUser, me["role"])

	// 7. Logout clears the session; the same cookie no longer authenticates.
	logoutReq, err := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout", nil)
	require.NoError(t, err)
	resp, err = client.Do(logoutReq)
	require.NoError(t, err)
	_ = resp.Body.Close()

	resp, err = client.Get(srv.URL + "/auth/me")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestManager_CallbackWithBadStateIsRejected(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildTestManager(t, nil)
	client := noRedirectClient(t)

	resp, err := client.Get(srv.URL + "/auth/github/callback?code=x&state=totally-wrong")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "/auth/login", loc.Path)
	assert.NotEmpty(t, loc.Query().Get("err"))
}

func TestManager_UnknownGitHubAccountWithoutInviteIsForbidden(t *testing.T) {
	t.Parallel()
	srv, _, _ := buildTestManager(t, nil)
	client := noRedirectClient(t)

	resp, err := client.Get(srv.URL + "/auth/github/start")
	require.NoError(t, err)
	_ = resp.Body.Close()
	ghLoc, err := resp.Location()
	require.NoError(t, err)
	state := ghLoc.Query().Get("state")

	resp, err = client.Get(srv.URL + "/auth/github/callback?code=fake-code&state=" + url.QueryEscape(state))
	require.NoError(t, err)
	body := readAll(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, string(body), "no invitation")
}

func TestManager_AdminsListAutoProvisions(t *testing.T) {
	t.Parallel()
	srv, _, mgr := buildTestManager(t, []string{"carol"})
	client := noRedirectClient(t)

	resp, err := client.Get(srv.URL + "/auth/github/start")
	require.NoError(t, err)
	_ = resp.Body.Close()
	ghLoc, err := resp.Location()
	require.NoError(t, err)
	state := ghLoc.Query().Get("state")

	resp, err = client.Get(srv.URL + "/auth/github/callback?code=fake-code&state=" + url.QueryEscape(state))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	u, ok, err := mgr.store.UserByGitHubID(context.Background(), 55)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, RoleAdmin, u.Role)
}

func TestManager_SecureCookieInferredFromPublicURL(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	mgr := NewManager(store, &GitHubClient{}, Config{PublicURL: "https://kitsoki.example.com"})
	req := httptest.NewRequest(http.MethodGet, "http://kitsoki.example.com/auth/login", nil)
	assert.True(t, mgr.secureCookies(req))

	mgr2 := NewManager(store, &GitHubClient{}, Config{})
	assert.False(t, mgr2.secureCookies(req))
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}
