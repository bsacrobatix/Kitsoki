package webconfig

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Auth mode values for AuthConfig.Mode. "auto" is the default: login is
// required exactly when the web server binds a non-loopback address, so a dev
// machine keeps the historical trusted-localhost posture with zero config
// while a public deployment fails closed.
const (
	AuthModeOff      = "off"
	AuthModeRequired = "required"
	AuthModeAuto     = "auto"
)

// AuthConfig is the `auth:` block: invitation-only GitHub sign-in for the
// `kitsoki web` / `kitsoki daemon` HTTP surface. The GitHub client secret is
// secret-bearing and belongs in .kitsoki.local.yaml (use ${VAR} to reference
// the environment). See internal/webauth for the runtime.
type AuthConfig struct {
	// Mode is off | required | auto (default auto — required iff the listen
	// address is non-loopback).
	Mode string `yaml:"mode,omitempty"`
	// GitHub carries the selected GitHub App login flow and credentials.
	// ClientID is required when auth is effectively on; ClientSecret is only
	// required for callback login (validated at server startup, where the
	// listen address is known).
	GitHub GitHubAuthConfig `yaml:"github,omitempty"`
	// PublicURL is the externally visible base URL, e.g.
	// https://kitsoki.example.com. It anchors the OAuth redirect_uri
	// (<public_url>/auth/github/callback must be the GitHub App's callback),
	// invite links, and Secure-cookie inference behind a TLS-terminating
	// proxy.
	PublicURL string `yaml:"public_url,omitempty"`
	// Admins lists GitHub logins auto-provisioned as admins on sign-in — the
	// "admins are defined locally" path that needs no invite.
	Admins []string `yaml:"admins,omitempty"`
	// SessionTTL is the browser-session lifetime as a Go duration string.
	// Empty ⇒ webauth.DefaultSessionTTL (30 days).
	SessionTTL string `yaml:"session_ttl,omitempty"`
	// ServiceTokens maps a service name to the environment variable NAME
	// holding its bearer token, e.g. {colony: KITSOKI_COLONY_TOKEN}. Only the
	// env var name is checked in; the value is resolved at server startup
	// (typically from ~/.config/kitsoki/daemon.env). A request presenting
	// `Authorization: Bearer <token>` matching a resolved token passes the
	// login gate with actor "service:<name>" — the headless-automation path
	// (same-host colony runners, cron drivers) that has no browser session.
	ServiceTokens map[string]string `yaml:"service_tokens,omitempty"`
}

// GitHubAuthConfig selects and configures the GitHub user-authentication flow.
// ClientID is always required when auth is on. Callback-based web flow also
// requires ClientSecret; DeviceFlow deliberately does not.
type GitHubAuthConfig struct {
	ClientID     string `yaml:"client_id,omitempty"`
	ClientSecret string `yaml:"client_secret,omitempty"`
	DeviceFlow   bool   `yaml:"device_flow,omitempty"`
}

// resolveAuth validates the `auth:` block fail-fast at load, mirroring
// resolveIntercept: mode must be a known value, session_ttl must parse as a
// positive duration, public_url must be an absolute http(s) URL, and ${VAR}
// references in the GitHub credentials must resolve. A nil block defaults to
// {Mode: auto}. Credential presence is NOT checked here — whether credentials
// are required depends on the listen address, which only the server command
// knows; it enforces that at startup.
func (cfg *WebConfig) resolveAuth() error {
	a := cfg.Auth
	if a == nil {
		return nil
	}
	switch a.Mode {
	case "", AuthModeAuto, AuthModeOff, AuthModeRequired:
	default:
		return fmt.Errorf("auth.mode %q is not one of \"off\"|\"required\"|\"auto\"", a.Mode)
	}
	if a.Mode == "" {
		a.Mode = AuthModeAuto
	}
	for field, val := range map[string]*string{
		"auth.github.client_id":     &a.GitHub.ClientID,
		"auth.github.client_secret": &a.GitHub.ClientSecret,
	} {
		expanded, missing := expandEnvVar(*val)
		if missing != "" {
			return fmt.Errorf("%s: env var %s not set", field, missing)
		}
		*val = expanded
	}
	if a.PublicURL != "" {
		u, err := url.Parse(a.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("auth.public_url %q is not an absolute http(s) URL", a.PublicURL)
		}
	}
	if a.SessionTTL != "" {
		d, err := time.ParseDuration(a.SessionTTL)
		if err != nil {
			return fmt.Errorf("auth.session_ttl %q is not a valid duration: %w", a.SessionTTL, err)
		}
		if d <= 0 {
			return fmt.Errorf("auth.session_ttl %q must be positive", a.SessionTTL)
		}
	}
	for i, admin := range a.Admins {
		if strings.TrimSpace(admin) == "" {
			return fmt.Errorf("auth.admins[%d] is empty", i)
		}
	}
	for name, envName := range a.ServiceTokens {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("auth.service_tokens has an empty service name")
		}
		if strings.TrimSpace(envName) == "" || strings.ContainsAny(envName, " \t=") {
			return fmt.Errorf("auth.service_tokens.%s: %q is not an environment variable name (values are never checked in; name the env var holding the token)", name, envName)
		}
	}
	return nil
}
