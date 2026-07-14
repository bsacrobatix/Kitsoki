package queue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStagingIntegrationProcessesReceiptBoundCandidateThroughProtectedStaging(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/candidate", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	gate := "git diff --check"
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: StagingIntegration{ProjectRoot: root, GateCommand: gate},
		Gate:        ShellGate{Command: gate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].Status != Landed {
		t.Fatalf("queue state=%#v", state.Candidates)
	}
	if got := git(t, root, "rev-parse", "main"); got != base {
		t.Fatalf("protected main=%s, want unchanged base=%s", got, base)
	}
	if got := git(t, root, "rev-parse", "staging/local"); got != sha {
		t.Fatalf("staging=%s, want candidate=%s", got, sha)
	}
}

func TestShellGateRejectsDirtySpeculativeWorkspace(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Queue Test")
	git(t, root, "config", "user.email", "queue@example.invalid")
	commit(t, root, "base.txt", "base\n", "base")
	_, err := (ShellGate{Command: "touch leaked.txt"}).Run(context.Background(), Speculation{WorkspacePath: root})
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("err=%v", err)
	}
}

func protectedQueueRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Queue Test")
	git(t, root, "config", "user.email", "queue@example.invalid")
	copyQueueScript(t, root, "dev-workspace.sh")
	copyQueueScript(t, root, "refresh-staging-local.sh")
	copyQueueScript(t, root, "protected-main-mode.sh")
	commit(t, root, "base.txt", "base\n", "base")
	base := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "staging/local", base)
	staging := filepath.Join(root, ".capsules", "staging", "local")
	if err := os.MkdirAll(filepath.Dir(staging), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "git", "clone", "--no-local", root, staging)
	git(t, staging, "remote", "rename", "origin", "source")
	git(t, staging, "config", "user.name", "Queue Test")
	git(t, staging, "config", "user.email", "queue@example.invalid")
	git(t, staging, "switch", "-c", "staging/local", "source/staging/local")
	if err := os.WriteFile(filepath.Join(staging, ".kitsoki-capsule"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func copyQueueScript(t *testing.T, root, name string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	source := filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", name)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "scripts", name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, path, text, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", path)
	git(t, dir, "commit", "-m", message)
}

func git(t *testing.T, dir string, args ...string) string { return run(t, dir, "git", args...) }

func run(t *testing.T, dir, program string, args ...string) string {
	t.Helper()
	cmd := exec.Command(program, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", program, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
