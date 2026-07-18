package webauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGitHub is a minimal stand-in for github.com/login/oauth/* and
// api.github.com/user, so GitHubClient tests never touch the network.
func fakeGitHub(t *testing.T, wantCode string, tokenErr string, user GitHubUser, userStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if wantCode != "" {
			assert.Equal(t, wantCode, r.Form.Get("code"))
		}
		w.Header().Set("Content-Type", "application/json")
		if tokenErr != "" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": tokenErr, "error_description": "bad"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "fake-token", "token_type": "bearer"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(userStatus)
		if userStatus == http.StatusOK {
			_ = json.NewEncoder(w).Encode(user)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(srv *httptest.Server) *GitHubClient {
	return &GitHubClient{
		ClientID:     "cid",
		ClientSecret: "csecret",
		AuthorizeURL: srv.URL + "/login/oauth/authorize",
		TokenURL:     srv.URL + "/login/oauth/access_token",
		UserURL:      srv.URL + "/user",
	}
}

func TestGitHubClient_AuthCodeURL(t *testing.T) {
	t.Parallel()
	c := &GitHubClient{ClientID: "cid", AuthorizeURL: "https://example.test/authorize"}
	u := c.AuthCodeURL("state123", "https://kitsoki.example.com/auth/github/callback")
	assert.Contains(t, u, "https://example.test/authorize?")
	assert.Contains(t, u, "client_id=cid")
	assert.Contains(t, u, "state=state123")
	assert.Contains(t, u, "redirect_uri=https%3A%2F%2Fkitsoki.example.com%2Fauth%2Fgithub%2Fcallback")
}

func TestGitHubClient_ExchangeAndFetchUser(t *testing.T) {
	t.Parallel()
	want := GitHubUser{ID: 100, Login: "octocat", Name: "The Octocat"}
	srv := fakeGitHub(t, "the-code", "", want, http.StatusOK)
	c := clientFor(srv)

	tok, err := c.Exchange(context.Background(), "the-code", "https://kitsoki.example.com/auth/github/callback")
	require.NoError(t, err)
	assert.Equal(t, "fake-token", tok)

	got, err := c.FetchUser(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestGitHubClient_Exchange_ErrorResponse(t *testing.T) {
	t.Parallel()
	srv := fakeGitHub(t, "", "bad_verification_code", GitHubUser{}, http.StatusOK)
	c := clientFor(srv)
	_, err := c.Exchange(context.Background(), "wrong", "https://kitsoki.example.com/auth/github/callback")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bad_verification_code")
}

func TestGitHubClient_FetchUser_NonOK(t *testing.T) {
	t.Parallel()
	srv := fakeGitHub(t, "", "", GitHubUser{}, http.StatusUnauthorized)
	c := clientFor(srv)
	_, err := c.FetchUser(context.Background(), "bad-token")
	assert.Error(t, err)
}
