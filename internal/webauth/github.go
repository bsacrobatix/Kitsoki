package webauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default GitHub endpoints. Kept as fields on [GitHubClient] so tests point
// them at a local fake server — automated tests never call GitHub.
const (
	defaultAuthorizeURL = "https://github.com/login/oauth/authorize"
	defaultTokenURL     = "https://github.com/login/oauth/access_token"
	defaultUserURL      = "https://api.github.com/user"
)

// GitHubUser is the slice of the GitHub /user profile kitsoki keys identity
// on. ID is the stable anchor (logins can be renamed).
type GitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// GitHubClient runs the standard OAuth App web flow with plain net/http: build
// the authorize redirect, exchange the callback code for an access token, and
// read the user's public profile. The token is used once for FetchUser and
// discarded — kitsoki keeps no GitHub credential.
type GitHubClient struct {
	ClientID     string
	ClientSecret string

	// Endpoint overrides for tests; empty fields use the github.com defaults.
	AuthorizeURL string
	TokenURL     string
	UserURL      string

	// HTTPClient defaults to a 10s-timeout client.
	HTTPClient *http.Client
}

// NewGitHubClient builds a client for the given OAuth App credentials with
// the production github.com endpoints.
func NewGitHubClient(clientID, clientSecret string) *GitHubClient {
	return &GitHubClient{ClientID: clientID, ClientSecret: clientSecret}
}

// AuthCodeURL is the GitHub authorize URL the login page redirects to. No
// scope is requested: the default grant covers the public profile, which is
// all identity needs.
func (c *GitHubClient) AuthCodeURL(state, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	return c.authorizeURL() + "?" + q.Encode()
}

// Exchange trades the callback's authorization code for an access token.
func (c *GitHubClient) Exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("webauth: github token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("webauth: github token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("webauth: github token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("webauth: github token exchange: HTTP %d", resp.StatusCode)
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("webauth: github token response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("webauth: github token exchange: %s (%s)", out.Error, out.ErrorDescription)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("webauth: github token exchange: empty access_token")
	}
	return out.AccessToken, nil
}

// FetchUser reads the authenticated user's public profile.
func (c *GitHubClient) FetchUser(ctx context.Context, accessToken string) (GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userURL(), nil)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("webauth: github user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("webauth: github user fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return GitHubUser{}, fmt.Errorf("webauth: github user fetch: HTTP %d", resp.StatusCode)
	}
	var u GitHubUser
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return GitHubUser{}, fmt.Errorf("webauth: github user response: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return GitHubUser{}, fmt.Errorf("webauth: github user response missing id/login")
	}
	return u, nil
}

func (c *GitHubClient) authorizeURL() string {
	if c.AuthorizeURL != "" {
		return c.AuthorizeURL
	}
	return defaultAuthorizeURL
}

func (c *GitHubClient) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return defaultTokenURL
}

func (c *GitHubClient) userURL() string {
	if c.UserURL != "" {
		return c.UserURL
	}
	return defaultUserURL
}

func (c *GitHubClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}
