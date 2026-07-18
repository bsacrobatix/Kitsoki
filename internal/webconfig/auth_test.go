package webconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_AuthAbsentIsNil(t *testing.T) {
	cfg, err := loadConfigText(t, "story_dirs:\n  - ./stories\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth != nil {
		t.Fatalf("Auth = %v, want nil", cfg.Auth)
	}
}

func TestLoad_AuthValid(t *testing.T) {
	cfg, err := loadConfigText(t, `auth:
  mode: required
  public_url: https://kitsoki.example.com
  admins: [bradsmith]
  session_ttl: 168h
  github:
    client_id: abc123
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth == nil {
		t.Fatal("Auth = nil, want a resolved block")
	}
	if cfg.Auth.Mode != "required" {
		t.Errorf("Mode = %q, want required", cfg.Auth.Mode)
	}
	if cfg.Auth.PublicURL != "https://kitsoki.example.com" {
		t.Errorf("PublicURL = %q", cfg.Auth.PublicURL)
	}
	if len(cfg.Auth.Admins) != 1 || cfg.Auth.Admins[0] != "bradsmith" {
		t.Errorf("Admins = %v", cfg.Auth.Admins)
	}
	if cfg.Auth.GitHub.ClientID != "abc123" {
		t.Errorf("ClientID = %q", cfg.Auth.GitHub.ClientID)
	}
}

func TestLoad_AuthInvalidMode(t *testing.T) {
	_, err := loadConfigText(t, "auth:\n  mode: sometimes\n")
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("want mode validation error, got %v", err)
	}
}

func TestLoad_AuthInvalidSessionTTL(t *testing.T) {
	_, err := loadConfigText(t, "auth:\n  session_ttl: not-a-duration\n")
	if err == nil || !strings.Contains(err.Error(), "session_ttl") {
		t.Fatalf("want session_ttl validation error, got %v", err)
	}
}

func TestLoad_AuthInvalidPublicURL(t *testing.T) {
	_, err := loadConfigText(t, "auth:\n  public_url: not-a-url\n")
	if err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Fatalf("want public_url validation error, got %v", err)
	}
}

func TestLoad_AuthEmptyAdminEntry(t *testing.T) {
	_, err := loadConfigText(t, "auth:\n  admins: [\"\"]\n")
	if err == nil || !strings.Contains(err.Error(), "admins") {
		t.Fatalf("want admins validation error, got %v", err)
	}
}

func TestLoad_AuthClientSecretEnvExpansion(t *testing.T) {
	t.Setenv("KITSOKI_TEST_GH_SECRET", "shh-secret")
	cfg, err := loadConfigText(t, "auth:\n  github:\n    client_secret: ${KITSOKI_TEST_GH_SECRET}\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth.GitHub.ClientSecret != "shh-secret" {
		t.Errorf("ClientSecret = %q, want expanded value", cfg.Auth.GitHub.ClientSecret)
	}
}

func TestLoad_AuthClientSecretUnsetEnvErrors(t *testing.T) {
	_, err := loadConfigText(t, "auth:\n  github:\n    client_secret: ${KITSOKI_TEST_GH_SECRET_UNSET}\n")
	if err == nil || !strings.Contains(err.Error(), "KITSOKI_TEST_GH_SECRET_UNSET") {
		t.Fatalf("want unset-env error, got %v", err)
	}
}

func TestLoad_AuthLocalOverrideMergesSecret(t *testing.T) {
	t.Setenv("KITSOKI_TEST_GH_SECRET2", "local-secret")
	dir := t.TempDir()
	base := filepath.Join(dir, DefaultConfigFile)
	if err := os.WriteFile(base, []byte("auth:\n  mode: required\n  github:\n    client_id: base-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := LocalConfigPath(base)
	if err := os.WriteFile(local, []byte("auth:\n  github:\n    client_secret: ${KITSOKI_TEST_GH_SECRET2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth.Mode != "required" {
		t.Errorf("Mode = %q, want the base file's value preserved by the merge", cfg.Auth.Mode)
	}
	if cfg.Auth.GitHub.ClientID != "base-id" {
		t.Errorf("ClientID = %q, want the base file's value preserved by the merge", cfg.Auth.GitHub.ClientID)
	}
	if cfg.Auth.GitHub.ClientSecret != "local-secret" {
		t.Errorf("ClientSecret = %q, want the local override expanded and merged in", cfg.Auth.GitHub.ClientSecret)
	}
}
