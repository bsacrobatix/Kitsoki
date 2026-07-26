package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"

	"github.com/spf13/cobra"
)

type exactSourceRefreshProvider struct {
	status   executor.ExecutionStatus
	released []string
}

func (p *exactSourceRefreshProvider) Describe(context.Context) (executor.Capabilities, error) {
	return executor.Capabilities{}, nil
}
func (p *exactSourceRefreshProvider) Prepare(_ context.Context, envelope executor.Envelope) (executor.Prepared, error) {
	return executor.Prepared{ID: p.status.ExecutionID, Envelope: envelope, Placement: "remote", Applied: envelope.Policy}, nil
}
func (p *exactSourceRefreshProvider) Run(context.Context, executor.Prepared, executor.Task, executor.EventSink) (executor.Result, error) {
	return executor.Result{}, nil
}
func (p *exactSourceRefreshProvider) Cancel(context.Context, string) error { return nil }
func (p *exactSourceRefreshProvider) Status(context.Context, string) (executor.ExecutionStatus, error) {
	return p.status, nil
}
func (p *exactSourceRefreshProvider) RequestCancel(context.Context, string) (executor.ExecutionStatus, error) {
	return executor.ExecutionStatus{}, nil
}
func (p *exactSourceRefreshProvider) ReleaseDetached(_ context.Context, jobID string) error {
	p.released = append(p.released, jobID)
	return nil
}

func TestCapsuleCIRefreshFinalizesExactSourceWithoutWorkspaceAndReleasesWorker(t *testing.T) {
	root := t.TempDir()
	contract := ci.ResultContract{
		Schema:    ci.VerdictSchema,
		PassExits: []string{"passed"},
		FailExits: []string{"failed"},
		ParkExits: []string{"needs_input"},
	}
	remote := ci.Remote{Pool: &ci.PoolExecutor{
		TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1",
		SourceBucket: &ci.SourceBucket{
			URL: "https://bucket.sgp1.digitaloceanspaces.com/kitsoki", KeyEnv: "BUCKET_KEY", SecretEnv: "BUCKET_SECRET",
		},
	}}
	authority, err := ci.NewImmutableExecutorControlAuthority(
		ci.Config{Remotes: map[string]ci.Remote{"vm-pool": remote}},
		"change", "vm-pool", contract,
	)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := environment.SealLock(environment.Lock{
		Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:environment-definition",
		Network: "none", Sandbox: "supervised",
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{
		JobID: "job-exact", ProjectID: "project", DefinitionDigest: "sha256:definition",
		Instance:     control.Handle{ID: "logical-exact-instance", Generation: 1},
		SourceDigest: "0123456789012345678901234567890123456789",
		StoryPath:    ".kitsoki/stories/change/app.yaml", StoryDigest: "sha256:story",
		Environment: lock, Policy: executor.Policy{Network: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{
		Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed",
		Checks:            []ci.Check{{ID: "gate", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:gate"}}},
		PromotionEligible: true, SourceDigest: envelope.SourceDigest, StoryDigest: envelope.StoryDigest,
		EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest,
	}
	verdictJSON, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	provider := &exactSourceRefreshProvider{status: executor.ExecutionStatus{
		Schema: executor.ExecutionStatusSchema, ExecutionID: "exec-exact",
		EnvelopeDigest: envelope.Digest, Status: "completed", Stage: "terminal",
		UpdatedAt: time.Now().UTC(), TerminalAt: time.Now().UTC(),
		Result: executor.Result{ExecutionID: "exec-exact", ExitCode: 0, VerdictJSON: verdictJSON},
	}}
	originalSelect := capsuleCISelectConfiguredExecutor
	capsuleCISelectConfiguredExecutor = func(_ context.Context, cfg ci.Config, executorName, projectRoot, poolStateRoot string) (executor.Provider, error) {
		if executorName != "vm-pool" || projectRoot != root || poolStateRoot != root {
			t.Fatalf("selection = executor %q project %q pool %q", executorName, projectRoot, poolStateRoot)
		}
		if cfg.Remotes["vm-pool"].Pool == nil {
			t.Fatalf("persisted remote placement was not reconstructed: %#v", cfg)
		}
		return provider, nil
	}
	t.Cleanup(func() { capsuleCISelectConfiguredExecutor = originalSelect })

	store := ci.FileRunStore{ProjectRoot: root}
	seed := ci.RunRecord{JobID: "job-exact", Result: ci.RunResult{
		Job:      artifactjob.Job{ID: "job-exact", Status: artifactjob.StatusRunning},
		Envelope: envelope, Execution: executor.Result{ExecutionID: "exec-exact"},
		Pipeline: "change", Executor: "vm-pool", Stage: ci.RunStageRunning,
		ExecutorControl: &authority,
	}}
	if err := store.Write(seed); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	got, err := capsuleCIRefreshJob(cmd, root, "job-exact", store, seed)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Result.Terminal || got.Result.Job.Status != artifactjob.StatusDone || got.Result.Verdict.Outcome != "passed" {
		t.Fatalf("refreshed run = %#v", got.Result)
	}
	if got.ReceiptID == "" || got.ReceiptVerification != "valid" {
		t.Fatalf("receipt = %q verification = %q", got.ReceiptID, got.ReceiptVerification)
	}
	if len(provider.released) != 1 || provider.released[0] != "job-exact" {
		t.Fatalf("detached releases = %v", provider.released)
	}
	if _, err := os.Stat(filepath.Join(root, ".capsules", "workspaces", "logical-exact-instance")); !os.IsNotExist(err) {
		t.Fatalf("refresh materialized a workspace: %v", err)
	}
}

func TestCapsuleCIRefreshRecoversLegacyExactSourceAuthorityAfterCurrentHeadAdvances(t *testing.T) {
	root := t.TempDir()
	writeCapsuleCIDevelopmentDefinition(t, root)
	files := map[string]string{
		".kitsoki/ci.yaml": `schema: capsule-ci/v1
default_environment: ci
remotes:
  vm-pool:
    pool:
      token_env: DO_TOKEN
      image: img
      size: s-1vcpu-1gb
      region: sgp1
      source_bucket:
        url: https://bucket.sgp1.digitaloceanspaces.com/kitsoki
        key_env: BUCKET_KEY
        secret_env: BUCKET_SECRET
pipelines:
  change:
    story: .kitsoki/stories/ci/app.yaml
    triggers: [local]
    executor: vm-pool
    result:
      schema: capsule-ci-verdict/v1
      pass_exits: [passed]
      fail_exits: [failed]
      park_exits: [needs_input]
`,
		".kitsoki/environments/ci.yaml": "schema: capsule-environment/v1\nid: ci\nnetwork: none\nsandbox: supervised\n",
		".kitsoki/stories/ci/app.yaml": `app:
  id: ci
  version: 0.1.0
  title: CI
  author: Test
  license: CC0
world: {}
intents: {}
root: idle
states:
  idle:
    view:
      - prose: ok
`,
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.invalid"},
		{"add", "."},
		{"commit", "-qm", "fixture"},
	} {
		command := exec.Command("git", args...)
		command.Dir = root
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	head, err := gitTrim(context.Background(), root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	run := ci.RunRecord{JobID: "legacy-exact", Result: ci.RunResult{
		Pipeline: "change", Executor: "vm-pool",
		Envelope: executor.Envelope{
			Instance:     control.Handle{ID: "logical-legacy-instance", Generation: 1},
			SourceDigest: head,
		},
		Execution: executor.Result{ExecutionID: "exec-legacy"},
	}}
	_, pipeline, executorName, projectRoot, recovered, err := capsuleCIExecutorControlInputs(context.Background(), root, run)
	if err != nil {
		t.Fatal(err)
	}
	if recovered == nil || recovered.SourceMode != ci.SourceModeImmutable || recovered.Executor != "vm-pool" {
		t.Fatalf("recovered authority = %#v", recovered)
	}
	if pipeline.Result.Schema != ci.VerdictSchema || executorName != "vm-pool" || projectRoot != root {
		t.Fatalf("pipeline=%#v executor=%q project=%q", pipeline, executorName, projectRoot)
	}

	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), []byte("changed current config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "add", ".kitsoki/ci.yaml")
	command.Dir = root
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add current config: %v: %s", err, out)
	}
	command = exec.Command("git", "commit", "-qm", "advance current")
	command.Dir = root
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit current config: %v: %s", err, out)
	}
	currentHead, err := gitTrim(context.Background(), root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if currentHead == head {
		t.Fatal("fixture current head did not advance")
	}
	_, _, _, _, recovered, err = capsuleCIExecutorControlInputs(context.Background(), root, run)
	if err != nil {
		t.Fatalf("recover old sealed source after current HEAD advanced: %v", err)
	}
	if recovered == nil || recovered.Executor != "vm-pool" || recovered.Result.Schema != ci.VerdictSchema {
		t.Fatalf("recovered authority after advance = %#v", recovered)
	}

	unavailable := run
	unavailable.Result.Envelope.SourceDigest = strings.Repeat("f", 40)
	_, _, _, _, _, err = capsuleCIExecutorControlInputs(context.Background(), root, unavailable)
	if err == nil || !strings.Contains(err.Error(), "sealed source commit") || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable sealed source recovery error = %v", err)
	}
}
