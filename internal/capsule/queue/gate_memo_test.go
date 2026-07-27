package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileGateMemoStoresOnlyPassingResultsAndRoundTrips(t *testing.T) {
	memo := FileGateMemo{ProjectRoot: t.TempDir()}
	tree := strings.Repeat("a", 40)
	config := fingerprint("absent")

	if _, ok := memo.Lookup(tree, "v1", config); ok {
		t.Fatal("empty memo should not have a hit")
	}
	if err := memo.Store(tree, "v1", config, GateResult{Passed: false, Evidence: []string{"gate:red"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := memo.Lookup(tree, "v1", config); ok {
		t.Fatal("a failing result must never be cached: a cache hit must never be able to mask a fix")
	}
	if err := memo.Store(tree, "v1", config, GateResult{Passed: true, Evidence: []string{"gate:green"}, Log: "ok"}); err != nil {
		t.Fatal(err)
	}
	got, ok := memo.Lookup(tree, "v1", config)
	if !ok || !got.Passed || got.Log != "ok" {
		t.Fatalf("expected a cache hit for the passing result, got %#v ok=%v", got, ok)
	}
	// Different gate identity, same tree: must not hit.
	if _, ok := memo.Lookup(tree, "v2", config); ok {
		t.Fatal("a different gate identity must not reuse another gate identity's result")
	}
	// Different tree, same identity: must not hit.
	if _, ok := memo.Lookup(strings.Repeat("b", 40), "v1", config); ok {
		t.Fatal("a different tree must not reuse another tree's result")
	}
	// Different machine-local runtime policy, same tree and gate: must not hit.
	if _, ok := memo.Lookup(tree, "v1", fingerprint("present", "different")); ok {
		t.Fatal("a different runtime config must not reuse another config's result")
	}
	// A memo written by the historical two-field implementation must miss
	// even if its JSON otherwise looks passing.
	legacyKey := strings.TrimPrefix(fingerprint(tree, "legacy/v1"), "sha256:")
	legacyPath := filepath.Join(memo.ProjectRoot, ".capsules", "queue", "gate-memo", legacyKey+".json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"passed":true,"gate_version":"legacy/v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := memo.Lookup(tree, "legacy/v1", config); ok {
		t.Fatal("legacy two-field memo entry must miss without runtime config identity")
	}
}

func TestFileGateMemoUsesExternalQueueAuthority(t *testing.T) {
	project := t.TempDir()
	queueRoot := filepath.Join(t.TempDir(), "queue")
	memo := FileGateMemo{ProjectRoot: project, QueueRoot: queueRoot}
	tree := strings.Repeat("d", 40)
	config := fingerprint("external-authority")
	if err := memo.Store(tree, "external/v1", config, GateResult{Passed: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := memo.Lookup(tree, "external/v1", config); !ok {
		t.Fatal("external queue authority memo did not round trip")
	}
	if _, err := os.Stat(filepath.Join(project, ".capsules", "queue", "gate-memo")); !os.IsNotExist(err) {
		t.Fatalf("external queue memo wrote project-local state: %v", err)
	}
}

// TestGateMemoSkipsARepeatGateRunOnTheIdenticalTree is the worker-level
// proof: a Gate that fails on any call after the first proves the second
// prepare pass over the identical tree never actually invoked it.
func TestGateMemoSkipsARepeatGateRunOnTheIdenticalTree(t *testing.T) {
	sha := strings.Repeat("c", 40)
	store := Store{ProjectRoot: t.TempDir()}
	if _, err := store.Submit(Submit{Branch: "agent/memo", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	gate := gateFunc(func(context.Context, Speculation) (GateResult, error) {
		calls++
		if calls > 1 {
			return GateResult{Passed: false}, nil
		}
		return GateResult{Passed: true, Evidence: []string{"gate:green"}}, nil
	})
	memo := FileGateMemo{ProjectRoot: t.TempDir()}
	deps := ProcessDeps{
		Integration: &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
			// The identical tree every time: this is the case a stale-base
			// Reprepare with an unchanged classification produces.
			return Speculation{SHA: "tree-" + c.SHA, RuntimeConfigDigest: fingerprint("absent")}, nil
		}},
		Gate: gate, GateVersion: "test/v1", GateMemo: memo,
	}
	worker := Worker{Store: store, Deps: deps}

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := mustGet(t, store, candidateIDFor(t, store, sha))
	if first.phase() != ReadyToFinalize {
		t.Fatalf("first prepare: phase=%s (want ready_to_finalize)", first.phase())
	}
	if calls != 1 {
		t.Fatalf("gate calls=%d after first prepare, want 1", calls)
	}

	// Force a reprepare of the identical tree (as a stale-base Reprepare
	// would): the candidate goes back through Preparing/Gating with the
	// exact same fakeIntegration, so it produces the exact same tree SHA.
	forceReprepare(t, store, first.ID)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := mustGet(t, store, first.ID)
	if second.phase() != ReadyToFinalize {
		t.Fatalf("second prepare: phase=%s (want ready_to_finalize)", second.phase())
	}
	if calls != 1 {
		t.Fatalf("gate calls=%d after second prepare on the identical tree, want still 1 (memo should have skipped it)", calls)
	}
	memoEvidenced := false
	for _, e := range second.Evidence {
		if strings.Contains(e, "queue:gate-reused=memo") {
			memoEvidenced = true
		}
	}
	if !memoEvidenced {
		t.Fatalf("expected memo-reuse evidence, got %v", second.Evidence)
	}
}

func candidateIDFor(t *testing.T, store Store, sha string) string {
	t.Helper()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range state.Candidates {
		if c.SHA == sha {
			return c.ID
		}
	}
	t.Fatalf("no candidate for sha %s", sha)
	return ""
}

// forceReprepare directly rewinds a ready_to_finalize candidate back to
// Reprepare, standing in for what a stale-base finalize detection does —
// this test is about the gate memo, not about reproducing that detection.
func forceReprepare(t *testing.T, store Store, id string) {
	t.Helper()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Candidates {
		if state.Candidates[i].ID == id {
			state.Candidates[i].Phase, state.Candidates[i].Status = Reprepare, Reprepare
			state.Candidates[i].WorkerID, state.Candidates[i].LeaseExpiresAt = "", time.Time{}
		}
	}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := write(path, state); err != nil {
		t.Fatal(err)
	}
}
