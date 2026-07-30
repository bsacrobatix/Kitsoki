package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests in this file deliberately drive the REAL ShellGate with Runner nil,
// so every case executes a real `sh -c` child and asserts against the real
// process exit status. A test double that returned a canned error would prove
// nothing here: the whole defect being pinned is that a shell gate's exit
// status was thrown away, so the exit status is the thing under test. In
// particular sgcMissingTool relies on a command that genuinely does not exist
// on PATH, which is the exact shape of the production failure (a worker whose
// PATH omitted the Go toolchain, so a suite hit `go: command not found`).
//
// No network, no LLM, no cost: every command is a shell builtin or a
// guaranteed-absent binary name.

// sgcMissingTool is a command name that must not resolve on any PATH.
const sgcMissingTool = "kitsoki-queue-gate-tool-that-must-not-exist"

func sgcGitWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Queue Test")
	git(t, root, "config", "user.email", "queue@example.invalid")
	commit(t, root, "base.txt", "base\n", "base")
	return root
}

// TestSgcShellGateClassifiesRealProcessExitStatus is the unit-level contract:
// which real exit statuses a shell gate's failure is read as environmental,
// and which stay a product (red-gate) failure that consumes the attempt
// budget.
func TestSgcShellGateClassifiesRealProcessExitStatus(t *testing.T) {
	// A file that exists but carries no execute bit, kept outside the
	// speculative workspace so running it cannot dirty the tree.
	noExec := filepath.Join(t.TempDir(), "not-executable.sh")
	if err := os.WriteFile(noExec, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		command   string
		wantEnv   bool
		wantCause string
		why       string
	}{
		{
			name:      "reserved env exit code",
			command:   fmt.Sprintf("exit %d", GateEnvExitCode),
			wantEnv:   true,
			wantCause: "gate_declared_environment_unusable",
			why:       "the gate explicitly declared its environment unusable",
		},
		{
			name:      "command not found",
			command:   sgcMissingTool,
			wantEnv:   true,
			wantCause: "command_not_found",
			why:       "exit 127: the shell never reached a program, so this is not a verdict on the tree",
		},
		{
			name:      "found but not executable",
			command:   noExec,
			wantEnv:   true,
			wantCause: "command_not_executable",
			why:       "exit 126: the shell never reached a program",
		},
		{
			name:      "gate process killed by a signal",
			command:   "kill -9 $$",
			wantEnv:   true,
			wantCause: "killed_signal_9",
			why:       "an OOM/resource kill of the gate itself says nothing about the tree",
		},
		{
			name: "child killed, shell reports 128+N",
			// `|| exit $?` stops sh from exec-replacing itself with the child, so
			// the shell survives to report the kill as its own 128+9 status —
			// the other half of the same class.
			command:   "sh -c 'kill -9 $$' || exit $?",
			wantEnv:   true,
			wantCause: "killed_signal_9",
			why:       "a shell-reported signal death is the same class as a direct one",
		},
		{
			name:    "red suite exits 1",
			command: "exit 1",
			wantEnv: false,
			why:     "a suite that ran and reported red is a product failure and must burn an attempt",
		},
		{
			name:    "red suite exits 2",
			command: "exit 2",
			wantEnv: false,
			why:     "any ordinary nonzero status stays a product failure",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := sgcGitWorkspace(t)
			result, err := ShellGate{Command: tc.command}.Run(context.Background(), Speculation{WorkspacePath: root})
			if err == nil {
				t.Fatalf("expected the gate to fail for %q", tc.command)
			}
			// The safety invariant that holds in every branch: classification
			// changes only the retry policy, never the verdict. A failing gate
			// is never reported as passed, so no classification can land red
			// code.
			if result.Passed {
				t.Fatalf("a failing gate must never report Passed=true (%s)", tc.name)
			}

			var envErr EnvError
			gotEnv := errors.As(err, &envErr)
			if gotEnv != tc.wantEnv {
				t.Fatalf("classified environmental=%v, want %v (%s)\nerr=%v", gotEnv, tc.wantEnv, tc.why, err)
			}
			if tc.wantEnv {
				// Every environmental gate failure must be Immediate: none of
				// these conditions clears by waiting, and the operator's
				// requirement is to fail fast and loud rather than retry.
				if !envErr.Immediate {
					t.Fatalf("%s must be an immediate environmental failure, not a slow retry", tc.name)
				}
				if envErr.Cause != tc.wantCause {
					t.Fatalf("cause=%q want %q — the cause is what makes queue status readable", envErr.Cause, tc.wantCause)
				}
			}

			// A product failure must not be mistaken for a harness failure
			// either, which would park the candidate immediately.
			var harness HarnessError
			if errors.As(err, &harness) {
				t.Fatalf("a shell gate failure must never be a harness failure: %v", err)
			}

			// The class is recorded in evidence so an operator can see from
			// `queue status` why a candidate took the path it took.
			wantClass := "class=product"
			if tc.wantEnv {
				wantClass = "class=environment cause=" + tc.wantCause
			}
			var found string
			for _, e := range result.Evidence {
				if strings.HasPrefix(e, "queue:gate:exit=") {
					found = e
				}
			}
			if found == "" {
				t.Fatalf("gate evidence must record the exit status and class, got %q", result.Evidence)
			}
			if !strings.Contains(found, wantClass) {
				t.Fatalf("evidence %q does not record %q", found, wantClass)
			}
		})
	}
}

// TestSgcNonVerdictGateFailsFastAndLoud is the end-to-end proof of the operator
// requirement. Two things must hold on the FIRST pass, not the fifth:
//
//   - the candidate is parked immediately, so nobody waits through a retry
//     window rediscovering a broken host ("if it goes past 1 attempt it's a
//     problem");
//   - the park reason names the environmental cause, so `queue status`
//     distinguishes a killed or starved gate from a red one, and
//     first_env_failure_at is populated instead of sitting at the zero value.
//
// Both the missing-toolchain failure (2026-07-29, candidate queue-ed0fe26f0af0)
// and the killed-gate failure (2026-07-30, candidate queue-eb33d07e4f94) are
// covered, because both are "the gate produced no verdict".
func TestSgcNonVerdictGateFailsFastAndLoud(t *testing.T) {
	cases := []struct {
		name       string
		command    string
		wantReason string
	}{
		{
			name:       "missing toolchain",
			command:    sgcMissingTool + " --run-the-suite",
			wantReason: "gate_no_verdict_command_not_found",
		},
		{
			name:       "gate declared its environment unusable",
			command:    fmt.Sprintf("exit %d", GateEnvExitCode),
			wantReason: "gate_no_verdict_gate_declared_environment_unusable",
		},
		{
			name:       "gate killed mid-suite",
			command:    "sh -c 'kill -9 $$' || exit $?",
			wantReason: "gate_no_verdict_killed_signal_9",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace := sgcGitWorkspace(t)
			store := Store{ProjectRoot: t.TempDir()}
			candidate := wltSubmit(t, store, "no-verdict")
			clock := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)

			integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
				return Speculation{SHA: "spec-" + c.SHA, WorkspacePath: workspace}, nil
			}}
			worker := Worker{Store: store, Deps: ProcessDeps{
				Integration:    integration,
				Gate:           ShellGate{Command: tc.command},
				EnvRetryDelay:  30 * time.Second,
				MaxEnvDuration: time.Hour,
				MaxAttempts:    5,
				Now:            func() time.Time { return clock },
			}}

			progressed, err := worker.RunOnce(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !progressed {
				t.Fatal("expected the worker to make progress")
			}
			got := mustGet(t, store, candidate.ID)

			// Fail fast: parked on the first pass, not queued for another try.
			if got.phase() != NeedsInput {
				t.Fatalf("phase=%s, want needs_input on the FIRST pass — a verdict-less gate must not be slow-retried", got.phase())
			}
			if !got.RetryAt.IsZero() {
				t.Fatalf("retry_at=%v, want zero — a parked candidate must not carry a retry timer", got.RetryAt)
			}
			// Fail loud: the reason names the cause.
			if got.RetryReason != tc.wantReason {
				t.Fatalf("retry_reason=%q, want %q — a killed/starved gate must be distinguishable from a red one in queue status", got.RetryReason, tc.wantReason)
			}
			// The product attempt budget was never spent: the gate never judged
			// this candidate's code.
			if got.Attempt != 0 {
				t.Fatalf("attempt=%d, want 0 — a verdict-less gate must not consume the product attempt budget", got.Attempt)
			}
			if got.FirstEnvFailureAt.IsZero() {
				t.Fatalf("first_env_failure_at is still the zero value; the environmental class never fired")
			}
			if got.EnvRetries != 1 {
				t.Fatalf("env_retries=%d, want 1", got.EnvRetries)
			}
		})
	}
}

// TestSgcRedGateStillBurnsTheAttemptBudget is the paired control. Without it
// the fix above could be satisfied by classifying everything environmental,
// which would let a genuinely red candidate retry for the whole wall-clock
// window instead of parking. A suite that runs and reports red must still be
// treated as a product failure.
func TestSgcRedGateStillBurnsTheAttemptBudget(t *testing.T) {
	workspace := sgcGitWorkspace(t)
	store := Store{ProjectRoot: t.TempDir()}
	candidate := wltSubmit(t, store, "red-suite")
	clock := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)

	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		return Speculation{SHA: "spec-" + c.SHA, WorkspacePath: workspace}, nil
	}}
	worker := Worker{Store: store, Deps: ProcessDeps{
		Integration:    integration,
		Gate:           ShellGate{Command: "echo 3 of 42 checks failed >&2; exit 1"},
		EnvRetryDelay:  30 * time.Second,
		MaxEnvDuration: time.Hour,
		Now:            func() time.Time { return clock },
	}}

	progressed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !progressed {
		t.Fatal("expected the worker to make progress")
	}
	got := mustGet(t, store, candidate.ID)
	if got.Attempt != 1 {
		t.Fatalf("attempt=%d, want 1 — a red suite must consume the product attempt budget", got.Attempt)
	}
	if got.EnvRetries != 0 || !got.FirstEnvFailureAt.IsZero() {
		t.Fatalf("a red suite must not be recorded as an environmental failure: env_retries=%d first_env_failure_at=%v", got.EnvRetries, got.FirstEnvFailureAt)
	}
	if got.RetryReason != "gate_failed" {
		t.Fatalf("retry_reason=%q", got.RetryReason)
	}
}
