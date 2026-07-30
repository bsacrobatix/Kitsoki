package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCapsuleCIProjectChecksBuildsTypedVerdictAndEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".kitsoki"), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := "schema: project-profile/v1\nid: example\ncommands:\n  test: go test ./...\n  build: go build ./...\n"
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := CapsuleCICommandRunnerFunc(func(_ context.Context, _ string, command string) (string, int, error) {
		if command == "go build ./..." {
			return "compile failed\n", 1, nil
		}
		return "ok\n", 0, nil
	})
	result, err := NewCapsuleCIProjectChecksHandler(runner)(context.Background(), map[string]any{"workdir": root, "job_id": "job/unsafe", "pipeline": "change", "source_digest": "source", "story_digest": "story", "environment_digest": "environment", "envelope_digest": "envelope"})
	if err != nil || result.Error != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	verdict := result.Data["verdict"].(map[string]any)
	if verdict["outcome"] != "failed" || verdict["promotion_eligible"] != false {
		t.Fatalf("verdict = %#v", verdict)
	}
	evidence := filepath.Join(root, ".artifacts", "capsule-ci", "checks", "jobunsafe.json")
	raw, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var artifact map[string]any
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact["outcome"] != "failed" {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func TestCapsuleCIProjectChecksRoutesNamedPipelineGate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".kitsoki"), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := "schema: project-profile/v1\nid: example\ncommands:\n  test: legacy test\n  build: legacy build\n  change: quick gate\n  full: full gate\n  release: release gate\n"
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ pipeline, command string }{
		{"change", "quick gate"},
		{"full", "full gate"},
		{"release", "release gate"},
	} {
		pipeline, wantCommand := test.pipeline, test.command
		t.Run(pipeline, func(t *testing.T) {
			var commands []string
			result, err := NewCapsuleCIProjectChecksHandler(CapsuleCICommandRunnerFunc(func(_ context.Context, _ string, command string) (string, int, error) {
				commands = append(commands, command)
				return "ok", 0, nil
			}))(context.Background(), map[string]any{"workdir": root, "job_id": pipeline, "pipeline": pipeline})
			if err != nil || result.Error != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if len(commands) != 1 || commands[0] != wantCommand {
				t.Fatalf("pipeline %s commands=%v", pipeline, commands)
			}
			checks := result.Data["checks"].([]map[string]any)
			if len(checks) != 1 || checks[0]["id"] != pipeline {
				t.Fatalf("checks=%#v", checks)
			}
		})
	}
}

func TestCapsuleCIProjectChecksMissingFullOrReleaseGateFailsClosed(t *testing.T) {
	root := capsuleCIProfileRoot(t)
	for _, pipeline := range []string{"full", "release"} {
		result, err := NewCapsuleCIProjectChecksHandler(CapsuleCICommandRunnerFunc(func(context.Context, string, string) (string, int, error) {
			t.Fatalf("%s unexpectedly ran a legacy command", pipeline)
			return "", 0, nil
		}))(context.Background(), map[string]any{"workdir": root, "job_id": "missing-" + pipeline, "pipeline": pipeline})
		if err != nil || result.Error != "" {
			t.Fatalf("%s result=%+v err=%v", pipeline, result, err)
		}
		verdict := result.Data["verdict"].(map[string]any)
		if verdict["outcome"] != "needs_input" || verdict["promotion_eligible"] != false {
			t.Fatalf("%s verdict=%#v", pipeline, verdict)
		}
	}
}

func TestCapsuleCIProjectChecksUsesTrustedRetainedEvidenceDestination(t *testing.T) {
	root := capsuleCIProfileRoot(t)
	retained := filepath.Join(t.TempDir(), "ci-evidence")
	longLog := "tail-marker:" + string(make([]byte, 1<<20+32))
	result, err := NewCapsuleCIProjectChecksHandlerWithEvidenceDestination(CapsuleCICommandRunnerFunc(func(_ context.Context, _ string, command string) (string, int, error) {
		if command == "go test ./..." {
			return longLog, 9, nil
		}
		return "build ok", 0, nil
	}), CapsuleCIEvidenceDestination{
		Root:            retained,
		ReferencePrefix: "file:.capsules/ci/evidence",
	})(context.Background(), map[string]any{"workdir": root, "job_id": "retained", "pipeline": "change"})
	if err != nil || result.Error != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := result.Data["evidence"]; got != "file:.capsules/ci/evidence/retained.json" {
		t.Fatalf("evidence ref = %#v", got)
	}
	raw, err := os.ReadFile(filepath.Join(retained, "retained.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact map[string]any
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	entry := artifact["checks"].([]any)[0].(map[string]any)
	log := entry["log"].(string)
	if len(log) != 1<<20 || log[len(log)-32:] != longLog[len(longLog)-32:] {
		t.Fatalf("bounded retained log length/tail = %d/%q", len(log), log[len(log)-32:])
	}
}

func TestCapsuleCIProjectChecksDefaultHostCommandHasNoDeadline(t *testing.T) {
	root := capsuleCIProfileRoot(t)
	called := 0
	result, err := NewCapsuleCIProjectChecksHandler(CapsuleCICommandRunnerFunc(func(ctx context.Context, _ string, _ string) (string, int, error) {
		called++
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("trusted local host command unexpectedly received a deadline")
		}
		return "ok\n", 0, nil
	}))(context.Background(), map[string]any{"workdir": root, "pipeline": "change"})
	if err != nil || result.Error != "" || called != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, called, err)
	}
}

func TestCapsuleCIProjectChecksSealsDeclaredTimeoutAndClassifiesIt(t *testing.T) {
	root := capsuleCIProfileRoot(t)
	result, err := NewCapsuleCIProjectChecksHandler(CapsuleCICommandRunnerFunc(func(ctx context.Context, _ string, _ string) (string, int, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatalf("command deadline = %v, %v", deadline, ok)
		}
		return "timed out\n", -1, context.DeadlineExceeded
	}))(context.Background(), map[string]any{"workdir": root, "job_id": "timeout", "pipeline": "change", "command_timeout": "50ms"})
	if err != nil || result.Error != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	artifact := readCapsuleCIArtifact(t, root, "timeout")
	checks := artifact["checks"].([]any)
	entry := checks[0].(map[string]any)
	if entry["error_kind"] != "timeout" || entry["error"] != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout evidence = %#v", entry)
	}
}

func TestCapsuleCIProjectChecksPreservesCallerCancellation(t *testing.T) {
	root := capsuleCIProfileRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewCapsuleCIProjectChecksHandler(CapsuleCICommandRunnerFunc(func(ctx context.Context, _ string, _ string) (string, int, error) {
		return "cancelled\n", -1, ctx.Err()
	}))(ctx, map[string]any{"workdir": root, "job_id": "cancelled", "pipeline": "change"})
	if err != nil || result.Error != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	artifact := readCapsuleCIArtifact(t, root, "cancelled")
	entry := artifact["checks"].([]any)[0].(map[string]any)
	if entry["error_kind"] != "cancelled" {
		t.Fatalf("cancellation evidence = %#v", entry)
	}
}

func TestShellCapsuleCICommandRunnerDistinguishesTimeoutFromExitFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, exitCode, err := (shellCapsuleCICommandRunner{}).Run(ctx, t.TempDir(), "sleep 1")
	if exitCode != -1 || err != context.DeadlineExceeded {
		t.Fatalf("timeout run = exit %d, err %v", exitCode, err)
	}
}

func capsuleCIProfileRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".kitsoki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte("schema: project-profile/v1\nid: example\ncommands:\n  test: go test ./...\n  build: go build ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func readCapsuleCIArtifact(t *testing.T, root, jobID string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".artifacts", "capsule-ci", "checks", jobID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact map[string]any
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestCapsuleCIProjectChecksParksWhenCommandsAreMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".kitsoki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte("schema: project-profile/v1\nid: empty\ncommands: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := NewCapsuleCIProjectChecksHandler(CapsuleCICommandRunnerFunc(func(context.Context, string, string) (string, int, error) {
		t.Fatal("runner should not be called")
		return "", 0, nil
	}))(context.Background(), map[string]any{"workdir": root, "pipeline": "change"})
	if err != nil {
		t.Fatal(err)
	}
	verdict := result.Data["verdict"].(map[string]any)
	if verdict["outcome"] != "needs_input" || verdict["promotion_eligible"] != false {
		t.Fatalf("verdict = %#v", verdict)
	}
}
