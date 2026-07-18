package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/webauth"
)

// buildAuthedIdentityServer wires a live webauth.Manager (temp SQLite store,
// fake GitHub) into a NewWithSource server via WithAuth, mirroring how
// cmd/kitsoki/web.go wires it in production. It is white-box (package server)
// for the same reason identity_test.go is: captureDriver satisfies the
// unexported Driver interface.
func buildAuthedIdentityServer(t *testing.T) (*httptest.Server, *captureDriver, *webauth.Store, *http.Client) {
	t.Helper()
	drv := &captureDriver{}

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	store, closeDB, err := webauth.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeDB() })

	ghUser := webauth.GitHubUser{ID: 1, Login: "authed-op", Name: "Authed Op"}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghUser)
	})
	ghSrv := httptest.NewServer(mux)
	t.Cleanup(ghSrv.Close)

	gh := &webauth.GitHubClient{
		ClientID: "cid", ClientSecret: "secret",
		AuthorizeURL: ghSrv.URL + "/login/oauth/authorize",
		TokenURL:     ghSrv.URL + "/login/oauth/access_token",
		UserURL:      ghSrv.URL + "/user",
	}
	authMgr := webauth.NewManager(store, gh, webauth.Config{})

	ts := httptest.NewServer(NewWithSource(stubSource{def: &app.AppDef{}}, WithDriver(drv), WithAuth(authMgr)).Handler())
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return ts, drv, store, client
}

func TestAuth_UnauthenticatedRPCIs401(t *testing.T) {
	t.Parallel()
	ts, _, _, client := buildAuthedIdentityServer(t)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"runstatus.session.submit","params":{"intent":"go"}}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/rpc", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_UnauthenticatedEventsIs401(t *testing.T) {
	t.Parallel()
	ts, _, _, client := buildAuthedIdentityServer(t)
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/rpc/events", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestAuth_AuthenticatedTurnRecordsGitHubLoginAsAuthor drives the full login
// round-trip (invite → GitHub → session cookie) and then proves the resolved
// identity — not any client-supplied header — is what lands in slots.author,
// matching the identity_test.go precedence contract but now under the gate.
func TestAuth_AuthenticatedTurnRecordsGitHubLoginAsAuthor(t *testing.T) {
	t.Parallel()
	ts, drv, store, client := buildAuthedIdentityServer(t)
	ctx := context.Background()

	_, code, err := store.CreateInvite(ctx, "Authed Op", webauth.RoleUser)
	require.NoError(t, err)

	resp, err := client.Get(ts.URL + "/auth/github/start?invite=" + code)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state)

	resp, err = client.Get(ts.URL + "/auth/github/callback?code=fake&state=" + state)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/rpc",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"runstatus.session.submit","params":{"intent":"go"}}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kitsoki-Actor", "spoofed-identity")
	resp, err = client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "authed-op", drv.lastSlots["author"], "the gate must overwrite a client-supplied actor header with the session identity")
}
