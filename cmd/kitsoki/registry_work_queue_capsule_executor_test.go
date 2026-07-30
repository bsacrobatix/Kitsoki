package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workqueue"
)

func TestCapsuleQueueClientProjectsServerOwnedRetainedWIPOverStoryClaims(t *testing.T) {
	data := []byte("canonical retained WIP")
	client := capsuleQueueClient{
		binding:    webconfig.WorkQueueExecutorBinding{BundleRefOutput: "bundle_ref", BundleDigestOutput: "bundle_digest", BundleKindOutput: "bundle_kind"},
		bundleRoot: t.TempDir(),
		readWIP:    func(context.Context, ci.Config, string, string) ([]byte, error) { return data, nil },
	}
	verdict := ci.Verdict{Outcome: "passed", Outputs: map[string]string{"bundle_ref": "story-supplied", "bundle_digest": "sha256:bad", "bundle_kind": "other"}}
	if err := client.projectRetainedWIP(context.Background(), ci.Config{}, "pool", "exec-1", &verdict); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got, want := verdict.Outputs["bundle_ref"], "capsule-ci/exec-1.bundle"; got != want {
		t.Fatalf("bundle ref = %q, want %q", got, want)
	}
	if got, want := verdict.Outputs["bundle_digest"], "sha256:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("bundle digest = %q, want %q", got, want)
	}
	if verdict.Outputs["bundle_kind"] != "git-bundle" {
		t.Fatalf("bundle kind = %q", verdict.Outputs["bundle_kind"])
	}
}

func TestCapsuleQueuePromoterImportsDetachedMultiRefOutputAndReplays(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	wqGit(t, project, "init", "-b", "main")
	wqGit(t, project, "config", "user.name", "test")
	wqGit(t, project, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(project, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wqGit(t, project, "add", ".")
	wqGit(t, project, "commit", "-m", "base")
	sealed := wqGit(t, project, "rev-parse", "HEAD")

	worker := t.TempDir()
	wqGit(t, worker, "clone", project, ".")
	wqGit(t, worker, "config", "user.name", "worker")
	wqGit(t, worker, "config", "user.email", "worker@example.invalid")
	wqGit(t, worker, "branch", "unrelated-base", sealed)
	wqGit(t, worker, "checkout", "--detach", sealed)
	if err := os.WriteFile(filepath.Join(worker, "fix.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wqGit(t, worker, "add", ".")
	wqGit(t, worker, "commit", "-m", "fix")
	output := wqGit(t, worker, "rev-parse", "HEAD")
	bundleRoot := t.TempDir()
	bundlePath := filepath.Join(bundleRoot, "retained.bundle")
	wqGit(t, worker, "bundle", "create", bundlePath, "--all")
	if err := exec.Command("git", "-C", project, "cat-file", "-e", output+"^{commit}").Run(); err == nil {
		t.Fatal("project unexpectedly has worker output before durable admission")
	}
	body, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	runID := "ci-run"
	run := ci.RunRecord{JobID: runID, Result: ci.RunResult{
		Job:      artifactjob.Job{ID: artifactjob.JobID(runID)},
		Envelope: executor.Envelope{SourceDigest: sealed},
		Verdict:  ci.Verdict{Outcome: "passed"},
		Terminal: true,
	}}
	if err := (ci.FileRunStore{ProjectRoot: project}).Write(run); err != nil {
		t.Fatal(err)
	}
	promoter := capsuleQueuePromoter{
		binding: webconfig.WorkQueueExecutorBinding{
			ProjectRoot: project, Pipeline: "change", PromotionTarget: "main",
			PromotionTargetPolicy: "wave-auto", PromotionFinalizationPolicy: "autonomous",
		},
		bundleRoot: bundleRoot,
	}
	request := workqueue.CapsulePromotionRequest{
		Job:    workqueue.Job{ID: "work-one", ApplicationID: "app", Queue: "fix", ProducesCode: true},
		Result: workqueue.CapsuleResult{RunRef: runID, ExecutionRef: "exec-one", BundleRef: "retained.bundle", BundleDigest: "sha256:" + hex.EncodeToString(sum[:])},
	}
	first, err := promoter.Promote(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := promoter.Promote(ctx, request)
	if err != nil || replay != first {
		t.Fatalf("replay=%q first=%q err=%v", replay, first, err)
	}
	request.Job.ID, request.Result.ExecutionRef = "work-two", "exec-two"
	duplicate, err := promoter.Promote(ctx, request)
	if err != nil || duplicate != first {
		t.Fatalf("identical second job=%q first=%q err=%v", duplicate, first, err)
	}
	state, err := (queueStoreForTest(project)).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].SHA != output ||
		state.Candidates[0].Admission != "durable_bundle" || state.Candidates[0].RequiredGateTier != "full" {
		t.Fatalf("durable candidates=%#v", state.Candidates)
	}
}

func queueStoreForTest(project string) queue.Store { return queue.Store{ProjectRoot: project} }

func wqGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCapsuleQueueClientRetainedWIPFailsClosed(t *testing.T) {
	client := capsuleQueueClient{
		binding:    webconfig.WorkQueueExecutorBinding{BundleRefOutput: "bundle_ref", BundleDigestOutput: "bundle_digest"},
		bundleRoot: t.TempDir(),
		readWIP:    func(context.Context, ci.Config, string, string) ([]byte, error) { return nil, context.DeadlineExceeded },
	}
	verdict := ci.Verdict{Outcome: "passed"}
	if err := client.projectRetainedWIP(context.Background(), ci.Config{}, "pool", "exec-1", &verdict); err == nil {
		t.Fatal("missing retained WIP unexpectedly projected")
	}
	client.readWIP = func(context.Context, ci.Config, string, string) ([]byte, error) { return []byte("bytes"), nil }
	if err := client.projectRetainedWIP(context.Background(), ci.Config{}, "pool", "../unsafe", &verdict); err == nil {
		t.Fatal("unsafe execution id unexpectedly projected")
	}
}
