package workerserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kitsoki/internal/capsule/executor"
)

// DefaultPreflightDiskFloorBytes is the minimum free space the job-start
// preflight requires on the workspace's filesystem when PreflightConfig
// does not override it. 2GiB comfortably covers a story's git objects,
// agent scratch files, and trace/artifact output on a freshly-booted
// ephemeral worker without being so large that a normal, healthy worker
// trips it.
const DefaultPreflightDiskFloorBytes int64 = 2 << 30

// PreflightConfig controls the job-start readiness checks RunPreflight
// performs once the source workspace is materialized and before the story
// (and therefore any coding-agent subprocess) launches. Every check is
// static and zero-cost by default — no network calls, no paid API round
// trips — so preflight stays fast (well under a second) even with
// LiveAuthProbe off. All fields are optional; the zero value runs every
// check with the default disk floor and no live probe.
type PreflightConfig struct {
	// Skip disables every preflight check. An escape hatch for exotic
	// worker setups this preflight does not yet understand — defaults to
	// false (preflight runs).
	Skip bool
	// DiskFloorBytes is the minimum free space required on the
	// workspace's filesystem. <=0 uses DefaultPreflightDiskFloorBytes.
	DiskFloorBytes int64
	// LiveAuthProbe, when true, spends one minimal real request against
	// the selected backend's API to confirm the credential is actually
	// accepted, not just present/well-formed. Off by default — preflight
	// must never spend money unless an operator explicitly opts in.
	// Currently implemented for the claude backend only; other backends
	// pass this check unconditionally (see preflightLiveAuthProbe).
	LiveAuthProbe bool
}

// PreflightOutcome is the result of one job-start preflight run: either OK,
// or a typed terminal failure naming which class of check tripped and a
// human-readable reason.
type PreflightOutcome struct {
	OK      bool
	Class   executor.FailureClass
	Message string
}

func preflightPass() PreflightOutcome { return PreflightOutcome{OK: true} }

func preflightFail(class executor.FailureClass, format string, args ...any) PreflightOutcome {
	return PreflightOutcome{Class: class, Message: fmt.Sprintf(format, args...)}
}

// preflightEnv is the narrow environment/filesystem/network surface every
// check reads through, so tests exercise the real check logic against fake
// env/files/responses instead of the live process environment, disk, and
// network. RunPreflight wires defaultPreflightEnv (the real os/exec/http
// surface); tests construct a preflightEnv literal with fakes.
type preflightEnv struct {
	Getenv      func(string) string
	Stat        func(string) (os.FileInfo, error)
	ReadFile    func(string) ([]byte, error)
	UserHomeDir func() (string, error)
	DiskFree    func(path string) (freeBytes int64, known bool, err error)
	Now         func() time.Time
	// Do sends a live-auth-probe HTTP request. Only consulted when
	// PreflightConfig.LiveAuthProbe is true.
	Do func(*http.Request) (*http.Response, error)
}

func defaultPreflightEnv() preflightEnv {
	return preflightEnv{
		Getenv:      os.Getenv,
		Stat:        os.Stat,
		ReadFile:    os.ReadFile,
		UserHomeDir: os.UserHomeDir,
		DiskFree:    preflightDiskFree,
		Now:         func() time.Time { return time.Now().UTC() },
		Do:          http.DefaultClient.Do,
	}
}

// RunPreflight runs every job-start readiness check for the selected agent
// backend against workspace, in order, stopping at the first failure. It is
// the seam cmd/kitsoki's `capsule worker run` calls once the source
// workspace is materialized and before the story (and any coding-agent
// subprocess) launches: on failure, the caller must not launch the story,
// so no story turn is ever burned on a job that could not have succeeded.
func RunPreflight(ctx context.Context, cfg PreflightConfig, backend, model, workspace string) PreflightOutcome {
	return runPreflight(ctx, cfg, defaultPreflightEnv(), backend, model, workspace)
}

func runPreflight(ctx context.Context, cfg PreflightConfig, env preflightEnv, backend, model, workspace string) PreflightOutcome {
	if cfg.Skip {
		return preflightPass()
	}
	if out := preflightAuthMaterial(env, backend); !out.OK {
		return out
	}
	if out := preflightDiskHeadroom(env, cfg, workspace); !out.OK {
		return out
	}
	if out := preflightRequiredTools(env, backend); !out.OK {
		return out
	}
	if out := preflightModelEndpointCoherence(env, backend, model); !out.OK {
		return out
	}
	if cfg.LiveAuthProbe {
		if out := preflightLiveAuthProbe(ctx, env, backend); !out.OK {
			return out
		}
	}
	return preflightPass()
}

// normalizeBackend maps the empty/default backend selector to "claude",
// matching capsuleWorkerAgentModel and storylauncher.Launcher's own default.
func normalizeBackend(backend string) string {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" {
		return "claude"
	}
	return backend
}

// preflightAuthMaterial checks (a): the selected backend's auth material is
// present and minimally valid, without ever making a paid call.
func preflightAuthMaterial(env preflightEnv, backend string) PreflightOutcome {
	switch normalizeBackend(backend) {
	case "claude":
		return preflightClaudeAuth(env)
	case "codex":
		return preflightCodexAuth(env)
	default:
		// Other backends (copilot, agy, ...) have no auth-material contract
		// modeled here yet; pass rather than false-fail an unmodeled
		// backend. Extend this switch when a new backend needs a check.
		return preflightPass()
	}
}

// preflightClaudeAuth implements the exact contract given in the task: a
// controller-supplied CLAUDE_CODE_OAUTH_TOKEN (see the precedence enforced
// in cmd/kitsoki capsuleWorkerChildEnv) or ANTHROPIC_API_KEY/AUTH_TOKEN is
// sufficient on its own; when ANTHROPIC_BASE_URL is set (a non-default
// gateway), ANTHROPIC_AUTH_TOKEN must be non-empty; otherwise the baked
// credentials file must exist, parse, and — if it records an expiry — not
// be expired.
func preflightClaudeAuth(env preflightEnv) PreflightOutcome {
	if strings.TrimSpace(env.Getenv("CLAUDE_CODE_OAUTH_TOKEN")) != "" {
		return preflightPass()
	}
	baseURL := strings.TrimSpace(env.Getenv("ANTHROPIC_BASE_URL"))
	if baseURL != "" {
		if strings.TrimSpace(env.Getenv("ANTHROPIC_AUTH_TOKEN")) == "" {
			return preflightFail(executor.FailureClassPreflightAuth,
				"claude backend selected: ANTHROPIC_BASE_URL=%s is set but ANTHROPIC_AUTH_TOKEN is empty — the selected gateway endpoint requires a bearer token", baseURL)
		}
		return preflightPass()
	}
	if strings.TrimSpace(env.Getenv("ANTHROPIC_API_KEY")) != "" || strings.TrimSpace(env.Getenv("ANTHROPIC_AUTH_TOKEN")) != "" {
		return preflightPass()
	}
	home, err := env.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return preflightFail(executor.FailureClassPreflightAuth,
			"claude backend selected: no ANTHROPIC_AUTH_TOKEN/ANTHROPIC_API_KEY/CLAUDE_CODE_OAUTH_TOKEN and unable to resolve $HOME to check claude credentials: %v", err)
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	if _, statErr := env.Stat(path); statErr != nil {
		return preflightFail(executor.FailureClassPreflightAuth,
			"claude backend selected: no auth material found — ANTHROPIC_AUTH_TOKEN/ANTHROPIC_API_KEY/CLAUDE_CODE_OAUTH_TOKEN are unset and %s is missing: %v", path, statErr)
	}
	raw, readErr := env.ReadFile(path)
	if readErr != nil {
		return preflightFail(executor.FailureClassPreflightAuth, "claude backend selected: credentials %s could not be read: %v", path, readErr)
	}
	expiresAt, hasExpiry, parseErr := claudeCredentialsExpiry(raw)
	if parseErr != nil {
		return preflightFail(executor.FailureClassPreflightAuth, "claude backend selected: credentials %s did not parse as JSON: %v", path, parseErr)
	}
	if hasExpiry && !expiresAt.IsZero() && env.Now().After(expiresAt) {
		return preflightFail(executor.FailureClassPreflightAuth, "claude backend selected: credentials %s expired at %s", path, expiresAt.UTC().Format(time.RFC3339))
	}
	return preflightPass()
}

// claudeCredentialsExpiry extracts an expiry timestamp from a claude CLI
// `.credentials.json` document, if one is recorded. It tolerates the known
// claude-code-cli shape ({"claudeAiOauth": {"expiresAt": <unix-ms>}}), a
// bare top-level "expiresAt" (unix milliseconds), and a bare top-level
// "expires_at" (RFC3339 string) — recording no expiry (hasExpiry=false) for
// any document that parses but names none of those fields, which is not a
// failure: not every credential shape records an expiry.
func claudeCredentialsExpiry(raw []byte) (expiresAt time.Time, hasExpiry bool, err error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return time.Time{}, false, err
	}
	if nested, ok := doc["claudeAiOauth"].(map[string]any); ok {
		if t, ok := millisEpochField(nested, "expiresAt"); ok {
			return t, true, nil
		}
	}
	if t, ok := millisEpochField(doc, "expiresAt"); ok {
		return t, true, nil
	}
	if t, ok := rfc3339Field(doc, "expires_at"); ok {
		return t, true, nil
	}
	return time.Time{}, false, nil
}

func millisEpochField(doc map[string]any, key string) (time.Time, bool) {
	v, ok := doc[key]
	if !ok {
		return time.Time{}, false
	}
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(f)).UTC(), true
}

func rfc3339Field(doc map[string]any, key string) (time.Time, bool) {
	v, ok := doc[key].(string)
	if !ok || strings.TrimSpace(v) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// preflightCodexAuth requires either an OPENAI_API_KEY/SYNTHETIC_API_KEY or
// a parseable ~/.codex/auth.json, matching the same two channels
// cmd/kitsoki/vmpool.go's baked-image verifier already checks for.
func preflightCodexAuth(env preflightEnv) PreflightOutcome {
	if strings.TrimSpace(env.Getenv("OPENAI_API_KEY")) != "" || strings.TrimSpace(env.Getenv("SYNTHETIC_API_KEY")) != "" {
		return preflightPass()
	}
	home, err := env.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return preflightFail(executor.FailureClassPreflightAuth,
			"codex backend selected: no OPENAI_API_KEY/SYNTHETIC_API_KEY and unable to resolve $HOME to check codex credentials: %v", err)
	}
	path := filepath.Join(home, ".codex", "auth.json")
	raw, readErr := env.ReadFile(path)
	if readErr != nil {
		return preflightFail(executor.FailureClassPreflightAuth,
			"codex backend selected: no auth material found — OPENAI_API_KEY/SYNTHETIC_API_KEY are unset and %s could not be read: %v", path, readErr)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return preflightFail(executor.FailureClassPreflightAuth, "codex backend selected: credentials %s did not parse as JSON: %v", path, err)
	}
	return preflightPass()
}

// preflightDiskHeadroom checks (b): free space on the workspace's
// filesystem is above the configured floor. An unknown disk reading
// (unsupported platform, stat failure) is not itself a preflight failure —
// only a KNOWN low-disk reading fails closed, so a worker running somewhere
// this check cannot answer still gets to try the job.
func preflightDiskHeadroom(env preflightEnv, cfg PreflightConfig, workspace string) PreflightOutcome {
	floor := cfg.DiskFloorBytes
	if floor <= 0 {
		floor = DefaultPreflightDiskFloorBytes
	}
	path := strings.TrimSpace(workspace)
	if path == "" {
		path = "."
	}
	free, known, err := env.DiskFree(path)
	if err != nil || !known {
		return preflightPass()
	}
	if free < floor {
		return preflightFail(executor.FailureClassPreflightEnv,
			"disk headroom %d bytes on %s is below the configured floor %d bytes", free, path, floor)
	}
	return preflightPass()
}

// Bin-override env vars for the coding-agent CLIs, matching
// internal/host's AgentBinEnv / CodexBinEnv (kept as a local literal rather
// than importing internal/host: internal/host transitively imports this
// package's own capsule/ci -> capsule/workerserver dependency chain, so a
// workerserver -> host import edge is a cycle. See
// TestPreflightRequiredToolsHonorsBinOverrideEnv for the pinned contract).
const (
	preflightClaudeBinEnv = "KITSOKI_AGENT_CLAUDE_BIN"
	preflightCodexBinEnv  = "KITSOKI_AGENT_CODEX_BIN"
)

// preflightRequiredTools checks (c): git and the selected backend's CLI
// resolve to a real binary — either the backend's bin-override env var
// (honoring the exact override name internal/host's agent dispatch uses,
// so an operator's baked non-PATH binary placement is recognized
// identically here) or a PATH lookup. A genuinely missing binary fails
// preflight with the same cause the story would have failed with anyway,
// just before any turn burns.
func preflightRequiredTools(env preflightEnv, backend string) PreflightOutcome {
	if _, err := exec.LookPath("git"); err != nil {
		return preflightFail(executor.FailureClassPreflightEnv, "required tool \"git\" is not on PATH: %v", err)
	}
	name, overrideEnv := "claude", preflightClaudeBinEnv
	if normalizeBackend(backend) == "codex" {
		name, overrideEnv = "codex", preflightCodexBinEnv
	}
	if bin := strings.TrimSpace(env.Getenv(overrideEnv)); bin != "" {
		if _, err := env.Stat(bin); err != nil {
			return preflightFail(executor.FailureClassPreflightEnv, "required tool %q override %s=%s does not exist: %v", name, overrideEnv, bin, err)
		}
		return preflightPass()
	}
	if _, err := exec.LookPath(name); err != nil {
		return preflightFail(executor.FailureClassPreflightEnv, "required tool %q is not on PATH: %v", name, err)
	}
	return preflightPass()
}

// preflightModelEndpointCoherence checks (d): when ANTHROPIC_BASE_URL points
// at a non-Anthropic gateway and no explicit model has been selected for
// the worker, the story would otherwise reach the deep, opaque "hf: prefix"
// class HTTP 400 the audit identified — preflight fails fast here instead
// with a message that names the actual cause.
func preflightModelEndpointCoherence(env preflightEnv, backend, model string) PreflightOutcome {
	if normalizeBackend(backend) != "claude" {
		// The worker-selected-model channel (KITSOKI_WORKER_AGENT_MODEL /
		// KITSOKI_CLAUDE_MODEL -> storylauncher.Launcher.AgentModel) only
		// exists for the claude backend today.
		return preflightPass()
	}
	baseURL := strings.TrimSpace(env.Getenv("ANTHROPIC_BASE_URL"))
	if baseURL == "" || isAnthropicBaseURL(baseURL) {
		return preflightPass()
	}
	if strings.TrimSpace(model) != "" {
		return preflightPass()
	}
	return preflightFail(executor.FailureClassPreflightEnv,
		"ANTHROPIC_BASE_URL=%s points at a non-Anthropic gateway but no explicit model is selected (set KITSOKI_WORKER_AGENT_MODEL or KITSOKI_CLAUDE_MODEL); dispatching with an unset model against a non-Anthropic endpoint fails deep in the story with an opaque HTTP 400", baseURL)
}

func isAnthropicBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	hostname := strings.ToLower(u.Hostname())
	return hostname == "api.anthropic.com" || strings.HasSuffix(hostname, ".anthropic.com")
}

// preflightLiveAuthProbe spends one minimal (max_tokens: 1) real request
// against the Anthropic Messages API to confirm the resolved credential is
// actually accepted, not merely present/well-formed. Scoped to the claude
// backend only (codex's OAuth-based ~/.codex/auth.json has no equivalent
// well-known minimal-cost probe modeled here yet) — other backends pass
// unconditionally. A 429 (rate limited) still confirms the credential is
// valid, so it passes; only an authentication-shaped rejection (401/403)
// fails preflight. Any other transport/status outcome (network error, 5xx)
// passes rather than false-failing preflight on a transient provider issue
// unrelated to the credential itself — LiveAuthProbe never blocks a job
// dispatch because the provider had a bad moment.
func preflightLiveAuthProbe(ctx context.Context, env preflightEnv, backend string) PreflightOutcome {
	if normalizeBackend(backend) != "claude" {
		return preflightPass()
	}
	token := strings.TrimSpace(env.Getenv("CLAUDE_CODE_OAUTH_TOKEN"))
	authHeader, authValue := "Authorization", "Bearer "+token
	if token == "" {
		token = strings.TrimSpace(env.Getenv("ANTHROPIC_AUTH_TOKEN"))
		authHeader, authValue = "Authorization", "Bearer "+token
	}
	if token == "" {
		token = strings.TrimSpace(env.Getenv("ANTHROPIC_API_KEY"))
		authHeader, authValue = "x-api-key", token
	}
	if token == "" {
		// No env-var credential to probe (baked-credentials-file case);
		// the static auth-material check above already validated that
		// path, and a live probe against a CLI-managed OAuth session file
		// is out of scope here.
		return preflightPass()
	}
	base := strings.TrimSpace(env.Getenv("ANTHROPIC_BASE_URL"))
	if base == "" {
		base = "https://api.anthropic.com"
	}
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-3-5-haiku-20241022",
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", strings.NewReader(string(body)))
	if err != nil {
		return preflightPass()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set(authHeader, authValue)
	resp, err := env.Do(req)
	if err != nil {
		// Network failure probing the provider is not itself proof the
		// credential is bad; let the job proceed rather than fail preflight
		// on a transient connectivity blip LiveAuthProbe was never meant to
		// gate on.
		return preflightPass()
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return preflightFail(executor.FailureClassPreflightAuth, "live auth probe against %s rejected the resolved credential: HTTP %d", base, resp.StatusCode)
	}
	return preflightPass()
}
