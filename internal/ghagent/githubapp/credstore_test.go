package githubapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCredentialsDir, dir)
	if got := CredentialsDir(); got != dir {
		t.Errorf("CredentialsDir = %s, want %s", got, dir)
	}
	if got := AppProfileDir("acme"); got != filepath.Join(dir, "gh-app", "acme") {
		t.Errorf("AppProfileDir = %s", got)
	}
}

func TestSetDefaultProfileSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCredentialsDir, dir)
	if err := os.MkdirAll(AppProfileDir("one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultProfile("one"); err != nil {
		t.Fatalf("SetDefaultProfile: %v", err)
	}
	// Re-pointing must replace, not fail.
	if err := os.MkdirAll(AppProfileDir("two"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultProfile("two"); err != nil {
		t.Fatalf("SetDefaultProfile repoint: %v", err)
	}
	target, err := os.Readlink(DefaultProfileLink())
	if err != nil || target != "two" {
		t.Fatalf("default link -> %q, %v", target, err)
	}
	// A real directory at the link path is refused, not clobbered.
	if err := os.Remove(DefaultProfileLink()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(DefaultProfileLink(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultProfile("one"); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("want not-a-symlink refusal, got %v", err)
	}
}

func TestTokenCachePathSanitizesClientID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCredentialsDir, dir)
	got := TokenCachePath("Iv1.abc/../evil")
	tokensDir := filepath.Join(dir, "gh-app", "tokens")
	if filepath.Dir(got) != tokensDir {
		t.Errorf("TokenCachePath escaped the tokens dir: %s", got)
	}
	if strings.ContainsAny(filepath.Base(got), "/\\") {
		t.Errorf("TokenCachePath base contains separators: %s", got)
	}
}

func TestResolveAppClientPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCredentialsDir, dir)

	// Default profile on disk.
	profile := AppProfileDir("prof")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	envBody := EnvClientID + "=profile-id\n" + EnvClientSecret + "=profile-secret\n" + EnvInstallationID + "=111\n"
	if err := os.WriteFile(filepath.Join(profile, "kitsoki.env"), []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultProfile("prof"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvClientID, "")
	t.Setenv(EnvClientSecret, "")
	t.Setenv(EnvInstallationID, "")
	t.Setenv(EnvAppEnvFile, "")

	// 4th tier: default profile.
	cc, err := ResolveAppClient("", "", 0, "")
	if err != nil {
		t.Fatalf("ResolveAppClient: %v", err)
	}
	if cc.ClientID != "profile-id" || cc.InstallationID != 111 || !strings.HasPrefix(cc.Source, "default profile") {
		t.Errorf("default profile resolution wrong: %+v", cc)
	}

	// 3rd tier: explicit env file beats the default profile.
	alt := filepath.Join(dir, "alt.env")
	if err := os.WriteFile(alt, []byte(EnvClientID+"=alt-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cc, err = ResolveAppClient("", "", 0, alt)
	if err != nil {
		t.Fatalf("ResolveAppClient env-file: %v", err)
	}
	if cc.ClientID != "alt-id" || !strings.HasPrefix(cc.Source, "env-file ") {
		t.Errorf("env-file resolution wrong: %+v", cc)
	}

	// 2nd tier: process env beats files.
	t.Setenv(EnvClientID, "env-id")
	cc, err = ResolveAppClient("", "", 0, alt)
	if err != nil {
		t.Fatalf("ResolveAppClient env: %v", err)
	}
	if cc.ClientID != "env-id" || cc.Source != "env" {
		t.Errorf("env resolution wrong: %+v", cc)
	}

	// 1st tier: flags beat everything.
	cc, err = ResolveAppClient("flag-id", "", 0, alt)
	if err != nil {
		t.Fatalf("ResolveAppClient flags: %v", err)
	}
	if cc.ClientID != "flag-id" || cc.Source != "flags" {
		t.Errorf("flag resolution wrong: %+v", cc)
	}
}

func TestResolveAppClientExplicitFileErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvCredentialsDir, dir)
	t.Setenv(EnvClientID, "")
	t.Setenv(EnvAppEnvFile, "")
	if _, err := ResolveAppClient("", "", 0, filepath.Join(dir, "missing.env")); err == nil {
		t.Fatal("explicitly named env file must error when unreadable")
	}
	// But a missing DEFAULT profile is fine — just unresolved.
	cc, err := ResolveAppClient("", "", 0, "")
	if err != nil {
		t.Fatalf("missing default profile must not error: %v", err)
	}
	if cc.ClientID != "" {
		t.Errorf("expected unresolved client id, got %+v", cc)
	}
}
