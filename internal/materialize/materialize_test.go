package materialize

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/graph"
	"kitsoki/internal/jobs"
)

// copyTestdataFixture copies the whole testdata/ tree (catalog.yaml + the
// fixture story) into a fresh t.TempDir() and returns the copy's root.
// Start()'s write-back (writeback.go) edits the catalog file in place and
// writes an artifact file under RepoRoot/.artifacts/ — a test that drives a
// job to completion must run against a disposable copy of both, not the
// checked-in fixture, or every test run would leave it mutated with the
// previous run's job id/timestamp and a stray .artifacts/ directory.
func copyTestdataFixture(t *testing.T) string {
	t.Helper()
	dstRoot := t.TempDir()
	err := filepath.WalkDir(testdataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(testdataDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644)
	})
	if err != nil {
		t.Fatalf("copy testdata fixture: %v", err)
	}
	return dstRoot
}

func loadTestStoryApp(t *testing.T) *app.AppDef {
	t.Helper()
	def, err := app.Load(testdataDir + "/story/app.yaml")
	if err != nil {
		t.Fatalf("app.Load: %v", err)
	}
	return def
}

const testdataDir = "testdata"
const testCatalogPath = testdataDir + "/catalog.yaml"

func loadTestCatalog(t *testing.T) *graph.Catalog {
	t.Helper()
	cat, err := graph.LoadCatalog(testCatalogPath)
	if err != nil {
		t.Fatalf("graph.LoadCatalog(%s): %v", testCatalogPath, err)
	}
	return cat
}

func TestResolveBinding(t *testing.T) {
	cat := loadTestCatalog(t)

	node := cat.Nodes[graph.NodeID("wi-ready")]
	binding, err := ResolveBinding(cat, node)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	if binding.Story != "story" {
		t.Errorf("Story = %q, want %q", binding.Story, "story")
	}
	if got, want := binding.Gates, []string{"gate", "owner"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Gates = %v, want %v", got, want)
	}
	if len(binding.Params) != 2 || binding.Params[0].ID != "depth" || binding.Params[1].ID != "audience" {
		t.Errorf("Params = %+v, want depth then audience", binding.Params)
	}

	plain := cat.Nodes[graph.NodeID("plain-one")]
	if _, err := ResolveBinding(cat, plain); err == nil {
		t.Error("ResolveBinding on a type with no materialize: binding should error")
	}
}

func TestUnmetGates(t *testing.T) {
	cat := loadTestCatalog(t)

	ready := cat.Nodes[graph.NodeID("wi-ready")]
	if unmet := UnmetGates(ready, []string{"gate", "owner"}); len(unmet) != 0 {
		t.Errorf("UnmetGates(wi-ready) = %v, want none", unmet)
	}

	notReady := cat.Nodes[graph.NodeID("wi-not-ready")]
	unmet := UnmetGates(notReady, []string{"gate", "owner"})
	if got, want := unmet, []string{"gate", "owner"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UnmetGates(wi-not-ready) = %v, want %v", got, want)
	}
}

func TestContextClosure(t *testing.T) {
	cat := loadTestCatalog(t)

	closure := ContextClosure(cat, graph.NodeID("wi-ready"), []graph.EdgeField{"in_phase"})
	got := make([]string, len(closure))
	for i, id := range closure {
		got[i] = string(id)
	}
	sort.Strings(got)
	if want := []string{"phase-one"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ContextClosure = %v, want %v", got, want)
	}

	// Filtering out the only declared edge kind yields an empty closure.
	if empty := ContextClosure(cat, graph.NodeID("wi-ready"), nil); len(empty) != 0 {
		t.Errorf("ContextClosure with no edge kinds = %v, want empty", empty)
	}
}

func TestStart_GateValidation_RejectsWithUnmetList(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	ctx := context.Background()

	_, _, err := Start(ctx, sched, Request{
		CatalogPath: testCatalogPath,
		RepoRoot:    testdataDir,
		NodeID:      graph.NodeID("wi-not-ready"),
	})
	if err == nil {
		t.Fatal("Start on a node with unmet gates should error")
	}
	var gateErr *GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("error = %v, want *GateError", err)
	}
	if got, want := gateErr.Unmet, []string{"gate", "owner"}; !reflect.DeepEqual(got, want) {
		t.Errorf("GateError.Unmet = %v, want %v", got, want)
	}
}

func TestStart_DrivesStoryAndHeartbeats(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	ctx := context.Background()
	fixtureRoot := copyTestdataFixture(t)
	catalogPath := filepath.Join(fixtureRoot, "catalog.yaml")

	jobID, stages, err := Start(ctx, sched, Request{
		CatalogPath: catalogPath,
		RepoRoot:    fixtureRoot,
		NodeID:      graph.NodeID("wi-ready"),
		Params:      map[string]any{"depth": 3, "audience": "public"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if jobID == "" {
		t.Fatal("Start returned empty job id")
	}

	wantStages := []Stage{{ID: "gather", Status: "waiting"}, {ID: "draft", Status: "waiting"}, {ID: "done", Status: "waiting"}}
	if !reflect.DeepEqual(stages, wantStages) {
		t.Errorf("initial stages = %+v, want %+v", stages, wantStages)
	}

	// Collect every stage heartbeat fanned out for this job, in order.
	ch, unsub := sched.Subscribe(jobID)
	defer unsub()

	var events []StageEvent
	timeout := time.After(5 * time.Second)
collect:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break collect
			}
			if se, ok := ev.Progress.(StageEvent); ok {
				events = append(events, se)
			}
			if ev.Status == jobs.JobDone || ev.Status == jobs.JobFailed {
				break collect
			}
		case <-timeout:
			t.Fatal("timed out waiting for job to complete")
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sched.WaitIdle(waitCtx); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}

	job, ok := sched.Get(jobID)
	if !ok {
		t.Fatal("Get: job not found")
	}
	if job.Status != jobs.JobDone {
		t.Fatalf("job status = %v, error = %q, want done", job.Status, job.Error)
	}

	wantEvents := []StageEvent{
		{Stage: "gather", Status: "in-progress"},
		{Stage: "gather", Status: "complete"},
		{Stage: "draft", Status: "in-progress"},
		{Stage: "draft", Status: "complete"},
		{Stage: "done", Status: "in-progress"},
		{Stage: "done", Status: "complete"},
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Errorf("stage events = %+v, want %+v", events, wantEvents)
	}

	if job.Result == nil {
		t.Fatal("job.Result is nil")
	}
	if got := job.Result.Data["artifact_path"]; got != ".artifacts/wi-ready/brief.md" {
		t.Errorf("artifact_path = %v, want %q", got, ".artifacts/wi-ready/brief.md")
	}

	// Write-back (slice 6): the artifact's content actually landed on disk
	// under RepoRoot, and the catalog's node block gained an evidence entry
	// plus a materialization: block reflecting the completed job.
	artifactBytes, err := os.ReadFile(filepath.Join(fixtureRoot, ".artifacts", "wi-ready", "brief.md"))
	if err != nil {
		t.Fatalf("read written artifact: %v", err)
	}
	if len(artifactBytes) == 0 {
		t.Error("written artifact is empty")
	}

	catalogRaw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read written-back catalog: %v", err)
	}
	catalogText := string(catalogRaw)
	for _, want := range []string{
		"    evidence:\n      - kind: doc",
		"path: .artifacts/wi-ready/brief.md",
		"    materialization:\n      job_id: " + `"` + string(jobID) + `"`,
		"status: complete",
		"story: story",
		"- { id: gather, status: complete }",
		"- { id: draft, status: complete }",
		"- { id: done, status: complete }",
	} {
		if !strings.Contains(catalogText, want) {
			t.Errorf("written-back catalog missing %q; got:\n%s", want, catalogText)
		}
	}

	// Round-trips as valid YAML with the node's other fields (id/gate/owner/
	// edges) intact — the splice must not have corrupted the surrounding block.
	rewritten, err := graph.LoadCatalog(catalogPath)
	if err != nil {
		t.Fatalf("write-back produced unparseable catalog: %v", err)
	}
	n := rewritten.Nodes[graph.NodeID("wi-ready")]
	if n == nil {
		t.Fatal("wi-ready missing from write-back catalog")
	}
	if n.Fields["owner"] != "brad" {
		t.Errorf("owner field corrupted by write-back: %v", n.Fields["owner"])
	}
	if len(n.Edges["in_phase"]) != 1 {
		t.Errorf("in_phase edge corrupted by write-back: %v", n.Edges["in_phase"])
	}
}

func TestRoomSequence(t *testing.T) {
	def := loadTestStoryApp(t)
	seq, err := RoomSequence(def)
	if err != nil {
		t.Fatalf("RoomSequence: %v", err)
	}
	if want := []string{"gather", "draft", "done"}; !reflect.DeepEqual(seq, want) {
		t.Errorf("RoomSequence = %v, want %v", seq, want)
	}
}
