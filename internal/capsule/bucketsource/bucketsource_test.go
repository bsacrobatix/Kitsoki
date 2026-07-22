package bucketsource_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/capsule/bucketsource"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/storydigest"
	"kitsoki/internal/capsule/workerserver"
	"kitsoki/internal/capsuletest"
	"kitsoki/internal/objectstore"
)

// countingStore wraps a Store and counts Puts per key so tests can assert
// write-once semantics for frozen sources.
type countingStore struct {
	objectstore.Store
	mu   sync.Mutex
	puts map[string]int
}

func (c *countingStore) Put(ctx context.Context, key string, body io.Reader, size int64, opts objectstore.PutOptions) (objectstore.Meta, error) {
	c.mu.Lock()
	if c.puts == nil {
		c.puts = map[string]int{}
	}
	c.puts[key]++
	c.mu.Unlock()
	return c.Store.Put(ctx, key, body, size, opts)
}

func (c *countingStore) putCount(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.puts[key]
}

// fakeFetcher resolves the Fake store's opaque presign URLs back into reads,
// standing in for the https fetch a production worker performs.
func fakeFetcher(store objectstore.Store) workerserver.SourceFetcher {
	return func(ctx context.Context, fetchURL string, max int64) ([]byte, error) {
		u, err := url.Parse(fetchURL)
		if err != nil || u.Scheme != "fake-presign" || u.Host != "get" {
			return nil, fmt.Errorf("unexpected fetch URL %q", fetchURL)
		}
		rc, _, err := store.Get(ctx, strings.TrimPrefix(u.Path, "/"))
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, max+1))
	}
}

type collectingSink struct {
	mu     sync.Mutex
	events []executor.Event
}

func (c *collectingSink) Emit(_ context.Context, event executor.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *collectingSink) find(kind string) []executor.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []executor.Event
	for _, event := range c.events {
		if event.Kind == kind {
			out = append(out, event)
		}
	}
	return out
}

// TestBucketTransportEndToEnd drives the full path: controller publishes the
// bundle to the (fake) bucket and sends a fetch reference; the worker
// downloads, verifies, materializes, runs the story, and mirrors run record +
// trace back to the bucket. A second fresh worker sharing the bucket then
// exercises the bucket cache hit: the frozen bundle is never uploaded twice.
func TestBucketTransportEndToEnd(t *testing.T) {
	project := capsuletest.Open(t, "clean-repo")
	storyRel := filepath.ToSlash(filepath.Join(".kitsoki", "stories", "ci", "app.yaml"))
	write(t, filepath.Join(project, storyRel), passingStory)
	envRel := writeEnvironment(t, project)
	git(t, project, "add", storyRel, envRel)
	git(t, project, "-c", "user.name=Capsule Test", "-c", "user.email=capsule@example.invalid", "commit", "-m", "Add no-LLM CI story")
	head := strings.TrimSpace(git(t, project, "rev-parse", "HEAD"))
	closure, err := storydigest.Compute(project, storyRel)
	if err != nil {
		t.Fatal(err)
	}
	envLock, err := (environment.Resolver{ProjectRoot: project}).Resolve(context.Background(), "ci")
	if err != nil {
		t.Fatal(err)
	}

	bucket := &countingStore{Store: objectstore.NewFake()}
	publisher := bucketsource.Publisher{Store: bucket, PresignTTL: time.Hour}
	runner := func(ctx context.Context, workspace string, prepared executor.Prepared, tracePath string) (executor.Result, error) {
		if err := os.WriteFile(tracePath, []byte(`{"kind":"story.turn","outcome":"passed"}`+"\n"), 0o600); err != nil {
			return executor.Result{}, err
		}
		return executor.Result{ExitCode: 0, VerdictArtifact: "verdict:bucket-e2e"}, nil
	}

	newWorker := func(root string) *httptest.Server {
		worker, err := workerserver.New(workerserver.Config{
			Root:          root,
			Token:         "test-token",
			RequireAuth:   true,
			Capabilities:  isolatedTestCapabilities(),
			Runner:        runner,
			Environment:   environment.Verifier{},
			SourceFetcher: fakeFetcher(bucket),
			Outputs:       bucketsource.OutputMirror{Store: bucket},
		})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewTLSServer(worker.Handler())
		t.Cleanup(server.Close)
		return server
	}

	envelope, err := executor.Seal(executor.Envelope{
		JobID:            "job-bucket-e2e",
		ProjectID:        "bucket-test",
		DefinitionDigest: "sha256:def",
		Instance:         control.Handle{ID: "workspace", Generation: 1},
		SourceDigest:     head,
		StoryPath:        storyRel,
		StoryDigest:      closure.Digest,
		Environment:      envLock,
		Trigger:          map[string]any{"kind": "local", "requested_pipeline": "change"},
		Policy:           executor.Policy{Network: "none", ExternalWrite: "deny"},
	})
	if err != nil {
		t.Fatal(err)
	}

	runOn := func(server *httptest.Server, id string, sink *collectingSink) executor.Result {
		controller := executor.HTTPRemoteWorker{
			Endpoint:   server.URL,
			Client:     server.Client(),
			Credential: func(context.Context) (string, error) { return "test-token", nil },
			Source: executor.SourceBundlerFunc(func(ctx context.Context, _ executor.Envelope) (executor.SourceBundle, error) {
				return executor.GitBundle(ctx, project, head, 0)
			}),
			SourceObjects: publisher,
		}
		prepared := executor.Prepared{ID: id, Envelope: envelope, Placement: "remote", Applied: envelope.Policy}
		result, err := controller.Run(context.Background(), prepared, nil, sink)
		if err != nil {
			t.Fatalf("run %s: %v", id, err)
		}
		return result
	}

	bundleKey := "sources/" + head + "/bundle.git"
	metaKey := "sources/" + head + "/meta.json"

	sink := &collectingSink{}
	result := runOn(newWorker(t.TempDir()), "bucket-e2e-1", sink)
	if result.ExecutionID != "bucket-e2e-1" {
		t.Fatalf("result = %+v", result)
	}
	if got := bucket.putCount(bundleKey); got != 1 {
		t.Fatalf("bundle uploaded %d times, want 1", got)
	}
	if _, err := bucket.Head(context.Background(), metaKey); err != nil {
		t.Fatalf("meta.json not published: %v", err)
	}
	uploads := sink.find("capsule.executor.source.uploading")
	if len(uploads) != 1 || uploads[0].Fields["source_transport"] != "bucket" {
		t.Fatalf("source.uploading events = %+v", uploads)
	}

	// Run record + trace mirrored under runs/<id>/ with terminal status and
	// the outputs.mirrored marker.
	var mirrored workerserver.RunRecord
	readJSONObject(t, bucket, "runs/bucket-e2e-1/run.json", &mirrored)
	if mirrored.Status != "completed" || mirrored.Stage != "terminal" {
		t.Fatalf("mirrored run = %+v", mirrored)
	}
	if !hasEvent(mirrored.Events, "capsule.worker.outputs.mirrored") {
		t.Fatalf("mirrored run missing outputs.mirrored event: %+v", mirrored.Events)
	}
	trace, _, err := bucket.Get(context.Background(), "runs/bucket-e2e-1/story-trace.jsonl")
	if err != nil {
		t.Fatalf("story trace not mirrored: %v", err)
	}
	traceData, _ := io.ReadAll(trace)
	trace.Close()
	if !strings.Contains(string(traceData), "story.turn") {
		t.Fatalf("mirrored trace = %q", traceData)
	}

	// A brand-new worker (empty source cache) sharing the bucket: controller
	// republish is skipped, fetch reference reuses the frozen copy.
	sink2 := &collectingSink{}
	result2 := runOn(newWorker(t.TempDir()), "bucket-e2e-2", sink2)
	if result2.ExecutionID != "bucket-e2e-2" {
		t.Fatalf("result2 = %+v", result2)
	}
	if got := bucket.putCount(bundleKey); got != 1 {
		t.Fatalf("frozen bundle re-uploaded (%d puts), want write-once", got)
	}
	if _, err := bucket.Head(context.Background(), "runs/bucket-e2e-2/run.json"); err != nil {
		t.Fatalf("second run record not mirrored: %v", err)
	}
}

func TestPublisherRejectsForeignMetadata(t *testing.T) {
	store := objectstore.NewFake()
	head := strings.Repeat("a", 40)
	// Publish a sidecar claiming a different head at this key.
	rogue, _ := json.Marshal(bucketsource.SourceObjectMeta{Schema: bucketsource.SourceObjectMetaSchema, Head: strings.Repeat("b", 40), BundleDigest: "sha256:rogue", Size: 3})
	if _, err := store.Put(context.Background(), "sources/"+head+"/meta.json", strings.NewReader(string(rogue)), int64(len(rogue)), objectstore.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	data := []byte("xyz")
	bundle := executor.SourceBundle{Schema: executor.SourceBundleSchema, Format: executor.SourceBundleFormat, Head: head, Digest: sha256Digest(data), Size: 3, Data: data}
	_, err := bucketsource.Publisher{Store: store}.EnsureBundle(context.Background(), bundle)
	if err == nil || !strings.Contains(err.Error(), "names head") {
		t.Fatalf("EnsureBundle error = %v", err)
	}
}

func readJSONObject(t *testing.T, store objectstore.Store, key string, out any) {
	t.Helper()
	rc, _, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	if err := json.NewDecoder(rc).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
}

func hasEvent(events []executor.Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeEnvironment(t *testing.T, project string) string {
	t.Helper()
	rel := filepath.ToSlash(filepath.Join(".kitsoki", "environments", "ci.yaml"))
	write(t, filepath.Join(project, rel), "schema: capsule-environment/v1\nid: ci\nnetwork: none\nsandbox: supervised\n")
	return rel
}

func isolatedTestCapabilities() executor.Capabilities {
	return executor.Capabilities{ID: "capsule-http-worker", Placements: []string{"remote"}, Isolation: "supervised", Networks: []string{"none", "replay"}, Cancellable: true}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

const passingStory = `app:
  id: remote-worker-test
  version: 0.1.0
  title: Remote worker test
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
    view: [{ prose: ready }]
    on:
      run:
        - target: done
          effects:
            - set:
                ci_verdict:
                  schema: capsule-ci-verdict/v1
                  pipeline: "{{ world.ci_pipeline }}"
                  outcome: passed
                  summary: no-LLM remote proof
                  checks:
                    - id: deterministic
                      kind: deterministic
                      outcome: passed
                      evidence: [worker:test]
                  promotion_eligible: true
                  source_digest: "{{ world.ci_source.digest }}"
                  story_digest: "{{ world.ci_trigger.story_digest }}"
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
  done:
    terminal: true
    view: [{ prose: passed }]
`

// TestMirrorTerminalMirrorsArtifactsNestedAndSkipsSymlink covers Phase 3
// artifact durability: every regular file under <runDir>/artifacts/ lands at
// runs/<id>/artifacts/<relative-path>, nested directories included, while
// symlinks are skipped rather than dereferenced.
func TestMirrorTerminalMirrorsArtifactsNestedAndSkipsSymlink(t *testing.T) {
	store := objectstore.NewFake()
	mirror := bucketsource.OutputMirror{Store: store}
	runDir := t.TempDir()
	artifactsDir := filepath.Join(runDir, "artifacts")
	write(t, filepath.Join(artifactsDir, "top.txt"), "top")
	write(t, filepath.Join(artifactsDir, "nested", "deep.txt"), "deep")
	linkPath := filepath.Join(artifactsDir, "link.txt")
	if err := os.Symlink(filepath.Join(artifactsDir, "top.txt"), linkPath); err != nil {
		t.Fatal(err)
	}

	record := workerserver.RunRecord{ExecutionID: "exec-artifacts-1"}
	if err := mirror.MirrorTerminal(context.Background(), record, runDir); err != nil {
		t.Fatalf("MirrorTerminal: %v", err)
	}

	assertObject(t, store, "runs/exec-artifacts-1/artifacts/top.txt", "top")
	assertObject(t, store, "runs/exec-artifacts-1/artifacts/nested/deep.txt", "deep")
	if _, err := store.Head(context.Background(), "runs/exec-artifacts-1/artifacts/link.txt"); err == nil {
		t.Fatalf("symlink should not be mirrored")
	}
}

func TestMirrorTerminalSkipsOversizedArtifact(t *testing.T) {
	store := objectstore.NewFake()
	mirror := bucketsource.OutputMirror{Store: store}
	runDir := t.TempDir()
	artifactsDir := filepath.Join(runDir, "artifacts")
	write(t, filepath.Join(artifactsDir, "small.txt"), "small")
	bigPath := filepath.Join(artifactsDir, "big.bin")
	bigFile, err := os.Create(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate to a sparse file past the cap; no bytes are actually written
	// or read since the size check runs before opening the file for upload.
	if err := bigFile.Truncate(bucketsource.DefaultMaxArtifactBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := bigFile.Close(); err != nil {
		t.Fatal(err)
	}

	record := workerserver.RunRecord{ExecutionID: "exec-artifacts-2"}
	if err := mirror.MirrorTerminal(context.Background(), record, runDir); err != nil {
		t.Fatalf("MirrorTerminal: %v", err)
	}
	assertObject(t, store, "runs/exec-artifacts-2/artifacts/small.txt", "small")
	if _, err := store.Head(context.Background(), "runs/exec-artifacts-2/artifacts/big.bin"); err == nil {
		t.Fatalf("oversized artifact should be skipped, not mirrored")
	}
}

// artifactFailingStore fails Put for one key so tests can prove artifact
// mirroring aggregates errors (via errors.Join) instead of aborting the walk
// partway through.
type artifactFailingStore struct {
	objectstore.Store
	failKey string
}

func (s *artifactFailingStore) Put(ctx context.Context, key string, body io.Reader, size int64, opts objectstore.PutOptions) (objectstore.Meta, error) {
	if key == s.failKey {
		io.Copy(io.Discard, body)
		return objectstore.Meta{}, fmt.Errorf("simulated put failure")
	}
	return s.Store.Put(ctx, key, body, size, opts)
}

func TestMirrorTerminalArtifactPutFailureAggregatesButContinues(t *testing.T) {
	runDir := t.TempDir()
	artifactsDir := filepath.Join(runDir, "artifacts")
	write(t, filepath.Join(artifactsDir, "good.txt"), "good")
	write(t, filepath.Join(artifactsDir, "bad.txt"), "bad")

	failKey := "runs/exec-artifacts-3/artifacts/bad.txt"
	store := &artifactFailingStore{Store: objectstore.NewFake(), failKey: failKey}
	mirror := bucketsource.OutputMirror{Store: store}

	record := workerserver.RunRecord{ExecutionID: "exec-artifacts-3"}
	err := mirror.MirrorTerminal(context.Background(), record, runDir)
	if err == nil || !strings.Contains(err.Error(), "bad.txt") {
		t.Fatalf("MirrorTerminal error = %v, want mention of bad.txt", err)
	}
	assertObject(t, store, "runs/exec-artifacts-3/artifacts/good.txt", "good")
}

// TestMirrorWIPWithoutWorkspaceDirIsANoOp covers a run that failed (or was
// otherwise terminal) before its workspace was ever materialized: MirrorWIP
// must return nil and store nothing, since ExportWIP has no workspace to
// inspect.
func TestMirrorWIPWithoutWorkspaceDirIsANoOp(t *testing.T) {
	store := objectstore.NewFake()
	mirror := bucketsource.OutputMirror{Store: store}
	runDir := t.TempDir() // no "workspace" subdirectory created

	record := workerserver.RunRecord{ExecutionID: "exec-wip-no-workspace", SourceDigest: "sha256:sealed"}
	if err := mirror.MirrorWIP(context.Background(), record, runDir); err != nil {
		t.Fatalf("MirrorWIP: %v", err)
	}
	if _, err := store.Head(context.Background(), "runs/exec-wip-no-workspace/wip/refs.bundle"); err == nil {
		t.Fatalf("expected no bundle stored when the run has no workspace dir")
	}
	if _, err := store.Head(context.Background(), "runs/exec-wip-no-workspace/wip/wip.json"); err == nil {
		t.Fatalf("expected no wip.json stored when the run has no workspace dir")
	}
}

// TestMirrorWIPWithWorkspaceCommitBeyondSealedHeadExports covers the
// durability-critical path end to end through OutputMirror.MirrorWIP: a
// run's workspace has a commit beyond the sealed source head, and MirrorWIP
// (via ExportWIP) publishes runs/<id>/wip/refs.bundle and wip.json.
func TestMirrorWIPWithWorkspaceCommitBeyondSealedHeadExports(t *testing.T) {
	repoDir, sealedHead := initWIPRepo(t)
	write(t, filepath.Join(repoDir, "b.txt"), "b\n")
	wipGit(t, repoDir, "add", "b.txt")
	wipGit(t, repoDir, "commit", "-q", "-m", "second")
	newHead := strings.TrimSpace(wipGit(t, repoDir, "rev-parse", "HEAD"))

	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	if err := os.Rename(repoDir, workspace); err != nil {
		t.Fatal(err)
	}

	store := objectstore.NewFake()
	mirror := bucketsource.OutputMirror{Store: store}
	record := workerserver.RunRecord{ExecutionID: "exec-wip-mirror-1", SourceDigest: sealedHead}
	if err := mirror.MirrorWIP(context.Background(), record, runDir); err != nil {
		t.Fatalf("MirrorWIP: %v", err)
	}

	wantKey := "runs/exec-wip-mirror-1/wip/refs.bundle"
	if _, err := store.Head(context.Background(), wantKey); err != nil {
		t.Fatalf("expected bundle stored at %s: %v", wantKey, err)
	}
	var sidecar bucketsource.WIPExport
	readJSONObject(t, store, "runs/exec-wip-mirror-1/wip/wip.json", &sidecar)
	if sidecar.Head != newHead || sidecar.SealedHead != sealedHead || sidecar.BundleKey != wantKey {
		t.Fatalf("sidecar = %+v, want head=%s sealed=%s key=%s", sidecar, newHead, sealedHead, wantKey)
	}
}

// TestMirrorWIPExportsFromManagedCloneUnderWorkspace covers the whole-loop
// pog-bugfix layout: the sealed top-level <runDir>/workspace stays clean at the
// sealed head while the fix is committed into a managed clone under
// <workspace>/.capsules/workspaces/<id>/ (its own .git, gitignored by the
// top-level checkout). MirrorWIP must export the CLONE's committed work to the
// canonical runs/<id>/wip/refs.bundle — not find the top-level clean and
// mirror nothing (the regression that left runs/<exec>/wip/ empty on a shipped
// worker run). The bundle's first head must be the shipped clone HEAD so the
// POG dispatch recovery (git bundle list-heads NR==1) recovers exactly it.
func TestMirrorWIPExportsFromManagedCloneUnderWorkspace(t *testing.T) {
	// Top-level sealed workspace, left clean at the sealed head, ignoring
	// .capsules/ exactly as real project checkouts do so the nested clone is
	// invisible to the top-level `git status`/`for-each-ref`.
	top, _ := initWIPRepo(t)
	write(t, filepath.Join(top, ".gitignore"), ".capsules/\n")
	wipGit(t, top, "add", ".gitignore")
	wipGit(t, top, "commit", "-q", "-m", "ignore capsules")
	sealedHead := strings.TrimSpace(wipGit(t, top, "rev-parse", "HEAD"))

	// Managed clone (own object store) carrying the fix on an agent/<id>
	// branch — the branch name `agent/…` sorts before `main`, so the shipped
	// commit is `git bundle list-heads`' first entry.
	clone := filepath.Join(top, ".capsules", "workspaces", "cap-1")
	if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "clone", "-q", "--origin", "source", top, clone).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	wipGit(t, clone, "config", "user.name", "Capsule WIP Test")
	wipGit(t, clone, "config", "user.email", "wip@example.invalid")
	wipGit(t, clone, "checkout", "-q", "-b", "agent/cap-1")
	write(t, filepath.Join(clone, "fix.txt"), "fix\n")
	wipGit(t, clone, "add", "fix.txt")
	wipGit(t, clone, "commit", "-q", "-m", "the fix")
	shipped := strings.TrimSpace(wipGit(t, clone, "rev-parse", "HEAD"))

	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	if err := os.Rename(top, workspace); err != nil {
		t.Fatal(err)
	}

	store := objectstore.NewFake()
	mirror := bucketsource.OutputMirror{Store: store}
	record := workerserver.RunRecord{ExecutionID: "exec-wip-clone-1", SourceDigest: sealedHead}
	if err := mirror.MirrorWIP(context.Background(), record, runDir); err != nil {
		t.Fatalf("MirrorWIP: %v", err)
	}

	wantKey := "runs/exec-wip-clone-1/wip/refs.bundle"
	if _, err := store.Head(context.Background(), wantKey); err != nil {
		t.Fatalf("expected bundle stored at %s (managed clone must be exported): %v", wantKey, err)
	}
	var sidecar bucketsource.WIPExport
	readJSONObject(t, store, "runs/exec-wip-clone-1/wip/wip.json", &sidecar)
	if sidecar.Head != shipped {
		t.Fatalf("sidecar.Head = %s, want shipped clone head %s", sidecar.Head, shipped)
	}

	// The recovered bundle must clone back to the shipped commit.
	rc, _, err := store.Get(context.Background(), wantKey)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	defer rc.Close()
	bundleDir := t.TempDir()
	bundlePath := filepath.Join(bundleDir, "refs.bundle")
	out, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.ReadFrom(rc); err != nil {
		t.Fatal(err)
	}
	out.Close()
	listed, err := exec.Command("git", "bundle", "list-heads", bundlePath).CombinedOutput()
	if err != nil {
		t.Fatalf("git bundle list-heads: %v: %s", err, listed)
	}
	firstHead := ""
	if fields := strings.Fields(strings.SplitN(strings.TrimSpace(string(listed)), "\n", 2)[0]); len(fields) > 0 {
		firstHead = fields[0]
	}
	if firstHead != shipped {
		t.Fatalf("bundle first list-heads entry = %s, want shipped %s (POG recovery keys off NR==1)", firstHead, shipped)
	}
}

func assertObject(t *testing.T, store objectstore.Store, key, want string) {
	t.Helper()
	rc, _, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("object %s = %q, want %q", key, data, want)
	}
}
