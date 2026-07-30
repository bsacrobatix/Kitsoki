package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// TestNhEvidenceRefPrefersGateLogOverFinalizationLogOverWorkspacePathOverID
// pins requirement (2)'s evidence pointer: parkHuman must prefer the most
// specific durable pointer available, in a fixed order (gate log,
// finalization log, workspace path, candidate ID), not just "set something
// non-empty". Each sub-case leaves only the fields at-or-after the expected
// winner populated, so a wrong precedence order — not just a blank
// NeedsHumanEvidenceRef — would be caught.
func TestNhEvidenceRefPrefersGateLogOverFinalizationLogOverWorkspacePathOverID(t *testing.T) {
	w := Worker{}
	cases := []struct {
		name string
		c    Candidate
		want string
	}{
		{
			name: "gate log wins over everything else",
			c: Candidate{
				ID:              "cand-1",
				GateLog:         "/logs/gate-1.log",
				FinalizationLog: "/logs/finalize-1.log",
				WorkspacePath:   "/work/cand-1",
			},
			want: "/logs/gate-1.log",
		},
		{
			name: "finalization log wins when gate log is absent",
			c: Candidate{
				ID:              "cand-2",
				FinalizationLog: "/logs/finalize-2.log",
				WorkspacePath:   "/work/cand-2",
			},
			want: "/logs/finalize-2.log",
		},
		{
			name: "workspace path wins when neither log is recorded",
			c: Candidate{
				ID:            "cand-3",
				WorkspacePath: "/work/cand-3",
			},
			want: "/work/cand-3",
		},
		{
			name: "candidate ID is the last-resort fallback",
			c:    Candidate{ID: "cand-4"},
			want: "cand-4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.c
			w.parkHuman(&c, "test_reason", ReasonGateFailed)
			if c.NeedsHumanEvidenceRef != tc.want {
				t.Fatalf("evidence ref=%q, want %q", c.NeedsHumanEvidenceRef, tc.want)
			}
		})
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

// nhSeedRawState writes body verbatim as a project's durable queue state.json,
// bypassing Submit so a test can hand-seed the exact on-disk shape a pre-enum
// record has (no reason_code key at all) in phases Submit cannot produce.
func nhSeedRawState(t *testing.T, body string) (Store, string) {
	t.Helper()
	store := Store{ProjectRoot: t.TempDir()}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return store, path
}

// nhRawCandidates re-reads the durable state.json as untyped JSON objects
// keyed by candidate id, so a test can assert on the PRESENCE or ABSENCE of a
// key rather than on a Go zero value (which cannot distinguish "reason_code
// was persisted as empty" from "reason_code was never written").
func nhRawCandidates(t *testing.T, path string) map[string]map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		Candidates []map[string]json.RawMessage `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("state.json is not loadable as JSON: %v\n%s", err, raw)
	}
	out := map[string]map[string]json.RawMessage{}
	for _, c := range onDisk.Candidates {
		var id string
		if err := json.Unmarshal(c["id"], &id); err != nil {
			t.Fatal(err)
		}
		out[id] = c
	}
	return out
}

// TestNhLegacyFreeformStampIsScopedToRetryWaitAndParkedPhases pins the
// backward-compatibility mapping's SCOPE, which the phase gate in normalize
// exists entirely to enforce and which nothing else in the suite
// distinguishes. RetryReason is deliberately durable history that survives a
// successful claim (claimPreparation clears ReasonCode and Failure but keeps
// RetryReason as "what happened last time") and survives landing, so an
// ungated `if c.RetryReason != "" && c.ReasonCode == ""` stamps
// legacy-freeform onto live and landed records that are not legacy at all.
//
// That mislabel is DURABLE, not cosmetic: normalize runs inside write, so the
// next save of the state file for any unrelated reason persists
// "reason_code": "legacy-freeform" onto an in-flight or landed candidate,
// permanently indistinguishable from a genuine pre-enum record. This test
// therefore asserts twice: on the loaded value, and on the bytes that survive
// an unrelated durable mutation.
func TestNhLegacyFreeformStampIsScopedToRetryWaitAndParkedPhases(t *testing.T) {
	sha := strings.Repeat("a", 40)
	// Every candidate below carries free-text retry_reason and NO
	// reason_code key, exactly like every state.json written before the enum
	// existed. Only the phases where a retry_reason is the candidate's
	// current explanation (retry_wait and the parked family) may be
	// classified; in every other phase it is stale history.
	candidate := func(seq int, id, phase, reason string) string {
		return fmt.Sprintf(`{
      "id": %q,
      "project_id": "project",
      "target_ref": "main",
      "target_policy": "wave-auto",
      "sequence": %d,
      "position": %d,
      "branch": "agent/%s",
      "sha": %q,
      "admission": "receipt",
      "receipt_id": "sha256:legacy",
      "backend": "local",
      "status": %q,
      "phase": %q,
      "submitted_at": "2026-01-01T00:00:00Z",
      "retry_reason": %q
    }`, id, seq, seq, id, sha, phase, phase, reason)
	}
	cases := []struct {
		id    string
		phase string
		// want is the ReasonCode normalize must produce; "" means the record
		// must be left unclassified rather than mislabeled legacy.
		want ReasonCode
	}{
		{id: "landed-rec", phase: "landed", want: ""},
		{id: "preparing-rec", phase: "preparing", want: ""},
		{id: "gating-rec", phase: "gating", want: ""},
		{id: "reprepare-rec", phase: "reprepare", want: ""},
		{id: "retrywait-rec", phase: "retry_wait", want: ReasonLegacyFreeform},
		{id: "needsinput-rec", phase: "needs_input", want: ReasonLegacyFreeform},
		{id: "conflict-rec", phase: "needs_conflict_input", want: ReasonLegacyFreeform},
	}
	bodies := make([]string, 0, len(cases))
	for i, tc := range cases {
		bodies = append(bodies, candidate(i+1, tc.id, tc.phase, "hand-typed prose from an interactive session ("+tc.phase+")"))
	}
	store, path := nhSeedRawState(t, `{
  "schema": "capsule-merge-queue/v2",
  "candidates": [`+strings.Join(bodies, ",")+`]
}`)

	state, err := store.List()
	if err != nil {
		t.Fatalf("legacy state.json failed to load cleanly: %v", err)
	}
	loaded := index(state)
	for _, tc := range cases {
		c, ok := loaded[tc.id]
		if !ok {
			t.Fatalf("candidate %s was dropped on load: %#v", tc.id, state.Candidates)
		}
		// The free-text detail is preserved in every phase either way —
		// ReasonCode is carried alongside RetryReason, never instead of it.
		if !strings.Contains(c.RetryReason, "hand-typed prose") {
			t.Fatalf("%s: free-text RetryReason was not preserved: %q", tc.id, c.RetryReason)
		}
		if c.ReasonCode != tc.want {
			t.Fatalf("%s (phase %s): ReasonCode=%q, want %q — the legacy-freeform stamp must be scoped to retry_wait/parked phases only", tc.id, c.phase(), c.ReasonCode, tc.want)
		}
	}

	// Durability half. Kick is an unrelated operator mutation that goes
	// through the same mutate -> write -> normalize path every durable save
	// uses; it touches only the retry_wait candidate's timer. Afterwards, no
	// non-parked candidate may have acquired a reason_code KEY on disk.
	if _, err := store.Kick(Op{ID: "retrywait-rec", Actor: "brad", Reason: "unrelated kick"}); err != nil {
		t.Fatal(err)
	}
	onDisk := nhRawCandidates(t, path)
	for _, tc := range cases {
		c, ok := onDisk[tc.id]
		if !ok {
			t.Fatalf("candidate %s vanished from state.json after an unrelated write", tc.id)
		}
		raw, present := c["reason_code"]
		if tc.want == "" {
			if present {
				t.Fatalf("%s (phase %s): an unrelated durable write persisted reason_code=%s onto a non-parked record; the mislabel is permanent once written", tc.id, tc.phase, raw)
			}
			continue
		}
		if !present {
			t.Fatalf("%s (phase %s): reason_code was not persisted at all", tc.id, tc.phase)
		}
		var got ReasonCode
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("%s: persisted reason_code=%q, want %q", tc.id, got, tc.want)
		}
	}
}

// TestNhReasonCodeWireValuesAreStable pins every code's on-the-wire string.
// ReasonCode is a DURABLE field in state.json and in `queue status --json`,
// so renaming a constant's value silently reclassifies existing parked
// candidates and breaks any downstream consumer switching on it; that has to
// be a deliberate, test-updating act rather than a refactor side effect. The
// count assertion keeps the set closed: a new code added without a
// deliberate decision fails here.
func TestNhReasonCodeWireValuesAreStable(t *testing.T) {
	want := map[ReasonCode]string{
		ReasonGateFailed:          "gate-failed",
		ReasonMergeConflict:       "merge-conflict",
		ReasonLeaseLost:           "lease-lost",
		ReasonSealMismatch:        "seal-mismatch",
		ReasonResolverFailed:      "resolver-failed",
		ReasonResolverExhausted:   "resolver-exhausted",
		ReasonRepairerExhausted:   "repairer-exhausted",
		ReasonFinalizationFailed:  "finalization-failed",
		ReasonBudgetExhausted:     "budget-exhausted",
		ReasonEnvironmentDegraded: "environment-degraded",
		ReasonHarnessFailure:      "harness-failure",
		ReasonOperatorParked:      "operator-parked",
		ReasonLegacyFreeform:      "legacy-freeform",
	}
	for code, wire := range want {
		if string(code) != wire {
			t.Fatalf("reason code wire value changed: got %q, want %q", string(code), wire)
		}
	}
	if len(want) != 13 {
		t.Fatalf("the closed set changed size (%d); add the new code here deliberately", len(want))
	}
}

// TestNhStageReasonCodeMappingSeparatesRetryingFromExhausted pins the two
// pure mapping functions directly, including the distinction the enum exists
// to make: a stage that is merely retrying must never carry a *-exhausted
// code, because a medic switching on resolver-exhausted would otherwise fire
// on a candidate with attempts still left.
func TestNhStageReasonCodeMappingSeparatesRetryingFromExhausted(t *testing.T) {
	// Still-retrying (retry_wait) mapping. Nothing is exhausted yet, so no
	// branch here may return a *-exhausted code.
	retrying := map[string]ReasonCode{
		"gate_failed":         ReasonGateFailed,
		"finalization_failed": ReasonFinalizationFailed,
		"speculation_failed":  ReasonResolverFailed,
		"something_new":       ReasonBudgetExhausted,
	}
	for stage, want := range retrying {
		if got := reasonCodeForStage(stage); got != want {
			t.Fatalf("reasonCodeForStage(%q)=%q, want %q", stage, got, want)
		}
	}
	for stage := range retrying {
		if got := reasonCodeForStage(stage); got == ReasonResolverExhausted || got == ReasonRepairerExhausted {
			t.Fatalf("reasonCodeForStage(%q) returned the exhausted code %q for a candidate that is only retrying", stage, got)
		}
	}

	// Exhaustion mapping. speculation_failed is the one stage whose code
	// genuinely differs between retrying and exhausted; gate_failed only
	// escalates when a Repairer actually had attempts to burn.
	exhausted := []struct {
		stage       string
		hasRepairer bool
		want        ReasonCode
	}{
		{stage: "gate_failed", hasRepairer: false, want: ReasonGateFailed},
		{stage: "gate_failed", hasRepairer: true, want: ReasonRepairerExhausted},
		{stage: "finalization_failed", hasRepairer: false, want: ReasonFinalizationFailed},
		{stage: "finalization_failed", hasRepairer: true, want: ReasonFinalizationFailed},
		{stage: "speculation_failed", hasRepairer: false, want: ReasonResolverExhausted},
		{stage: "speculation_failed", hasRepairer: true, want: ReasonResolverExhausted},
		{stage: "something_new", hasRepairer: false, want: ReasonBudgetExhausted},
		// Environmental exhaustion deliberately does not route through this
		// function at all: retryOrParkEnv mints the "_environment_degraded"
		// suffix and calls parkHuman with ReasonEnvironmentDegraded itself.
		// Pinned so nobody re-adds a suffix branch here and leaves two
		// competing sources of truth for the environmental code.
		{stage: "gate_failed_environment_degraded", hasRepairer: false, want: ReasonBudgetExhausted},
	}
	for _, tc := range exhausted {
		if got := exhaustionReasonCode(tc.stage, tc.hasRepairer); got != tc.want {
			t.Fatalf("exhaustionReasonCode(%q, hasRepairer=%v)=%q, want %q", tc.stage, tc.hasRepairer, got, tc.want)
		}
	}
}

// nhSingleQueued submits exactly one candidate, so a test that drives the
// worker loop over several passes is not also stepping a second candidate
// through the same failure.
func nhSingleQueued(t *testing.T) (Store, Candidate) {
	t.Helper()
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("c", 40)
	c, err := store.Submit(Submit{Branch: "agent/solo", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	return store, c
}

// TestNhEnvironmentalRetryStampsEnvironmentDegradedNotTheStageCode pins the
// retry (still-within-bound) half of the environmental path. The park half is
// already covered elsewhere; the retry half is where the code is easy to get
// wrong, because retryOrParkEnv receives the same bare stage tag
// ("gate_failed") that retryOrPark does. Routing that tag through
// reasonCodeForStage would stamp gate-failed — a product-failure
// classification — on what is actually a transport/fetch/lock failure that
// merely happened during the gate stage, and an operator triaging by code
// would go hunting for a red gate that never ran.
func TestNhEnvironmentalRetryStampsEnvironmentDegradedNotTheStageCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage string
		deps  func() ProcessDeps
	}{
		{
			name:  "environmental gate failure is not gate-failed",
			stage: "gate_failed",
			deps: func() ProcessDeps {
				return ProcessDeps{Integration: specIntegration(), Gate: gateFunc(func(context.Context, Speculation) (GateResult, error) {
					return GateResult{}, Environmental(fmt.Errorf("gate transport lost"))
				})}
			},
		},
		{
			name:  "environmental speculation failure is not resolver-failed",
			stage: "speculation_failed",
			deps: func() ProcessDeps {
				return ProcessDeps{Gate: passingGate{}, Integration: &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
					return Speculation{}, Environmental(fmt.Errorf("workspace create: lock held"))
				}}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, candidate := nhSingleQueued(t)
			clock := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
			deps := tc.deps()
			// A generous wall-clock bound, so this pass lands squarely in
			// the retry branch and never reaches parkHuman.
			deps.EnvRetryDelay, deps.MaxEnvDuration, deps.Now = 30*time.Second, time.Hour, func() time.Time { return clock }
			worker := Worker{Store: store, Deps: deps}
			if _, err := worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			c := mustGet(t, store, candidate.ID)
			if c.phase() != RetryWait {
				t.Fatalf("phase=%s, want retry_wait (still within the environmental wall-clock bound)", c.phase())
			}
			if c.RetryReason != tc.stage {
				t.Fatalf("RetryReason=%q, want the bare stage tag %q preserved as free-text detail", c.RetryReason, tc.stage)
			}
			if c.ReasonCode != ReasonEnvironmentDegraded {
				t.Fatalf("ReasonCode=%q, want %q: an EnvError-driven retry must not be classified as a product failure", c.ReasonCode, ReasonEnvironmentDegraded)
			}
			// The environmental budget is separate from the product one, so
			// the claim-time increment is rolled back.
			if c.Attempt != 0 {
				t.Fatalf("environmental retry burned the product attempt budget: attempt=%d", c.Attempt)
			}
		})
	}
}

// TestNhSpeculationRetryIsResolverFailedThenResolverExhausted pins the
// resolver-failed / resolver-exhausted split end to end through the real
// worker loop, not just the mapping function: the same stage tag
// ("speculation_failed") must produce the neutral code while attempts remain
// and the exhausted code only at the transition that actually parks.
func TestNhSpeculationRetryIsResolverFailedThenResolverExhausted(t *testing.T) {
	store, candidate := nhSingleQueued(t)
	clock := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	deps := ProcessDeps{
		Gate: passingGate{},
		Integration: &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
			// Non-environmental and with no "continuation" in the message,
			// so this is a plain product-failure speculation error.
			return Speculation{}, fmt.Errorf("resolver returned a dirty tree")
		}},
		MaxAttempts: 3, RetryDelay: time.Minute, MaxRetryDelay: time.Minute,
		Now: func() time.Time { return clock },
	}
	worker := Worker{Store: store, Deps: deps}

	var retryCodes []ReasonCode
	var parked Candidate
	for i := 0; i < 6; i++ {
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		c := mustGet(t, store, candidate.ID)
		if c.phase() == NeedsHuman {
			parked = c
			break
		}
		if c.phase() != RetryWait {
			t.Fatalf("pass %d: phase=%s, want retry_wait or needs_human", i, c.phase())
		}
		retryCodes = append(retryCodes, c.ReasonCode)
		clock = c.RetryAt.Add(time.Second)
	}
	if parked.ID == "" {
		t.Fatal("candidate never exhausted its attempt budget")
	}
	if len(retryCodes) != 2 {
		t.Fatalf("expected 2 retry_wait passes before exhaustion at MaxAttempts=3, got %d (%v)", len(retryCodes), retryCodes)
	}
	for i, code := range retryCodes {
		if code != ReasonResolverFailed {
			t.Fatalf("retry pass %d: ReasonCode=%q, want %q — nothing is exhausted yet, so the code must not say so", i+1, code, ReasonResolverFailed)
		}
	}
	if parked.ReasonCode != ReasonResolverExhausted {
		t.Fatalf("exhaustion ReasonCode=%q, want %q", parked.ReasonCode, ReasonResolverExhausted)
	}
	if parked.RetryReason != "max_attempts_exhausted" {
		t.Fatalf("exhaustion RetryReason=%q, want the free-text detail to still say max_attempts_exhausted", parked.RetryReason)
	}
}

// TestNhFinalizationFailureStampsFinalizationFailedThroughToExhaustion pins
// ReasonFinalizationFailed, which is the one dual-purpose code with no
// escalation: a failing protected compare-and-swap is exactly as specific
// whether attempts remain or not, so the code must be identical in
// retry_wait and needs_human and only the phase may change.
func TestNhFinalizationFailureStampsFinalizationFailedThroughToExhaustion(t *testing.T) {
	store, candidate := nhSingleQueued(t)
	clock := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	deps := ProcessDeps{
		Integration: specIntegration(),
		Gate:        passingGate{},
		Finalizer: finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
			return FinalizeResult{}, fmt.Errorf("protected compare-and-swap rejected")
		}),
		MaxAttempts: 2, RetryDelay: time.Minute, MaxRetryDelay: time.Minute,
		Now: func() time.Time { return clock },
	}
	worker := Worker{Store: store, Deps: deps}

	sawRetryWait := false
	var parked Candidate
	for i := 0; i < 8; i++ {
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		c := mustGet(t, store, candidate.ID)
		switch c.phase() {
		case RetryWait:
			sawRetryWait = true
			if c.ReasonCode != ReasonFinalizationFailed || c.RetryReason != "finalization_failed" {
				t.Fatalf("retry_wait: RetryReason=%q ReasonCode=%q, want finalization_failed/%s", c.RetryReason, c.ReasonCode, ReasonFinalizationFailed)
			}
			clock = c.RetryAt.Add(time.Second)
		case NeedsHuman:
			parked = c
		}
		if parked.ID != "" {
			break
		}
	}
	if !sawRetryWait {
		t.Fatal("never observed the still-retrying finalization failure")
	}
	if parked.ID == "" {
		t.Fatal("finalization failures never exhausted the attempt budget")
	}
	if parked.ReasonCode != ReasonFinalizationFailed {
		t.Fatalf("exhausted ReasonCode=%q, want %q unchanged from the retrying case", parked.ReasonCode, ReasonFinalizationFailed)
	}
	if parked.NeedsHumanEvidenceRef == "" {
		t.Fatal("needs_human park recorded no evidence pointer")
	}
}

// TestNhUnresolvedConflictParkStampsMergeConflictAndParkedAt pins
// failPreparation's conflict-continuation branch. Beyond the code itself this
// park now also stamps ParkedAt/ParkedBy, which it previously left zero — a
// deliberate consequence, since without ParkedAt an aging unresolved conflict
// was invisible to Summarize's OldestParkedAge and could never be classified
// SweepStale. Both halves are pinned so neither can silently regress.
func TestNhUnresolvedConflictParkStampsMergeConflictAndParkedAt(t *testing.T) {
	store, candidate := nhSingleQueued(t)
	at := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	deps := ProcessDeps{
		Gate: passingGate{},
		Integration: &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
			return Speculation{}, fmt.Errorf("conflict markers remain in shared.txt; supply a continuation")
		}},
		Now: func() time.Time { return at },
	}
	if _, err := store.Process(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	c := mustGet(t, store, candidate.ID)
	if c.phase() != NeedsConflictInput {
		t.Fatalf("phase=%s, want needs_conflict_input", c.phase())
	}
	if c.ReasonCode != ReasonMergeConflict {
		t.Fatalf("ReasonCode=%q, want %q", c.ReasonCode, ReasonMergeConflict)
	}
	if c.RetryReason != "merge_conflict_unresolved" {
		t.Fatalf("RetryReason=%q, want merge_conflict_unresolved", c.RetryReason)
	}
	if c.ConflictContinuation == "" {
		t.Fatal("the continuation detail was not retained for the human to act on")
	}
	if !c.ParkedAt.Equal(at) || c.ParkedBy != "queue-worker" {
		t.Fatalf("conflict park did not stamp ParkedAt/ParkedBy: at=%s by=%q", c.ParkedAt, c.ParkedBy)
	}
	// The consequence of stamping ParkedAt: the park is now visible to the
	// age roll-up an operator triages by.
	if got := Summarize(State{Candidates: []Candidate{c}}, at.Add(3*time.Hour)).OldestParkedAge; got != "3h0m0s" {
		t.Fatalf("OldestParkedAge=%q, want the conflict park to age visibly", got)
	}
}

// TestNhOperatorParkReplacesAnAutomationParkCompletely pins Store.Park's two
// obligations on a candidate automation had already given up on: the code
// becomes operator-parked (the code names who/how, never the operator's own
// prose), and the needs_human evidence pointer is CLEARED. Leaving the
// pointer behind would produce a needs_input candidate advertising a
// needs_human evidence ref, with nothing recording that the automation park
// it described had been replaced.
func TestNhOperatorParkReplacesAnAutomationParkCompletely(t *testing.T) {
	store, first, _ := queuedPair(t)
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate(), MaxAttempts: 1})
	automation := mustGet(t, store, first.ID)
	if automation.phase() != NeedsHuman || automation.NeedsHumanEvidenceRef == "" {
		t.Fatalf("precondition: phase=%s evidence_ref=%q", automation.phase(), automation.NeedsHumanEvidenceRef)
	}

	parked, err := store.Park(Op{ID: first.ID, Actor: "brad", Reason: "holding this for the release cut"})
	if err != nil {
		t.Fatal(err)
	}
	if parked.phase() != NeedsInput {
		t.Fatalf("phase=%s, want needs_input", parked.phase())
	}
	if parked.ReasonCode != ReasonOperatorParked {
		t.Fatalf("ReasonCode=%q, want %q", parked.ReasonCode, ReasonOperatorParked)
	}
	if parked.RetryReason != "holding this for the release cut" {
		t.Fatalf("RetryReason=%q, want the operator's own prose kept as free-text detail", parked.RetryReason)
	}
	if parked.NeedsHumanEvidenceRef != "" {
		t.Fatalf("needs_input candidate still carries a needs_human evidence ref: %q", parked.NeedsHumanEvidenceRef)
	}
	if !hasEvidence(parked, "queue:park by brad") {
		t.Fatalf("the replacement was not audited: %v", parked.Evidence)
	}
	// Durable, not just the returned value.
	if reread := mustGet(t, store, first.ID); reread.NeedsHumanEvidenceRef != "" || reread.ReasonCode != ReasonOperatorParked {
		t.Fatalf("durable record disagrees: %#v", reread)
	}
}

// TestNhReclaimedExpiredLeaseStampsLeaseLost pins ReasonLeaseLost, the one
// code attached to a candidate that is being requeued rather than parked. Two
// candidates are needed because the reclaiming pass immediately re-picks one
// of them (claimPreparation clears ReasonCode on the candidate it claims), so
// only the one left behind carries the durable classification — which is
// exactly the record an operator would be reading.
func TestNhReclaimedExpiredLeaseStampsLeaseLost(t *testing.T) {
	store, first, second := queuedPair(t)
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Candidates {
		state.Candidates[i].Phase, state.Candidates[i].Status = Preparing, Preparing
		state.Candidates[i].WorkerID = "crashed"
		state.Candidates[i].LeaseExpiresAt = time.Now().Add(-time.Minute)
	}
	if err := write(path, state); err != nil {
		t.Fatal(err)
	}

	// A speculate that never returns a usable tree keeps this pass from
	// carrying the claimed candidate past preparation, so the assertions
	// below are about the reclaim itself.
	worker := Worker{Store: store, Deps: ProcessDeps{
		Integration: &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
			return Speculation{}, Environmental(fmt.Errorf("still degraded"))
		}},
		Gate: passingGate{},
	}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// first is the better-ordered candidate, so it is the one re-picked.
	left := mustGet(t, store, second.ID)
	if left.phase() != Reprepare {
		t.Fatalf("expired lease was not reclaimed into reprepare: phase=%s", left.phase())
	}
	if left.ReasonCode != ReasonLeaseLost {
		t.Fatalf("reclaimed candidate ReasonCode=%q, want %q", left.ReasonCode, ReasonLeaseLost)
	}
	if !strings.Contains(left.Failure, "lease expired") {
		t.Fatalf("Failure=%q, want the free-text detail to name the lease expiry alongside the code", left.Failure)
	}
	// The reclaimed record is requeued, not parked: lease-lost explains why a
	// reprepare was needed, it is not a human-required terminal state.
	if parked(left.phase()) {
		t.Fatalf("lease loss must requeue, not park: phase=%s", left.phase())
	}
	if reclaimed := mustGet(t, store, first.ID); reclaimed.ReasonCode == ReasonLeaseLost {
		t.Fatalf("the candidate this pass actually claimed should have had its stale code cleared: %#v", reclaimed)
	}
}

// TestNhStatusLineRendersTypedReasonCodeAndEvidenceRef pins the typed code's
// visibility in the DEFAULT human `queue status` output. The enum is the
// deliverable; if it only exists under --json then an operator eyeballing
// status still reads nothing but hand-typed prose, which is the situation
// this work set out to fix. Both new fields are appended, never inserted, so
// every field position an existing reader depends on is unchanged.
func TestNhStatusLineRendersTypedReasonCodeAndEvidenceRef(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	c := Candidate{
		Sequence:              7,
		ID:                    "queue-abc",
		Phase:                 NeedsHuman,
		BaseSHA:               "base1",
		TreeSHA:               "tree1",
		RetryReason:           "max_attempts_exhausted",
		ReasonCode:            ReasonRepairerExhausted,
		NeedsHumanEvidenceRef: "/logs/gate-abc.log",
		GateLog:               "/logs/gate-abc.log",
	}
	line := StatusLine(c, now)
	for _, want := range []string{"needs_human", "next=human required", "reason_code=repairer-exhausted", "needs_human_evidence=/logs/gate-abc.log"} {
		if !strings.Contains(line, want) {
			t.Fatalf("StatusLine is missing %q: %q", want, line)
		}
	}
	// Prefix stability: the pre-existing fields still lead the line in the
	// same order, so the additions are purely additive.
	if !strings.HasPrefix(line, "7 queue-abc needs_human owner= ") {
		t.Fatalf("StatusLine prefix changed shape: %q", line)
	}

	// A candidate with neither field set gains nothing — no empty
	// reason_code= noise on the ordinary lines that make up most of the
	// output.
	plain := StatusLine(Candidate{Sequence: 1, ID: "queue-plain", Phase: Queued}, now)
	if strings.Contains(plain, "reason_code=") || strings.Contains(plain, "needs_human_evidence=") {
		t.Fatalf("unset fields must not be rendered: %q", plain)
	}
}
