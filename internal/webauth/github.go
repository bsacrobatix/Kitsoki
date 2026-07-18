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
	defaultDeviceURL    = "https://github.com/login/device/code"
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
	DeviceURL    string
	TokenURL     string
	UserURL      string

	// HTTPClient defaults to a 10s-timeout client.
	HTTPClient *http.Client
}

// DeviceAuthorization is GitHub's short-lived browser handoff for Device
// Flow. DeviceCode stays server-side; UserCode is the value shown to the
// person signing in.
type DeviceAuthorization struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       time.Duration
	Interval        time.Duration
}

// DevicePollState describes a non-error response from GitHub's device-token
// endpoint.
type DevicePollState int

const (
	DevicePollPending DevicePollState = iota
	DevicePollSlowDown
	DevicePollComplete
)

// DevicePollResult is one poll of a pending Device Flow authorization.
type DevicePollResult struct {
	State       DevicePollState
	AccessToken string
	Interval    time.Duration
}

// NewGitHubClient builds a client for the given GitHub App credentials with
// the production github.com endpoints. ClientSecret may be empty when the
// Manager is explicitly configured for Device Flow.
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

// StartDeviceAuthorization asks GitHub for a one-time user code. It is used
// by hosted deployments that deliberately select Device Flow and therefore
// do not need a GitHub App client secret or callback registration.
func (c *GitHubClient) StartDeviceAuthorization(ctx context.Context) (DeviceAuthorization, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.deviceURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("webauth: github device-code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("webauth: github device-code request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return DeviceAuthorization{}, fmt.Errorf("webauth: github device-code response: %w", err)
	}
	var out struct {
		DeviceCode       string `json:"device_code"`
		UserCode         string `json:"user_code"`
		VerificationURI  string `json:"verification_uri"`
		ExpiresIn        int64  `json:"expires_in"`
		Interval         int64  `json:"interval"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return DeviceAuthorization{}, fmt.Errorf("webauth: github device-code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		return DeviceAuthorization{}, fmt.Errorf("webauth: github device-code request: HTTP %d %s (%s)", resp.StatusCode, out.Error, out.ErrorDescription)
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "" {
		return DeviceAuthorization{}, fmt.Errorf("webauth: github device-code response missing required fields")
	}
	expires := time.Duration(out.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 15 * time.Minute
	}
	interval := time.Duration(out.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return DeviceAuthorization{
		DeviceCode:      out.DeviceCode,
		UserCode:        out.UserCode,
		VerificationURI: out.VerificationURI,
		ExpiresIn:       expires,
		Interval:        interval,
	}, nil
}

// PollDeviceAuthorization performs one Device Flow token poll. The Manager
// controls scheduling so an HTTP request never blocks while a person is
// completing GitHub's one-time-code screen.
func (c *GitHubClient) PollDeviceAuthorization(ctx context.Context, deviceCode string) (DevicePollResult, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return DevicePollResult{}, fmt.Errorf("webauth: github device-token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return DevicePollResult{}, fmt.Errorf("webauth: github device-token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Interval         int64  `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return DevicePollResult{}, fmt.Errorf("webauth: github device-token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return DevicePollResult{}, fmt.Errorf("webauth: github device-token request: HTTP %d", resp.StatusCode)
	}
	switch out.Error {
	case "":
		if out.AccessToken == "" {
			return DevicePollResult{}, fmt.Errorf("webauth: github device-token response: empty access_token")
		}
		return DevicePollResult{State: DevicePollComplete, AccessToken: out.AccessToken}, nil
	case "authorization_pending":
		return DevicePollResult{State: DevicePollPending}, nil
	case "slow_down":
		interval := time.Duration(out.Interval) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		return DevicePollResult{State: DevicePollSlowDown, Interval: interval}, nil
	case "expired_token":
		return DevicePollResult{}, fmt.Errorf("webauth: github device code expired")
	case "access_denied":
		return DevicePollResult{}, fmt.Errorf("webauth: github device authorization denied")
	default:
		return DevicePollResult{}, fmt.Errorf("webauth: github device flow: %s (%s)", out.Error, out.ErrorDescription)
	}
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

func (c *GitHubClient) deviceURL() string {
	if c.DeviceURL != "" {
		return c.DeviceURL
	}
	return defaultDeviceURL
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
