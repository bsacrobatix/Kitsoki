package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
)

func TestCapsuleWorkerRunWritesExecutorResult(t *testing.T) {
	root := t.TempDir()
	environmentPath := filepath.Join(root, ".kitsoki", "environments", "ci.yaml")
	if err := os.MkdirAll(filepath.Dir(environmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentPath, []byte("schema: capsule-environment/v1\nid: ci\nnetwork: none\nsandbox: supervised\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	story := filepath.Join(root, "app.yaml")
	raw := `app:
  id: worker-ci
  version: 0.1.0
  title: Worker CI
  author: Test
  license: CC0
world:
  ci_job_id: { type: string, default: "" }
  ci_pipeline: { type: string, default: "" }
  ci_trigger: { type: object, default: {} }
  ci_source: { type: object, default: {} }
  ci_workspace: { type: object, default: {} }
  ci_environment: { type: object, default: {} }
  ci_policy: { type: object, default: {} }
  ci_verdict: { type: object, default: {} }
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
states:
  idle:
    view: [{ prose: "idle" }]
    on:
      run:
        - target: done
          effects:
            - set:
                ci_verdict:
                  schema: capsule-ci-verdict/v1
                  pipeline: change
                  outcome: passed
                  checks:
                    - id: worker
                      kind: deterministic
                      outcome: passed
                      evidence: ["artifact:worker"]
                  promotion_eligible: true
                  source_digest: "{{ world.ci_source.digest }}"
                  story_digest: "{{ world.ci_trigger.story_digest }}"
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
  done:
    view: [{ prose: "done" }]
`
	if err := os.WriteFile(story, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := (environment.Resolver{ProjectRoot: root}).Resolve(t.Context(), "ci")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryPath: "app.yaml", StoryDigest: "sha256:story", Environment: lock, Trigger: map[string]any{"requested_pipeline": "change"}, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	envelopePath := filepath.Join(root, "envelope.json")
	resultPath := filepath.Join(root, "result.json")
	encoded, _ := json.Marshal(envelope)
	if err := os.WriteFile(envelopePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	// --preflight-skip: this test exercises the executor-result-writing
	// contract for a no-agent-calls story, not job-start preflight (which
	// has its own dedicated test below) — the test environment has no real
	// backend credentials to satisfy the auth-material check.
	out, err := execRoot(t, "capsule", "worker", "run", "--envelope", envelopePath, "--result", resultPath, "--workspace", root, "--preflight-skip")
	if err != nil {
		t.Fatalf("worker run: %v\n%s", err, out)
	}
	var result struct {
		Result          executor.Result          `json:"result"`
		CompletionState executor.CompletionState `json:"completion_state"`
	}
	rawResult, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		t.Fatal(err)
	}
	if result.CompletionState.Outcome != "passed" || len(result.Result.VerdictJSON) == 0 {
		t.Fatalf("result %#v", result)
	}
}

func TestWorkerPassEnvCapabilitiesAndLogsExposeNamesNotValues(t *testing.T) {
	t.Setenv("SYNTHETIC_API_KEY", "super-secret-worker-token")
	t.Setenv("EMPTY_WORKER_SECRET", "")
	refs := capsuleWorkerPresentEnvRefs([]string{"SYNTHETIC_API_KEY", "EMPTY_WORKER_SECRET", "SYNTHETIC_API_KEY", "MISSING_SECRET"})
	if len(refs) != 1 || refs[0] != "SYNTHETIC_API_KEY" {
		t.Fatalf("environment refs %#v", refs)
	}
	redacted := redactWorkerEnvValues("failed with super-secret-worker-token", refs)
	if strings.Contains(redacted, "super-secret-worker-token") || !strings.Contains(redacted, "<redacted:SYNTHETIC_API_KEY>") {
		t.Fatalf("redacted log %q", redacted)
	}
}

func TestCapsuleWorkerAgentModelPrefersGenericOverride(t *testing.T) {
	t.Setenv("KITSOKI_WORKER_AGENT_MODEL", "gpt-5.6-terra")
	t.Setenv("KITSOKI_CLAUDE_MODEL", "hf:zai-org/GLM-5.2")
	if got := capsuleWorkerAgentModel("codex"); got != "gpt-5.6-terra" {
		t.Fatalf("generic worker model = %q, want gpt-5.6-terra", got)
	}
}

func TestCapsuleWorkerAgentModelUsesLegacyClaudeOverrideOnlyForClaude(t *testing.T) {
	t.Setenv("KITSOKI_WORKER_AGENT_MODEL", "")
	t.Setenv("KITSOKI_CLAUDE_MODEL", "hf:zai-org/GLM-5.2")
	if got := capsuleWorkerAgentModel("claude"); got != "hf:zai-org/GLM-5.2" {
		t.Fatalf("claude worker model = %q, want hf:zai-org/GLM-5.2", got)
	}
	if got := capsuleWorkerAgentModel("codex"); got != "" {
		t.Fatalf("codex inherited Claude-only model %q", got)
	}
}

func TestCapsuleWorkerProcessRunnerPassesSelectedModelAtProcessBoundary(t *testing.T) {
	t.Setenv(capsuleWorkerAgentModelEnv, "gpt-5.6-terra")
	t.Setenv("KITSOKI_UNRELATED_SECRET", "must-not-cross-worker-boundary")

	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	commandContext := func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=^TestCapsuleWorkerProcessBoundaryHelper$", "--"}, args...)
		return exec.CommandContext(ctx, os.Args[0], helperArgs...)
	}
	runner := capsuleWorkerProcessRunnerWithCommand("codex", nil, nil, commandContext)
	result, err := runner(t.Context(), root, executor.Prepared{
		ID:       "process-boundary",
		Envelope: executor.Envelope{JobID: "job-process-boundary"},
	}, filepath.Join(runDir, "trace.jsonl"))
	if err != nil {
		t.Fatalf("worker process runner: %v", err)
	}
	if got := result.Provider["completion_state"]; got != "passed" {
		t.Fatalf("completion state = %q, want passed", got)
	}
}

func TestCapsuleWorkerProcessBoundaryHelper(t *testing.T) {
	resultPath := workerHelperFlagValue(os.Args, "--result")
	if resultPath == "" {
		t.Skip("subprocess helper")
	}
	if got := os.Getenv(capsuleWorkerAgentModelEnv); got != "gpt-5.6-terra" {
		t.Fatalf("worker subprocess model = %q, want gpt-5.6-terra", got)
	}
	if _, ok := os.LookupEnv("KITSOKI_UNRELATED_SECRET"); ok {
		t.Fatal("worker subprocess received unrelated environment value")
	}
	raw, err := json.Marshal(struct {
		Result          executor.Result          `json:"result"`
		CompletionState executor.CompletionState `json:"completion_state"`
	}{
		Result:          executor.Result{ExecutionID: "process-boundary", ExitCode: 0},
		CompletionState: executor.CompletionState{Schema: executor.CompletionStateSchema, Outcome: "passed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func workerHelperFlagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

// TestCapsuleWorkerChildEnvAuthPrecedence pins the single source of truth
// for CLAUDE_CODE_OAUTH_TOKEN vs ANTHROPIC_AUTH_TOKEN precedence: when a
// controller-supplied per-job token is present, the image-baked default is
// deliberately withheld from the dispatched subprocess's environment rather
// than forwarding both and leaving the claude CLI's own undocumented
// internal precedence to decide.
func TestCapsuleWorkerChildEnvAuthPrecedence(t *testing.T) {
	t.Run("controller oauth token suppresses the baked anthropic auth token", func(t *testing.T) {
		t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "controller-token")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "baked-token")
		env := capsuleWorkerChildEnv([]string{"ANTHROPIC_AUTH_TOKEN"})
		if !containsEnvKV(env, "CLAUDE_CODE_OAUTH_TOKEN=controller-token") {
			t.Fatalf("child env must carry the controller token: %v", env)
		}
		if containsEnvPrefix(env, "ANTHROPIC_AUTH_TOKEN=") {
			t.Fatalf("child env must withhold the baked token when the controller token is present: %v", env)
		}
	})
	t.Run("baked anthropic auth token flows through absent a controller token", func(t *testing.T) {
		t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "baked-token")
		env := capsuleWorkerChildEnv([]string{"ANTHROPIC_AUTH_TOKEN"})
		if !containsEnvKV(env, "ANTHROPIC_AUTH_TOKEN=baked-token") {
			t.Fatalf("child env must carry the baked token absent a controller token: %v", env)
		}
	})
}

func containsEnvKV(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func TestCapsuleWorkerPreflightArgsRendersOnlyExplicitlySetKnobs(t *testing.T) {
	if got := capsuleWorkerPreflightArgs(false, 0, false); len(got) != 0 {
		t.Fatalf("all-default config must render no flags, got %v", got)
	}
	got := capsuleWorkerPreflightArgs(true, 5<<30, true)
	want := []string{"--preflight-skip", "--preflight-disk-floor-bytes", "5368709120", "--preflight-live-auth-probe"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

// TestCapsuleWorkerRunPreflightRejectsBeforeStoryLaunches proves the
// job-start preflight seam end-to-end: with every claude auth channel
// neutralized, `capsule worker run` must fail at preflight — before ever
// calling storylauncher.Launcher.Launch — with a typed terminal result
// naming preflight_auth, not a bare/opaque story failure.
func TestCapsuleWorkerRunPreflightRejectsBeforeStoryLaunches(t *testing.T) {
	root := t.TempDir()
	environmentPath := filepath.Join(root, ".kitsoki", "environments", "ci.yaml")
	if err := os.MkdirAll(filepath.Dir(environmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentPath, []byte("schema: capsule-environment/v1\nid: ci\nnetwork: none\nsandbox: supervised\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := (environment.Resolver{ProjectRoot: root}).Resolve(t.Context(), "ci")
	if err != nil {
		t.Fatal(err)
	}
	// A story path that does not exist on disk: proves the story is never
	// even loaded (app.Load, which would fail differently and much later
	// than preflight) when preflight rejects first.
	envelope, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryPath: "does-not-exist.yaml", StoryDigest: "sha256:story", Environment: lock, Trigger: map[string]any{"requested_pipeline": "change"}, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	envelopePath := filepath.Join(root, "envelope.json")
	resultPath := filepath.Join(root, "result.json")
	encoded, _ := json.Marshal(envelope)
	if err := os.WriteFile(envelopePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	// Neutralize every claude auth channel so preflight reliably rejects
	// regardless of what the machine actually running this test has
	// configured.
	for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", t.TempDir()) // no ~/.claude/.credentials.json here

	out, err := execRoot(t, "capsule", "worker", "run", "--envelope", envelopePath, "--result", resultPath, "--workspace", root)
	if err == nil {
		t.Fatalf("expected the preflight rejection to fail the command, out=%s", out)
	}
	rawResult, readErr := os.ReadFile(resultPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var result struct {
		Result          executor.Result          `json:"result"`
		CompletionState executor.CompletionState `json:"completion_state"`
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		t.Fatal(err)
	}
	if result.CompletionState.Outcome != "failed" || !strings.Contains(result.CompletionState.Reason, "preflight") {
		t.Fatalf("result %#v", result)
	}
	if result.Result.Provider["failure_class"] != "preflight_auth" {
		t.Fatalf("failure_class = %q, want preflight_auth", result.Result.Provider["failure_class"])
	}
}
