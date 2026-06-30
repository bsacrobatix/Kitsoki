package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectProfileValidateProfileJSONEnvelope(t *testing.T) {
	profile := `{
		"schema": "project-profile/v1",
		"id": "acme-rust",
		"title": "Acme Rust",
		"repo": {"root": ".", "vcs": "git"},
		"stack": {"kind": "rust", "languages": ["rust"]},
		"commands": {"dev": "make dev", "test": "make test", "build": "make build"},
		"kitsoki": {
			"story": "dev-story",
			"instance": {
				"id": "acme-rust-dev",
				"path": ".kitsoki/stories/acme-rust-dev/app.yaml",
				"bindings": {
					"ticket": "host.local_files.ticket",
					"vcs": "host.git",
					"ci": "host.local",
					"workspace": "host.git_worktree",
					"transport": "host.append_to_file"
				}
			}
		}
	}`

	out, err := execRoot(t,
		"project-profile", "validate",
		"--json",
		"--envelope",
		"--repo-root", t.TempDir(),
		"--profile-json", profile,
	)
	if err != nil {
		t.Fatalf("validate --profile-json --envelope: %v\n%s", err, out)
	}
	var env struct {
		OK                bool           `json:"ok"`
		Profile           map[string]any `json:"profile"`
		ProfileJSON       string         `json:"profile_json"`
		Schema            []string       `json:"schema"`
		Semantic          []string       `json:"semantic"`
		Warnings          []string       `json:"warnings"`
		ValidatorStdout   string         `json:"validator_stdout"`
		ValidatorStderr   string         `json:"validator_stderr"`
		ValidatorExitCode int            `json:"validator_exit_code"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope is not json: %v\n%s", err, out)
	}
	if !env.OK || env.ValidatorExitCode != 0 {
		t.Fatalf("envelope should validate: %+v", env)
	}
	if got := env.Profile["id"]; got != "acme-rust" {
		t.Fatalf("profile id: got %v", got)
	}
	if env.ProfileJSON == "" || !strings.Contains(env.ProfileJSON, `"id":"acme-rust"`) {
		t.Fatalf("profile_json missing compact profile: %q", env.ProfileJSON)
	}
	if len(env.Schema) != 0 || len(env.Semantic) != 0 {
		t.Fatalf("unexpected validation errors: schema=%v semantic=%v", env.Schema, env.Semantic)
	}
	if env.ValidatorStderr != "" {
		t.Fatalf("validator_stderr: %q", env.ValidatorStderr)
	}
	if !strings.Contains(env.ValidatorStdout, `"ok": true`) {
		t.Fatalf("validator_stdout missing validation report: %q", env.ValidatorStdout)
	}
}

func TestProjectProfileValidateEnvelopeReturnsZeroForInvalidProfile(t *testing.T) {
	out, err := execRoot(t,
		"project-profile", "validate",
		"--json",
		"--envelope",
		"--profile-json", `{"schema":"project-profile/v1"}`,
	)
	if err != nil {
		t.Fatalf("invalid envelope should return zero so host.run can bind stdout_json: %v\n%s", err, out)
	}
	var env struct {
		OK                bool     `json:"ok"`
		Schema            []string `json:"schema"`
		ValidatorExitCode int      `json:"validator_exit_code"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope is not json: %v\n%s", err, out)
	}
	if env.OK {
		t.Fatalf("invalid profile reported ok: %s", out)
	}
	if env.ValidatorExitCode != 1 {
		t.Fatalf("validator_exit_code: got %d, want 1", env.ValidatorExitCode)
	}
	if len(env.Schema) == 0 {
		t.Fatalf("expected schema errors: %s", out)
	}
}
