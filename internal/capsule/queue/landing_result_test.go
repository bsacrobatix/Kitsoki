package queue

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReconcileLandingProvesExactLinearizedTransition(t *testing.T) {
	root, candidate, linearSHA, oldResult := linearizedLandingFixture(t)
	store := Store{ProjectRoot: root}
	if _, err := store.mutate(func(state *State) (Candidate, error) {
		state.Candidates = append(state.Candidates, candidate)
		return candidate, nil
	}); err != nil {
		t.Fatal(err)
	}
	git(t, root, "update-ref", "-m", "dev-workspace merge queue/speculative/"+candidate.ID+" into staging/local",
		"refs/heads/staging/local", linearSHA, candidate.BaseSHA)

	got, err := store.ReconcileLanding(Op{
		ID: candidate.ID, Actor: "test-operator", Reason: "repair historical helper result",
		Now: time.Date(2026, 7, 26, 13, 50, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultMainSHA != linearSHA {
		t.Fatalf("result_main_sha=%s, want actual protected commit %s", got.ResultMainSHA, linearSHA)
	}
	if !hasEvidence(got, "queue:reconcile-landing by test-operator") ||
		!hasEvidence(got, "old_result="+oldResult) ||
		!hasEvidence(got, "actual_target="+linearSHA) {
		t.Fatalf("missing attributed reconciliation evidence: %v", got.Evidence)
	}
}

func TestReconcileLandingRejectsAmbiguousRefTransition(t *testing.T) {
	root, candidate, linearSHA, _ := linearizedLandingFixture(t)
	store := Store{ProjectRoot: root}
	if _, err := store.mutate(func(state *State) (Candidate, error) {
		state.Candidates = append(state.Candidates, candidate)
		return candidate, nil
	}); err != nil {
		t.Fatal(err)
	}
	// Even an identical tree is insufficient without the exact helper reflog
	// attribution naming this candidate.
	git(t, root, "update-ref", "-m", "unattributed staging rewrite",
		"refs/heads/staging/local", linearSHA, candidate.BaseSHA)

	if _, err := store.ReconcileLanding(Op{ID: candidate.ID, Actor: "test"}); err == nil ||
		!strings.Contains(err.Error(), "does not name candidate") {
		t.Fatalf("err=%v, want strict reflog-attribution refusal", err)
	}
	unchanged, err := store.Get(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ResultMainSHA != candidate.ResultMainSHA {
		t.Fatalf("failed reconciliation mutated result from %s to %s", candidate.ResultMainSHA, unchanged.ResultMainSHA)
	}
}

func TestReconcileLandingRejectsLaterTargetAdvance(t *testing.T) {
	root, candidate, linearSHA, _ := linearizedLandingFixture(t)
	storeCandidate(t, Store{ProjectRoot: root}, candidate)
	git(t, root, "update-ref", "-m", "dev-workspace merge queue/speculative/"+candidate.ID+" into staging/local",
		"refs/heads/staging/local", linearSHA, candidate.BaseSHA)
	git(t, root, "checkout", "staging/local")
	commit(t, root, "later.txt", "later\n", "later staging advance")
	git(t, root, "checkout", "main")

	if _, err := (Store{ProjectRoot: root}).ReconcileLanding(Op{ID: candidate.ID, Actor: "test"}); err == nil ||
		!strings.Contains(err.Error(), "does not name candidate") {
		t.Fatalf("err=%v, want refusal after a later target transition", err)
	}
}

func TestReconcileLandingRejectsMissingReflog(t *testing.T) {
	root, candidate, linearSHA, _ := linearizedLandingFixture(t)
	storeCandidate(t, Store{ProjectRoot: root}, candidate)
	git(t, root, "update-ref", "-m", "dev-workspace merge queue/speculative/"+candidate.ID+" into staging/local",
		"refs/heads/staging/local", linearSHA, candidate.BaseSHA)
	git(t, root, "reflog", "expire", "--expire=now", "--all")

	if _, err := (Store{ProjectRoot: root}).ReconcileLanding(Op{ID: candidate.ID, Actor: "test"}); err == nil ||
		!strings.Contains(err.Error(), "unambiguous latest target transition") {
		t.Fatalf("err=%v, want fail-closed missing-reflog refusal", err)
	}
}

func TestReconcileLandingIsIdempotentAndSerialized(t *testing.T) {
	root, candidate, linearSHA, _ := linearizedLandingFixture(t)
	store := Store{ProjectRoot: root, LockWait: 2 * time.Second}
	storeCandidate(t, store, candidate)
	git(t, root, "update-ref", "-m", "dev-workspace merge queue/speculative/"+candidate.ID+" into staging/local",
		"refs/heads/staging/local", linearSHA, candidate.BaseSHA)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ReconcileLanding(Op{ID: candidate.ID, Actor: "test"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reconcile: %v", err)
		}
	}
	got, err := store.Get(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultMainSHA != linearSHA {
		t.Fatalf("result=%s, want %s", got.ResultMainSHA, linearSHA)
	}
	count := 0
	for _, line := range got.Evidence {
		if strings.Contains(line, "queue:reconcile-landing") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("reconcile evidence count=%d, want exactly one: %v", count, got.Evidence)
	}
	again, err := store.ReconcileLanding(Op{ID: candidate.ID, Actor: "test"})
	if err != nil || again.ResultMainSHA != linearSHA {
		t.Fatalf("idempotent retry result=%#v err=%v", again, err)
	}
}

func TestReconcileLandingRejectsWrongBaseReceiptAndTarget(t *testing.T) {
	t.Run("wrong base", func(t *testing.T) {
		root, candidate, linearSHA, _ := linearizedLandingFixture(t)
		candidate.BaseSHA = linearSHA
		candidate.DependencyFingerprint = preparedFingerprint(candidate)
		store := Store{ProjectRoot: root}
		storeCandidate(t, store, candidate)
		actualBase := git(t, root, "rev-parse", "staging/local")
		git(t, root, "update-ref", "-m", "dev-workspace merge queue/speculative/"+candidate.ID+" into staging/local",
			"refs/heads/staging/local", linearSHA, actualBase)
		if _, err := store.ReconcileLanding(Op{ID: candidate.ID}); err == nil ||
			!strings.Contains(err.Error(), "does not match prepared base") {
			t.Fatalf("err=%v, want wrong-base refusal", err)
		}
	})
	t.Run("substituted receipt", func(t *testing.T) {
		root, candidate, linearSHA, _ := linearizedLandingFixture(t)
		candidate.ReceiptID = persistedReceipt(t, root, candidate.BaseSHA).ReceiptID
		store := Store{ProjectRoot: root}
		storeCandidate(t, store, candidate)
		git(t, root, "update-ref", "-m", "dev-workspace merge queue/speculative/"+candidate.ID+" into staging/local",
			"refs/heads/staging/local", linearSHA, candidate.BaseSHA)
		if _, err := store.ReconcileLanding(Op{ID: candidate.ID}); err == nil ||
			!strings.Contains(err.Error(), "verify source receipt") {
			t.Fatalf("err=%v, want substituted-receipt refusal", err)
		}
	})
	t.Run("substituted target", func(t *testing.T) {
		root, candidate, _, _ := linearizedLandingFixture(t)
		candidate.TargetRef = "main"
		store := Store{ProjectRoot: root}
		storeCandidate(t, store, candidate)
		if _, err := store.ReconcileLanding(Op{ID: candidate.ID}); err == nil ||
			!strings.Contains(err.Error(), "only supports managed staging") {
			t.Fatalf("err=%v, want substituted-target refusal", err)
		}
	})
}

func linearizedLandingFixture(t *testing.T) (root string, candidate Candidate, linearSHA, mergeSHA string) {
	t.Helper()
	root = protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "staging/local")
	commit(t, root, "feature.txt", "feature\n", "feature")
	linearSHA = git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/linearized", linearSHA)
	git(t, root, "reset", "--hard", base)
	git(t, root, "checkout", "-b", "speculative", base)
	git(t, root, "merge", "--no-ff", "--no-edit", linearSHA)
	mergeSHA = git(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "main")
	git(t, root, "reset", "--hard", base)

	candidate = Candidate{
		ID: "queue-linearized", ProjectID: "test", TargetRef: "staging/local",
		TargetBaseSHAAtAdmission: base, TargetPolicy: WaveAutoPolicy,
		Branch: "agent/linearized", SHA: linearSHA, Admission: ReceiptAdmission,
		Status: Landed, Phase: Landed,
		BaseSHA: base, TreeSHA: mergeSHA, ValidatedSHA: mergeSHA,
		ResultMainSHA: mergeSHA, GateVersion: "git diff --check",
	}
	candidate.ReceiptID = persistedReceipt(t, root, linearSHA).ReceiptID
	candidate.DependencyFingerprint = preparedFingerprint(candidate)
	return root, candidate, linearSHA, mergeSHA
}

func storeCandidate(t *testing.T, store Store, candidate Candidate) {
	t.Helper()
	if _, err := store.mutate(func(state *State) (Candidate, error) {
		state.Candidates = append(state.Candidates, candidate)
		return candidate, nil
	}); err != nil {
		t.Fatal(err)
	}
}
