package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// This file pins P1.7 part 1: the typed ReasonCode enum carried alongside
// RetryReason, the needs_human terminal-for-automation state, and backward
// compatibility with pre-existing free-text retry_reason records. Every
// top-level identifier here is prefixed Nh to avoid collisions with other
// agents' concurrently-added test files in this package.

// TestNhReasonCodeRoundTripsThroughStateJSON pins requirement (1): a
// candidate's ReasonCode, written alongside RetryReason by a real worker
// exhaustion, survives an on-disk JSON round trip byte-for-byte through the
// same Store.write/Store.List path every durable persist uses.
func TestNhReasonCodeRoundTripsThroughStateJSON(t *testing.T) {
	store, first, _ := queuedPair(t)
	// No Repairer configured, so a gate that never goes green exhausts
	// straight to ReasonGateFailed.
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate(), MaxAttempts: 1})
	before := mustGet(t, store, first.ID)
	if before.phase() != NeedsHuman || before.ReasonCode != ReasonGateFailed {
		t.Fatalf("precondition: phase=%s code=%s", before.phase(), before.ReasonCode)
	}

	// Re-read through a fresh Store value (same directory) to force an
	// actual disk read + JSON unmarshal, not just the in-memory value Kick
	// et al. hand back.
	reread := Store{ProjectRoot: store.ProjectRoot}
	state, err := reread.List()
	if err != nil {
		t.Fatal(err)
	}
	after := index(state)[first.ID]
	if after.RetryReason != before.RetryReason {
		t.Fatalf("RetryReason did not round-trip: before=%q after=%q", before.RetryReason, after.RetryReason)
	}
	if after.ReasonCode != ReasonGateFailed {
		t.Fatalf("ReasonCode did not round-trip: got %q, want %q", after.ReasonCode, ReasonGateFailed)
	}

	// And directly against the raw bytes on disk: the enum must serialize
	// as its plain string value (a stable wire format), not some Go-only
	// representation.
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reason_code": "gate-failed"`) {
		t.Fatalf("state.json does not carry the typed reason_code: %s", raw)
	}
}

// TestNhLegacyFreeTextRetryReasonLoadsAsLegacyFreeform pins requirement (3):
// a state.json written before ReasonCode existed — RetryReason set, no
// reason_code key at all — must load cleanly and be classified
// ReasonLegacyFreeform, never left blank and never rejected.
func TestNhLegacyFreeTextRetryReasonLoadsAsLegacyFreeform(t *testing.T) {
	root := t.TempDir()
	store := Store{ProjectRoot: root}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root+"/.capsules/queue", 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-existing v2 state.json: hand-typed prose retry_reason
	// from an interactive session, no reason_code field present at all
	// (exactly what every state.json written before this change looks
	// like).
	legacyJSON := `{
  "schema": "capsule-merge-queue/v2",
  "candidates": [
    {
      "id": "queue-legacy1",
      "project_id": "project",
      "target_ref": "main",
      "target_policy": "wave-auto",
      "sequence": 1,
      "branch": "agent/legacy",
      "sha": "` + strings.Repeat("a", 40) + `",
      "admission": "receipt",
      "receipt_id": "sha256:legacy",
      "backend": "local",
      "position": 1,
      "status": "needs_input",
      "phase": "needs_input",
      "submitted_at": "2026-01-01T00:00:00Z",
      "retry_reason": "had to hand-fix a submodule pin during an interactive session, see chat log"
    }
  ]
}`
	if err := os.WriteFile(path, []byte(legacyJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.List()
	if err != nil {
		t.Fatalf("legacy free-text retry_reason failed to load cleanly: %v", err)
	}
	if len(state.Candidates) != 1 {
		t.Fatalf("candidates=%#v", state.Candidates)
	}
	c := state.Candidates[0]
	if c.phase() != NeedsInput {
		t.Fatalf("legacy status must be preserved as-is, got phase=%s", c.phase())
	}
	if !strings.Contains(c.RetryReason, "hand-fix a submodule pin") {
		t.Fatalf("legacy free-text RetryReason was not preserved: %q", c.RetryReason)
	}
	if c.ReasonCode != ReasonLegacyFreeform {
		t.Fatalf("legacy retry_reason with no code must map to legacy-freeform, got %q", c.ReasonCode)
	}

	// And it stays that way through a second load (compaction/normalize is
	// idempotent on this path too).
	again, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if again.Candidates[0].ReasonCode != ReasonLegacyFreeform {
		t.Fatalf("legacy classification did not survive a second load: %q", again.Candidates[0].ReasonCode)
	}
}

// TestNhNeedsHumanNeverAutoRetriedByWorkerLoop pins requirement (2)'s core
// worker-loop guarantee: once a candidate is needs_human, an arbitrary
// number of further worker passes — even against dependencies that would
// now happily prepare, gate, and land it — must never touch it. Only an
// explicit human verb may move it.
func TestNhNeedsHumanNeverAutoRetriedByWorkerLoop(t *testing.T) {
	store, first, second := queuedPair(t)
	// first is parked to needs_human by a broken resolver harness; second
	// has no such problem and must still land normally past it.
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		if c.ID == first.ID {
			return Speculation{}, Harness(fmt.Errorf("resolver launch path missing app.yaml"))
		}
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	deps := ProcessDeps{Integration: integration, Gate: passingGate{}}
	if _, err := store.Process(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	parked := mustGet(t, store, first.ID)
	if parked.phase() != NeedsHuman {
		t.Fatalf("precondition: %s", parked.phase())
	}
	if mustGet(t, store, second.ID).phase() != Landed {
		t.Fatal("precondition: second candidate should have landed past the parked head")
	}

	// Run the worker loop many more times. The needs_human candidate's
	// WorkerID/LeaseExpiresAt/RetryAt must stay exactly as parkHuman left
	// them (zero/empty), and its phase must never move, no matter how many
	// times RunOnce is called.
	worker := Worker{Store: store, Deps: deps}
	for i := 0; i < 20; i++ {
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	still := mustGet(t, store, first.ID)
	if still.phase() != NeedsHuman {
		t.Fatalf("needs_human candidate was moved by the worker loop: phase=%s", still.phase())
	}
	if still.WorkerID != "" || !still.LeaseExpiresAt.IsZero() || !still.RetryAt.IsZero() {
		t.Fatalf("needs_human candidate was claimed by the worker loop: %#v", still)
	}
	if still.Attempt != parked.Attempt {
		t.Fatalf("needs_human candidate's attempt budget was burned by further worker passes: before=%d after=%d", parked.Attempt, still.Attempt)
	}
}

// TestNhResumeMovesNeedsHumanBackToWorkableQueued pins requirement (2)'s
// resume half: Store.Resume is the explicit human verb that returns a
// needs_human candidate to Queued with a fresh attempt budget, after which
// ordinary worker passes can land it exactly like any other candidate.
func TestNhResumeMovesNeedsHumanBackToWorkableQueued(t *testing.T) {
	store, first, _ := queuedPair(t)
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate(), MaxAttempts: 1})
	before := mustGet(t, store, first.ID)
	if before.phase() != NeedsHuman {
		t.Fatalf("precondition: %s", before.phase())
	}

	resumed, err := store.Resume(Op{ID: first.ID, Actor: "brad", Reason: "fixed the underlying flake"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.phase() != Queued {
		t.Fatalf("resume did not return needs_human to queued: %#v", resumed)
	}
	if resumed.ReasonCode != "" || resumed.NeedsHumanEvidenceRef != "" || resumed.RetryReason != "" {
		t.Fatalf("resume left needs_human classification behind: %#v", resumed)
	}
	if !hasEvidence(resumed, "queue:resume by brad") {
		t.Fatalf("resume was not audited: %v", resumed.Evidence)
	}

	// Now workable: a passing gate lands it like any freshly queued
	// candidate.
	state, err := store.Process(context.Background(), ProcessDeps{Integration: specIntegration(), Gate: passingGate{}})
	if err != nil {
		t.Fatal(err)
	}
	if index(state)[first.ID].phase() != Landed {
		t.Fatalf("resumed needs_human candidate did not land: %#v", state.Candidates)
	}
}

// TestNhQueueStatusRendersNeedsHumanDistinctly pins requirement (4)'s status
// rendering half: needs_human must show up as its own, distinctly countable
// phase in both the summary roll-up (StatusSummary/Summarize, what `queue
// status`'s summary line prints) and the per-candidate StatusLine.
func TestNhQueueStatusRendersNeedsHumanDistinctly(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	state := State{Candidates: []Candidate{
		{ID: "a", Phase: Queued},
		{ID: "b", Phase: NeedsInput, RetryReason: "parked_by_operator", ReasonCode: ReasonOperatorParked, ParkedAt: now.Add(-time.Hour)},
		{ID: "c", Phase: NeedsHuman, RetryReason: "max_attempts_exhausted", ReasonCode: ReasonGateFailed, ParkedAt: now.Add(-2 * time.Hour)},
		{ID: "d", Phase: NeedsHuman, RetryReason: "resolver_harness_failure", ReasonCode: ReasonHarnessFailure, ParkedAt: now.Add(-30 * time.Minute)},
	}}
	summary := Summarize(state, now)

	// needs_human is its own key in the generic phase roll-up — distinct
	// from needs_input — so it is visible without any needs_human-specific
	// code at all.
	if summary.PhaseCounts[NeedsHuman] != 2 || summary.PhaseCounts[NeedsInput] != 1 {
		t.Fatalf("phase counts=%#v", summary.PhaseCounts)
	}
	// And it is also broken out explicitly, at a glance, without requiring
	// a PhaseCounts lookup.
	if summary.NeedsHumanCount != 2 {
		t.Fatalf("needs_human_count=%d, want 2", summary.NeedsHumanCount)
	}
	if summary.ParkedCount != 3 {
		t.Fatalf("parked_count=%d, want 3 (needs_input + both needs_human)", summary.ParkedCount)
	}
	if summary.ReasonCodeCounts[ReasonGateFailed] != 1 || summary.ReasonCodeCounts[ReasonHarnessFailure] != 1 || summary.ReasonCodeCounts[ReasonOperatorParked] != 1 {
		t.Fatalf("reason code counts=%#v", summary.ReasonCodeCounts)
	}

	// The per-candidate StatusLine's next-action hint distinguishes
	// needs_human from every other phase too.
	line := StatusLine(state.Candidates[2], now)
	if !strings.Contains(line, "needs_human") || !strings.Contains(line, "next=human required") {
		t.Fatalf("StatusLine does not distinctly render needs_human: %q", line)
	}

	// Report/JSON: reason_code_counts and needs_human_count are visible
	// keys in the exact payload `queue status --json` emits.
	report := Report(state, now)
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"needs_human_count"`, `"reason_code_counts"`, `"needs_human"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("status JSON missing %s: %s", key, raw)
		}
	}
}
