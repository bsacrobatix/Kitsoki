// web_auth.go — wires the internal/webauth login gate into `kitsoki web` /
// `kitsoki daemon` (buildWebAuth) and implements `kitsoki web invite`, the
// operator command that mints the one-time links the login flow redeems.
package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/webauth"
	"kitsoki/internal/webconfig"
)

// resolveAuthRequired collapses the config's off/required/auto mode against
// the actual listen address. A nil auth block means "auto".
func resolveAuthRequired(a *webconfig.AuthConfig, addr string) bool {
	mode := webconfig.AuthModeAuto
	if a != nil && a.Mode != "" {
		mode = a.Mode
	}
	switch mode {
	case webconfig.AuthModeOff:
		return false
	case webconfig.AuthModeRequired:
		return true
	default: // auto
		return !webauth.IsLoopbackAddr(addr)
	}
}

// buildWebAuth turns the config's auth block into a ready webauth.Manager, or
// (nil, nil, nil) when auth is off for this addr. It fails fast on a required
// deployment with no GitHub OAuth App configured — better a startup error
// than an open public surface or an unusable login page. The returned close
// func (when non-nil) owns the auth store's DB handle.
func buildWebAuth(a *webconfig.AuthConfig, addr, dbPath string, warnTo io.Writer) (*webauth.Manager, func() error, error) {
	if !resolveAuthRequired(a, addr) {
		return nil, nil, nil
	}
	if a == nil || a.GitHub.ClientID == "" {
		return nil, nil, fmt.Errorf("auth is required for --addr %s but auth.github.client_id is not configured; set it in .kitsoki.local.yaml (or set auth.mode: off to serve without login)", addr)
	}
	if !a.GitHub.DeviceFlow && a.GitHub.ClientSecret == "" {
		return nil, nil, fmt.Errorf("auth is required for --addr %s but callback login needs auth.github.client_secret; configure it or explicitly set auth.github.device_flow: true", addr)
	}
	ttl := webauth.DefaultSessionTTL
	if a.SessionTTL != "" {
		// Already validated by webconfig.Load.
		if d, err := time.ParseDuration(a.SessionTTL); err == nil {
			ttl = d
		}
	}
	if a.PublicURL != "" {
		if u, err := url.Parse(a.PublicURL); err == nil && u.Scheme == "http" {
			fmt.Fprintf(warnTo, "kitsoki: auth.public_url %s is http — session cookies will not be marked Secure; prefer https in production\n", a.PublicURL)
		}
	}
	store, closeStore, err := webauth.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open auth store: %w", err)
	}
	tokens, err := resolveServiceTokens(a.ServiceTokens, warnTo)
	if err != nil {
		_ = closeStore()
		return nil, nil, err
	}
	mgr := webauth.NewManager(store, webauth.NewGitHubClient(a.GitHub.ClientID, a.GitHub.ClientSecret), webauth.Config{
		PublicURL:     a.PublicURL,
		Admins:        a.Admins,
		SessionTTL:    ttl,
		DeviceFlow:    a.GitHub.DeviceFlow,
		ServiceTokens: tokens,
	})
	return mgr, closeStore, nil
}

// resolveServiceTokens turns the config's service→env-var-name map into
// service→token values. An unset env var disables that service's token with a
// warning (the deployment may legitimately not run that service); a set but
// weak token is a hard startup error — better to fail fast than accept a
// guessable bearer credential on a public surface.
func resolveServiceTokens(named map[string]string, warnTo io.Writer) (map[string]string, error) {
	if len(named) == 0 {
		return nil, nil
	}
	const minServiceTokenLen = 16
	tokens := make(map[string]string, len(named))
	for name, envName := range named {
		value := os.Getenv(envName)
		if value == "" {
			fmt.Fprintf(warnTo, "kitsoki: auth.service_tokens.%s: env var %s is not set; bearer access for this service is disabled\n", name, envName)
			continue
		}
		if len(value) < minServiceTokenLen {
			return nil, fmt.Errorf("auth.service_tokens.%s: env var %s holds a token shorter than %d characters; mint a stronger secret (e.g. `openssl rand -hex 32`)", name, envName, minServiceTokenLen)
		}
		tokens[name] = value
	}
	return tokens, nil
}

// webInviteCmd implements `kitsoki web invite` (also mounted under `kitsoki
// daemon invite`): mint a one-time invite link tied to a person and print it
// for the operator to share out of band. Runs against the same sessions.db
// the server uses — WAL makes that safe while the daemon is up — and creates
// the schema if the server has never run.
func webInviteCmd() *cobra.Command {
	var (
		admin      bool
		list       bool
		dbPath     string
		configPath string
		baseURL    string
	)
	cmd := &cobra.Command{
		Use:   "invite [name]",
		Short: "Mint a one-time login invite link (or --list existing invites)",
		Long: `Mint a one-time invite link tied to a person. Share the printed URL with them
out of band (chat, email); opening it and signing in with GitHub associates
their GitHub account and logs them in. Codes are stored only as hashes — the
link is shown once, here.

The link's base URL resolves --base-url > auth.public_url (.kitsoki.yaml) >
http://127.0.0.1:7777.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbPath == "" {
				dbPath = defaultDBPath()
			}
			store, closeStore, err := webauth.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open auth store: %w", err)
			}
			defer func() { _ = closeStore() }()

			if list {
				return printInvites(cmd.OutOrStdout(), store)
			}
			if len(args) != 1 {
				return fmt.Errorf("invite needs the person's name (or --list)")
			}
			name := args[0]
			role := webauth.RoleUser
			if admin {
				role = webauth.RoleAdmin
			}
			if baseURL == "" {
				if cfg, err := webconfig.Load(configPath); err == nil && cfg.Auth != nil && cfg.Auth.PublicURL != "" {
					baseURL = cfg.Auth.PublicURL
				} else {
					baseURL = "http://127.0.0.1:7777"
				}
			}
			inv, code, err := store.CreateInvite(context.Background(), name, role)
			if err != nil {
				return err
			}
			link := fmt.Sprintf("%s/auth/invite?code=%s", trimTrailingSlash(baseURL), url.QueryEscape(code))
			fmt.Fprintf(cmd.OutOrStdout(), "Invitation for %q (role: %s)\nShare this one-time link:\n\n  %s\n", inv.Name, inv.Role, link)
			return nil
		},
	}
	cmd.Flags().BoolVar(&admin, "admin", false, "grant the admin role on redemption")
	cmd.Flags().BoolVar(&list, "list", false, "list existing invites instead of creating one")
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite session store path (default: nearest .kitsoki/sessions.db — must match the server's --db)")
	cmd.Flags().StringVar(&configPath, "config", webconfig.DefaultConfigFile, "path to the web config file (for auth.public_url)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "base URL for the printed link (default: auth.public_url, else http://127.0.0.1:7777)")
	return cmd
}

func printInvites(w io.Writer, store *webauth.Store) error {
	invites, err := store.ListInvites(context.Background())
	if err != nil {
		return err
	}
	if len(invites) == 0 {
		fmt.Fprintln(w, "no invites")
		return nil
	}
	for _, inv := range invites {
		status := "pending"
		if inv.RedeemedAt != nil {
			status = "redeemed " + inv.RedeemedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s  %-20s %-5s %s (created %s)\n", inv.ID, inv.Name, inv.Role, status, inv.CreatedAt.Format(time.RFC3339))
	}
	return nil
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
