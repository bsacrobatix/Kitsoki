package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/capsule/vmpool"
)

// runVmp executes the vmpool command tree in isolation (no dependency on the
// full root command) with the given args, returning combined stdout/stderr.
func runVmp(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := vmpoolCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// vmpWithFakeProvisioner points every vmpool subcommand at fake for the
// duration of the test, regardless of --token-env / DO token availability.
func vmpWithFakeProvisioner(t *testing.T, fake *vmpool.Fake) {
	t.Helper()
	orig := vmpoolNewProvisioner
	vmpoolNewProvisioner = func(string) (vmpool.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { vmpoolNewProvisioner = orig })
}

// vmpWithFixedNow pins vmpoolNow (used for image-builder timestamps/names) to
// ts for the duration of the test.
func vmpWithFixedNow(t *testing.T, ts time.Time) {
	t.Helper()
	orig := vmpoolNow
	vmpoolNow = func() time.Time { return ts }
	t.Cleanup(func() { vmpoolNow = orig })
}

// vmpSeedWorker writes w directly into the durable store at project, as if a
// prior Pool.Acquire had run.
func vmpSeedWorker(t *testing.T, project string, w vmpool.Worker) {
	t.Helper()
	store := vmpool.Store{ProjectRoot: project}
	require.NoError(t, store.Update(func(s *vmpool.State) error {
		s.Workers = append(s.Workers, w)
		return nil
	}))
}

func TestVmpStatusRendersWorkersFromSeededStore(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	inst, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "kitsoki-worker-job-1", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)
	fake.Activate(inst.ID, "10.0.0.5", "")

	vmpSeedWorker(t, dir, vmpool.Worker{
		ID: "vm-job-1", JobID: "job-1", InstanceID: inst.ID, InstanceName: "kitsoki-worker-job-1",
		Status: vmpool.StatusReady, PublicIP: "10.0.0.5", CreatedAt: time.Now().UTC(),
	})

	out, err := runVmp(t, "status", "--project", dir, "--json")
	require.NoError(t, err)

	var got vmpoolStatusOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, vmpool.DefaultTag, got.Config.Tag)
	require.Equal(t, vmpool.DefaultRegion, got.Config.Region)
	require.Len(t, got.Workers, 1)
	require.Equal(t, "vm-job-1", got.Workers[0].ID)
	require.Contains(t, got.Reconcile.Active, inst.ID)
	require.Empty(t, got.Reconcile.Orphans)
	require.Empty(t, got.Reconcile.Lost)
}

func TestVmpStatusHumanOutputListsWorkerLine(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	vmpSeedWorker(t, dir, vmpool.Worker{ID: "vm-job-9", JobID: "job-9", Status: vmpool.StatusCreating, CreatedAt: time.Now().UTC()})

	out, err := runVmp(t, "status", "--project", dir)
	require.NoError(t, err)
	require.Contains(t, out, "vm-job-9")
	require.Contains(t, out, "job-9")
}

func TestVmpReleaseMarksDestroyed(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	inst, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "w", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)
	vmpSeedWorker(t, dir, vmpool.Worker{ID: "vm-job-2", JobID: "job-2", InstanceID: inst.ID, Status: vmpool.StatusReady, CreatedAt: time.Now().UTC()})

	out, err := runVmp(t, "release", "vm-job-2", "--project", dir)
	require.NoError(t, err)

	var w vmpool.Worker
	require.NoError(t, json.Unmarshal([]byte(out), &w))
	require.Equal(t, vmpool.StatusDestroyed, w.Status)
	require.Contains(t, fake.Destroyed(), inst.ID)
}

func TestVmpReapPlanOnlyDoesNotDestroyOrphans(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	inst, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "orphan", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)

	out, err := runVmp(t, "reap", "--project", dir)
	require.NoError(t, err)

	var report vmpool.ReconcileReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Contains(t, report.Orphans, inst.ID)
	require.Empty(t, fake.Destroyed(), "plan-only reap must not destroy anything")
}

func TestVmpReapRepairDestroysOrphansAndMarksLost(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	orphan, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "orphan", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)
	// A durable worker whose instance no longer exists in the cloud ("lost").
	vmpSeedWorker(t, dir, vmpool.Worker{ID: "vm-job-lost", JobID: "job-lost", InstanceID: "does-not-exist", Status: vmpool.StatusReady, CreatedAt: time.Now().UTC()})

	out, err := runVmp(t, "reap", "--project", dir, "--repair")
	require.NoError(t, err)

	var report vmpool.ReconcileReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Contains(t, report.Orphans, orphan.ID)
	require.Contains(t, report.Lost, "vm-job-lost")
	require.Contains(t, fake.Destroyed(), orphan.ID)

	state, err := (vmpool.Store{ProjectRoot: dir}).Load()
	require.NoError(t, err)
	w, ok := state.WorkerByID("vm-job-lost")
	require.True(t, ok)
	require.Equal(t, vmpool.StatusFailed, w.Status)
}

// TestVmpReapPlanOnlyClassifiesPreservedWorkersInsteadOfOrphans is the
// regression test for the fast-follow this change fixes: the plan-only path
// (`vmpool reap`/`vmpool status` without --repair) must classify a
// PreserveFailed worker's instance as preserved-protected or
// preserved-expired-reclaimable via the same vmpool.Classify Pool.Reconcile
// uses, never as a plain orphan, and must take no action either way.
func TestVmpReapPlanOnlyClassifiesPreservedWorkersInsteadOfOrphans(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	protected, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "protected", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)
	expired, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "expired", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)

	// Default PreserveFailedTTL is 4h (vmpool.DefaultPreserveFailedTTL):
	// well within it counts as protected, well past it counts as expired.
	vmpSeedWorker(t, dir, vmpool.Worker{
		ID: "vm-protected", JobID: "job-protected", InstanceID: protected.ID,
		Status: vmpool.StatusFailed, Preserved: true, TerminalAt: time.Now().UTC().Add(-1 * time.Hour),
	})
	vmpSeedWorker(t, dir, vmpool.Worker{
		ID: "vm-expired", JobID: "job-expired", InstanceID: expired.ID,
		Status: vmpool.StatusFailed, Preserved: true, TerminalAt: time.Now().UTC().Add(-5 * time.Hour),
	})

	out, err := runVmp(t, "reap", "--project", dir)
	require.NoError(t, err)

	var report vmpool.ReconcileReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))

	require.Empty(t, report.Orphans, "preserved workers must never be reported as orphans")
	require.Len(t, report.PreservedProtected, 1)
	require.Equal(t, "vm-protected", report.PreservedProtected[0].WorkerID)
	require.Positive(t, report.PreservedProtected[0].TTLRemaining, "protected worker should report TTL remaining")
	require.Contains(t, report.ExpiredPreserved, "vm-expired")
	require.Empty(t, fake.Destroyed(), "plan-only reap must not destroy anything")

	state, err := (vmpool.Store{ProjectRoot: dir}).Load()
	require.NoError(t, err)
	protectedWorker, ok := state.WorkerByID("vm-protected")
	require.True(t, ok)
	require.True(t, protectedWorker.Preserved, "plan-only reap must not mutate durable state")
	expiredWorker, ok := state.WorkerByID("vm-expired")
	require.True(t, ok)
	require.True(t, expiredWorker.Preserved, "plan-only reap must not mutate durable state")
}

// TestVmpReapRepairReclaimsExpiredPreservedButProtectsWithinTTL is the
// --repair counterpart: it must destroy and reclaim only the
// preserve_failed_ttl-expired worker's instance, leaving the still-protected
// one running and untouched.
func TestVmpReapRepairReclaimsExpiredPreservedButProtectsWithinTTL(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	protected, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "protected", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)
	expired, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "expired", Tags: []string{vmpool.DefaultTag}})
	require.NoError(t, err)

	vmpSeedWorker(t, dir, vmpool.Worker{
		ID: "vm-protected", JobID: "job-protected", InstanceID: protected.ID,
		Status: vmpool.StatusFailed, Preserved: true, TerminalAt: time.Now().UTC().Add(-1 * time.Hour),
	})
	vmpSeedWorker(t, dir, vmpool.Worker{
		ID: "vm-expired", JobID: "job-expired", InstanceID: expired.ID,
		Status: vmpool.StatusFailed, Preserved: true, TerminalAt: time.Now().UTC().Add(-5 * time.Hour),
	})

	out, err := runVmp(t, "reap", "--project", dir, "--repair")
	require.NoError(t, err)

	var report vmpool.ReconcileReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Contains(t, report.ExpiredPreserved, "vm-expired")
	require.Len(t, report.PreservedProtected, 1)
	require.Equal(t, "vm-protected", report.PreservedProtected[0].WorkerID)

	require.Contains(t, fake.Destroyed(), expired.ID)
	require.NotContains(t, fake.Destroyed(), protected.ID)

	state, err := (vmpool.Store{ProjectRoot: dir}).Load()
	require.NoError(t, err)
	expiredWorker, ok := state.WorkerByID("vm-expired")
	require.True(t, ok)
	require.False(t, expiredWorker.Preserved, "expired preservation must be cleared on repair")
	protectedWorker, ok := state.WorkerByID("vm-protected")
	require.True(t, ok)
	require.True(t, protectedWorker.Preserved, "still-protected worker must not be reclaimed early")
}

func TestVmpImageBuildCreatesBuilderWithExpectedUserData(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)
	fixed := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	vmpWithFixedNow(t, fixed)

	_, err := runVmp(t, "image", "build",
		"--project", dir,
		"--name", "kitsoki-worker-base-2026-07-19",
		"--kitsoki-tarball-url", "https://example.com/kitsoki-src.tar.gz",
		"--go-version", "1.25.0",
		"--node-major", "20",
	)
	require.NoError(t, err)

	created := fake.Created()
	require.Len(t, created, 1)
	cp := created[0]
	require.Equal(t, fmt.Sprintf("kitsoki-image-builder-%d", fixed.Unix()), cp.Name)
	require.Equal(t, []string{vmpoolBuilderTag}, cp.Tags)
	require.Contains(t, cp.UserData, "npm install -g @anthropic-ai/claude-code @openai/codex")
	require.Contains(t, cp.UserData, "GO_VERSION=1.25.0")
	require.Contains(t, cp.UserData, "NODE_MAJOR=20")
	require.Contains(t, cp.UserData, "KITSOKI_TARBALL_URL=https://example.com/kitsoki-src.tar.gz")
	require.Contains(t, cp.UserData, vmpoolVerifyScriptPath)
	require.Contains(t, cp.UserData, "VERIFIED")
	require.Contains(t, cp.UserData, "go build -o /usr/local/bin/kitsoki")

	builders, err := vmpoolLoadImageBuilders(dir)
	require.NoError(t, err)
	require.Len(t, builders, 1)
	require.Equal(t, "kitsoki-worker-base-2026-07-19", builders[0].SnapshotName)
	require.Equal(t, cp.Name, builders[0].Name)
	require.Equal(t, fixed, builders[0].CreatedAt)
}

func TestVmpImageBuildWithoutTarballURLDocumentsManualHandoff(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	out, err := runVmp(t, "image", "build", "--project", dir, "--name", "snap-manual")
	require.NoError(t, err)
	require.Contains(t, out, "ssh root@")
	require.Contains(t, out, "claude login")
	require.Contains(t, out, "codex login")
	require.Contains(t, out, "kitsoki --version")
	require.Contains(t, out, vmpoolVerifyScriptPath)

	created := fake.Created()
	require.Len(t, created, 1)
	require.Contains(t, created[0].UserData, "operator must scp the kitsoki binary")
}

func TestVmpImageFinalizeCallsSnapshotAndReportsImageID(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)
	vmpWithFixedNow(t, time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC))

	_, err := runVmp(t, "image", "build", "--project", dir, "--name", "kitsoki-worker-base-v1")
	require.NoError(t, err)

	builders, err := vmpoolLoadImageBuilders(dir)
	require.NoError(t, err)
	require.Len(t, builders, 1)
	builder := builders[0]

	out, err := runVmp(t, "image", "finalize", "--project", dir, "--builder", builder.Name)
	require.NoError(t, err)

	var result struct {
		ImageID          string `json:"image_id"`
		SnapshotName     string `json:"snapshot_name"`
		BuilderDestroyed bool   `json:"builder_destroyed"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "kitsoki-worker-base-v1", result.SnapshotName)
	require.False(t, result.BuilderDestroyed)

	snaps := fake.Snapshots()
	require.Len(t, snaps, 1)
	require.Equal(t, builder.DropletID, snaps[0].InstanceID)
	require.Equal(t, "kitsoki-worker-base-v1", snaps[0].Name)
	require.Equal(t, snaps[0].ImageID, result.ImageID)
	require.NotContains(t, fake.Destroyed(), builder.DropletID)
}

func TestVmpImageFinalizeDestroyBuilderDestroysAfterSnapshot(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	_, err := runVmp(t, "image", "build", "--project", dir, "--name", "snap-destroy")
	require.NoError(t, err)
	builders, err := vmpoolLoadImageBuilders(dir)
	require.NoError(t, err)
	require.Len(t, builders, 1)

	out, err := runVmp(t, "image", "finalize", "--project", dir, "--builder", builders[0].Name, "--destroy-builder")
	require.NoError(t, err)

	var result struct {
		BuilderDestroyed bool `json:"builder_destroyed"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.True(t, result.BuilderDestroyed)
	require.Contains(t, fake.Destroyed(), builders[0].DropletID)
}

func TestVmpImageFinalizeByDropletIDFallsBackWhenUnrecorded(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	inst, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "out-of-band-builder", Tags: []string{vmpoolBuilderTag}})
	require.NoError(t, err)

	out, err := runVmp(t, "image", "finalize", "--project", dir, "--builder", inst.ID, "--name", "snap-oob")
	require.NoError(t, err)

	var result struct {
		ImageID string `json:"image_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.NotEmpty(t, result.ImageID)

	snaps := fake.Snapshots()
	require.Len(t, snaps, 1)
	require.Equal(t, inst.ID, snaps[0].InstanceID)
}

func TestVmpImageFinalizeRequiresSnapshotName(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	inst, err := fake.Create(context.Background(), vmpool.CreateParams{Name: "no-name-builder", Tags: []string{vmpoolBuilderTag}})
	require.NoError(t, err)

	_, err = runVmp(t, "image", "finalize", "--project", dir, "--builder", inst.ID)
	require.Error(t, err)
}

func TestVmpImageListShowsConfiguredImageAndBuilders(t *testing.T) {
	dir := t.TempDir()
	fake := vmpool.NewFake()
	vmpWithFakeProvisioner(t, fake)

	_, err := runVmp(t, "image", "build", "--project", dir, "--name", "snap-list")
	require.NoError(t, err)

	out, err := runVmp(t, "image", "list", "--project", dir, "--image", "kitsoki-worker-base", "--json")
	require.NoError(t, err)

	var result struct {
		Image    string               `json:"image"`
		Builders []vmpoolImageBuilder `json:"builders"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "kitsoki-worker-base", result.Image)
	require.Len(t, result.Builders, 1)
	require.Equal(t, "snap-list", result.Builders[0].SnapshotName)
}

func TestVmpResolveBuilderPrefersDropletIDOverName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, vmpoolAppendImageBuilder(dir, vmpoolImageBuilder{DropletID: "1", Name: "builder-a", SnapshotName: "snap-a", CreatedAt: time.Unix(100, 0)}))
	require.NoError(t, vmpoolAppendImageBuilder(dir, vmpoolImageBuilder{DropletID: "2", Name: "builder-b", SnapshotName: "snap-b", CreatedAt: time.Unix(200, 0)}))

	byID, err := vmpoolResolveBuilder(dir, "2")
	require.NoError(t, err)
	require.Equal(t, "builder-b", byID.Name)

	byName, err := vmpoolResolveBuilder(dir, "builder-a")
	require.NoError(t, err)
	require.Equal(t, "1", byName.DropletID)

	_, err = vmpoolResolveBuilder(dir, "nonexistent-name")
	require.Error(t, err)
}

func TestVmpResolveBuilderPicksMostRecentOnDuplicateName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, vmpoolAppendImageBuilder(dir, vmpoolImageBuilder{DropletID: "1", Name: "dup", SnapshotName: "old", CreatedAt: time.Unix(100, 0)}))
	require.NoError(t, vmpoolAppendImageBuilder(dir, vmpoolImageBuilder{DropletID: "2", Name: "dup", SnapshotName: "new", CreatedAt: time.Unix(200, 0)}))

	got, err := vmpoolResolveBuilder(dir, "dup")
	require.NoError(t, err)
	require.Equal(t, "2", got.DropletID)
	require.Equal(t, "new", got.SnapshotName)
}

func TestVmpDetectGoVersionParsesGoMod(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.25.0\n\nrequire (\n)\n"), 0o644))
	require.Equal(t, "1.25.0", vmpoolDetectGoVersion(dir))
}

func TestVmpDetectGoVersionFallsBackWithoutGoMod(t *testing.T) {
	dir := t.TempDir()
	require.Equal(t, vmpoolFallbackGoVersion, vmpoolDetectGoVersion(dir))
}

func TestVmpBuilderUserDataRejectsMissingGoVersion(t *testing.T) {
	_, err := vmpoolBuilderUserData(vmpoolBuilderSpec{})
	require.Error(t, err)
}
