package workerserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/capsule/executor"
)

// fakePreflightEnv builds a preflightEnv backed entirely by literals/maps —
// no real process environment, filesystem, or network — so every check
// below exercises the actual check logic against fake env/files, per the
// task's testing requirement.
func fakePreflightEnv(t *testing.T, env map[string]string, files map[string][]byte) preflightEnv {
	t.Helper()
	return preflightEnv{
		Getenv: func(k string) string { return env[k] },
		Stat: func(p string) (os.FileInfo, error) {
			if _, ok := files[p]; ok {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
		ReadFile: func(p string) ([]byte, error) {
			if b, ok := files[p]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
		UserHomeDir: func() (string, error) { return "/home/worker", nil },
		DiskFree:    func(string) (int64, bool, error) { return 0, false, nil },
		Now:         func() time.Time { return time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC) },
		Do:          func(*http.Request) (*http.Response, error) { return nil, errors.New("Do not installed for this test") },
	}
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o600 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

func TestRunPreflightSkipBypassesEveryCheck(t *testing.T) {
	env := fakePreflightEnv(t, nil, nil) // no auth material, would fail every check
	out := runPreflight(context.Background(), PreflightConfig{Skip: true}, env, "claude", "", "/ws")
	if !out.OK {
		t.Fatalf("Skip=true must bypass every check, got %+v", out)
	}
}

func TestPreflightClaudeAuth(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		files     map[string][]byte
		wantOK    bool
		wantClass executor.FailureClass
	}{
		{
			name:   "oauth token alone is sufficient",
			env:    map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok"},
			wantOK: true,
		},
		{
			name:      "base url set without auth token fails",
			env:       map[string]string{"ANTHROPIC_BASE_URL": "https://gateway.example/v1"},
			wantOK:    false,
			wantClass: executor.FailureClassPreflightAuth,
		},
		{
			name:   "base url set with auth token passes",
			env:    map[string]string{"ANTHROPIC_BASE_URL": "https://gateway.example/v1", "ANTHROPIC_AUTH_TOKEN": "tok"},
			wantOK: true,
		},
		{
			name:   "api key alone is sufficient",
			env:    map[string]string{"ANTHROPIC_API_KEY": "key"},
			wantOK: true,
		},
		{
			name:      "no env auth and no credentials file fails",
			env:       nil,
			wantOK:    false,
			wantClass: executor.FailureClassPreflightAuth,
		},
		{
			name:   "credentials file present with no recorded expiry passes",
			files:  map[string][]byte{"/home/worker/.claude/.credentials.json": []byte(`{"claudeAiOauth":{"accessToken":"x"}}`)},
			wantOK: true,
		},
		{
			name:   "credentials file present with future expiry passes",
			files:  map[string][]byte{"/home/worker/.claude/.credentials.json": []byte(fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d}}`, time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC).UnixMilli()))},
			wantOK: true,
		},
		{
			name:      "credentials file present but expired fails",
			files:     map[string][]byte{"/home/worker/.claude/.credentials.json": []byte(fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d}}`, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()))},
			wantOK:    false,
			wantClass: executor.FailureClassPreflightAuth,
		},
		{
			name:      "credentials file present but unparsable fails",
			files:     map[string][]byte{"/home/worker/.claude/.credentials.json": []byte("not json")},
			wantOK:    false,
			wantClass: executor.FailureClassPreflightAuth,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := fakePreflightEnv(t, tc.env, tc.files)
			out := preflightClaudeAuth(env)
			if out.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (%+v)", out.OK, tc.wantOK, out)
			}
			if !tc.wantOK && out.Class != tc.wantClass {
				t.Fatalf("Class = %q, want %q", out.Class, tc.wantClass)
			}
		})
	}
}

func TestPreflightCodexAuth(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		files  map[string][]byte
		wantOK bool
	}{
		{name: "openai api key sufficient", env: map[string]string{"OPENAI_API_KEY": "k"}, wantOK: true},
		{name: "synthetic api key sufficient", env: map[string]string{"SYNTHETIC_API_KEY": "k"}, wantOK: true},
		{name: "auth.json present and parses", files: map[string][]byte{"/home/worker/.codex/auth.json": []byte(`{"tokens":{}}`)}, wantOK: true},
		{name: "auth.json missing fails", wantOK: false},
		{name: "auth.json unparsable fails", files: map[string][]byte{"/home/worker/.codex/auth.json": []byte("{not json")}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := fakePreflightEnv(t, tc.env, tc.files)
			out := preflightCodexAuth(env)
			if out.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (%+v)", out.OK, tc.wantOK, out)
			}
			if !tc.wantOK && out.Class != executor.FailureClassPreflightAuth {
				t.Fatalf("Class = %q, want preflight_auth", out.Class)
			}
		})
	}
}

func TestPreflightAuthMaterialUnmodeledBackendPasses(t *testing.T) {
	env := fakePreflightEnv(t, nil, nil)
	if out := preflightAuthMaterial(env, "copilot"); !out.OK {
		t.Fatalf("unmodeled backend must pass rather than false-fail: %+v", out)
	}
}

func TestPreflightDiskHeadroom(t *testing.T) {
	t.Run("below floor fails", func(t *testing.T) {
		env := fakePreflightEnv(t, nil, nil)
		env.DiskFree = func(string) (int64, bool, error) { return 1 << 20, true, nil } // 1MiB
		out := preflightDiskHeadroom(env, PreflightConfig{DiskFloorBytes: 2 << 30}, "/ws")
		if out.OK || out.Class != executor.FailureClassPreflightEnv {
			t.Fatalf("out = %+v, want a preflight_env failure", out)
		}
	})
	t.Run("above floor passes", func(t *testing.T) {
		env := fakePreflightEnv(t, nil, nil)
		env.DiskFree = func(string) (int64, bool, error) { return 10 << 30, true, nil }
		out := preflightDiskHeadroom(env, PreflightConfig{DiskFloorBytes: 2 << 30}, "/ws")
		if !out.OK {
			t.Fatalf("out = %+v, want pass", out)
		}
	})
	t.Run("unknown reading passes rather than false-failing", func(t *testing.T) {
		env := fakePreflightEnv(t, nil, nil)
		env.DiskFree = func(string) (int64, bool, error) { return 0, false, errors.New("unsupported") }
		out := preflightDiskHeadroom(env, PreflightConfig{}, "/ws")
		if !out.OK {
			t.Fatalf("out = %+v, want pass on unknown disk reading", out)
		}
	})
	t.Run("zero/negative floor uses the documented default", func(t *testing.T) {
		env := fakePreflightEnv(t, nil, nil)
		env.DiskFree = func(string) (int64, bool, error) { return DefaultPreflightDiskFloorBytes - 1, true, nil }
		out := preflightDiskHeadroom(env, PreflightConfig{DiskFloorBytes: -1}, "/ws")
		if out.OK {
			t.Fatalf("out = %+v, want the default floor to still apply", out)
		}
	})
}

func TestPreflightRequiredTools(t *testing.T) {
	t.Run("missing git fails", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir()) // a PATH with nothing on it
		env := fakePreflightEnv(t, nil, nil)
		out := preflightRequiredTools(env, "claude")
		if out.OK || out.Class != executor.FailureClassPreflightEnv || !strings.Contains(out.Message, "git") {
			t.Fatalf("out = %+v, want a preflight_env failure naming git", out)
		}
	})
	t.Run("missing backend binary fails naming the backend", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir()) // git and claude both unresolvable
		env := fakePreflightEnv(t, nil, nil)
		out := preflightRequiredTools(env, "claude")
		if out.OK || !strings.Contains(out.Message, "git") {
			// git is checked first; this pins that ordering rather than a
			// specific message, since both tools are absent here.
			t.Fatalf("out = %+v, want git named first", out)
		}
	})
	t.Run("claude bin override exempts the PATH lookup", func(t *testing.T) {
		binDir := t.TempDir()
		binPath := binDir + "/claude-shim"
		if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		env := fakePreflightEnv(t, map[string]string{"KITSOKI_AGENT_CLAUDE_BIN": binPath}, nil)
		env.Stat = os.Stat // exercise a real Stat against the real temp file
		out := preflightRequiredTools(env, "claude")
		if !out.OK {
			t.Fatalf("out = %+v, want the override path to satisfy the check", out)
		}
	})
	t.Run("claude bin override pointing nowhere fails", func(t *testing.T) {
		env := fakePreflightEnv(t, map[string]string{"KITSOKI_AGENT_CLAUDE_BIN": "/does/not/exist"}, nil)
		out := preflightRequiredTools(env, "claude")
		if out.OK || out.Class != executor.FailureClassPreflightEnv || !strings.Contains(out.Message, "KITSOKI_AGENT_CLAUDE_BIN") {
			t.Fatalf("out = %+v, want a preflight_env failure naming the override", out)
		}
	})
	t.Run("codex uses its own override env name", func(t *testing.T) {
		env := fakePreflightEnv(t, map[string]string{"KITSOKI_AGENT_CODEX_BIN": "/does/not/exist"}, nil)
		out := preflightRequiredTools(env, "codex")
		if out.OK || !strings.Contains(out.Message, "KITSOKI_AGENT_CODEX_BIN") {
			t.Fatalf("out = %+v, want the codex-specific override named", out)
		}
	})
}

func TestPreflightModelEndpointCoherence(t *testing.T) {
	cases := []struct {
		name    string
		backend string
		baseURL string
		model   string
		wantOK  bool
	}{
		{name: "no base url passes", wantOK: true},
		{name: "anthropic base url passes without a model", baseURL: "https://api.anthropic.com", wantOK: true},
		{name: "anthropic subdomain passes without a model", baseURL: "https://eu.api.anthropic.com", wantOK: true},
		{name: "non-anthropic base url with explicit model passes", baseURL: "https://gateway.example/v1", model: "hf:zai-org/GLM-5.2", wantOK: true},
		{name: "non-anthropic base url with no model fails", baseURL: "https://gateway.example/v1", wantOK: false},
		{name: "non-claude backend is out of scope and passes", backend: "codex", baseURL: "https://gateway.example/v1", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := fakePreflightEnv(t, map[string]string{"ANTHROPIC_BASE_URL": tc.baseURL}, nil)
			out := preflightModelEndpointCoherence(env, tc.backend, tc.model)
			if out.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (%+v)", out.OK, tc.wantOK, out)
			}
			if !tc.wantOK && out.Class != executor.FailureClassPreflightEnv {
				t.Fatalf("Class = %q, want preflight_env", out.Class)
			}
		})
	}
}

func TestPreflightLiveAuthProbe(t *testing.T) {
	t.Run("disabled backend passes unconditionally", func(t *testing.T) {
		env := fakePreflightEnv(t, nil, nil)
		if out := preflightLiveAuthProbe(context.Background(), env, "codex"); !out.OK {
			t.Fatalf("codex must pass (out of scope): %+v", out)
		}
	})
	t.Run("no credential to probe passes", func(t *testing.T) {
		env := fakePreflightEnv(t, nil, nil)
		if out := preflightLiveAuthProbe(context.Background(), env, "claude"); !out.OK {
			t.Fatalf("no env credential must pass (nothing to probe): %+v", out)
		}
	})
	t.Run("200 passes", func(t *testing.T) {
		env := fakePreflightEnv(t, map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok"}, nil)
		env.Do = func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}
		if out := preflightLiveAuthProbe(context.Background(), env, "claude"); !out.OK {
			t.Fatalf("200 must pass: %+v", out)
		}
	})
	t.Run("429 rate limited still confirms a valid credential and passes", func(t *testing.T) {
		env := fakePreflightEnv(t, map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok"}, nil)
		env.Do = func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody}, nil
		}
		if out := preflightLiveAuthProbe(context.Background(), env, "claude"); !out.OK {
			t.Fatalf("429 must pass (credential is valid, just throttled): %+v", out)
		}
	})
	t.Run("401 fails", func(t *testing.T) {
		env := fakePreflightEnv(t, map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok"}, nil)
		env.Do = func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody}, nil
		}
		out := preflightLiveAuthProbe(context.Background(), env, "claude")
		if out.OK || out.Class != executor.FailureClassPreflightAuth {
			t.Fatalf("401 must fail preflight_auth: %+v", out)
		}
	})
	t.Run("network failure passes rather than false-failing", func(t *testing.T) {
		env := fakePreflightEnv(t, map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok"}, nil)
		env.Do = func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") }
		if out := preflightLiveAuthProbe(context.Background(), env, "claude"); !out.OK {
			t.Fatalf("a transient network failure must not fail preflight: %+v", out)
		}
	})
}

func TestRunPreflightDisabledLiveAuthProbeNeverCallsDo(t *testing.T) {
	// A working KITSOKI_AGENT_CLAUDE_BIN override so the required-tools
	// check passes deterministically regardless of whether the real
	// claude CLI happens to be installed on the machine running this test
	// — this test's only concern is that Do is never dialed when
	// LiveAuthProbe is off, not the tools check itself (see
	// TestPreflightRequiredTools for that).
	claudeShim := t.TempDir() + "/claude-shim"
	if err := os.WriteFile(claudeShim, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := fakePreflightEnv(t, map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok", "KITSOKI_AGENT_CLAUDE_BIN": claudeShim}, nil)
	env.Stat = os.Stat
	env.Do = func(*http.Request) (*http.Response, error) {
		t.Fatal("Do must not be called when LiveAuthProbe is off")
		return nil, nil
	}
	out := runPreflight(context.Background(), PreflightConfig{}, env, "claude", "", "/ws")
	if !out.OK {
		t.Fatalf("out = %+v, want pass", out)
	}
}

func TestClaudeCredentialsExpiry(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantHas    bool
		wantErr    bool
		wantExpiry time.Time
	}{
		{name: "nested claudeAiOauth expiresAt", raw: `{"claudeAiOauth":{"expiresAt":1735689600000}}`, wantHas: true, wantExpiry: time.UnixMilli(1735689600000).UTC()},
		{name: "bare expiresAt", raw: `{"expiresAt":1735689600000}`, wantHas: true, wantExpiry: time.UnixMilli(1735689600000).UTC()},
		{name: "bare expires_at rfc3339", raw: `{"expires_at":"2025-01-01T00:00:00Z"}`, wantHas: true, wantExpiry: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "no expiry recorded", raw: `{"claudeAiOauth":{"accessToken":"x"}}`, wantHas: false},
		{name: "malformed json", raw: `not json`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, has, err := claudeCredentialsExpiry([]byte(tc.raw))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if has != tc.wantHas {
				t.Fatalf("hasExpiry = %v, want %v", has, tc.wantHas)
			}
			if has && !got.Equal(tc.wantExpiry) {
				t.Fatalf("expiry = %v, want %v", got, tc.wantExpiry)
			}
		})
	}
}

func TestIsAnthropicBaseURL(t *testing.T) {
	cases := map[string]bool{
		"https://api.anthropic.com":     true,
		"https://eu.api.anthropic.com":  true,
		"https://gateway.example/v1":    false,
		"not a url \x7f":                false,
		"https://notanthropic.com.evil": false,
	}
	for raw, want := range cases {
		if got := isAnthropicBaseURL(raw); got != want {
			t.Errorf("isAnthropicBaseURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestClassifyRunnerFailurePrefersProviderClassThenDefaultsToStory(t *testing.T) {
	if got := classifyRunnerFailure(executor.Result{}); got != executor.FailureClassStory {
		t.Fatalf("no provider hint = %v, want story", got)
	}
	if got := classifyRunnerFailure(executor.Result{Provider: map[string]string{"failure_class": "agent_quota"}}); got != executor.FailureClassAgentQuota {
		t.Fatalf("recognized hint = %v, want agent_quota", got)
	}
	if got := classifyRunnerFailure(executor.Result{Provider: map[string]string{"failure_class": "not-a-real-class"}}); got != executor.FailureClassStory {
		t.Fatalf("unrecognized/forged hint must fall back to story, got %v", got)
	}
}
