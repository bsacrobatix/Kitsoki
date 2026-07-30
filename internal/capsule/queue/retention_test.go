package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// retentionCandidate builds a minimal, schema-valid Candidate for retention
// tests. It deliberately bypasses Store.Submit/receipt validation (as the
// package's other direct-write tests do — see queue_test.go's use of
// write(path, state)) since these tests exercise pure state-machine/
// compaction semantics, not receipt admission.
func retentionCandidate(id string, seq uint64, status Status, completed time.Time) Candidate {
	return Candidate{
		ID:                 id,
		ProjectID:          "project",
		TargetRef:          "main",
		TargetPolicy:       WaveAutoPolicy,
		Sequence:           seq,
		Position:           int(seq),
		Branch:             "agent/" + id,
		SHA:                fmt.Sprintf("%040d", seq),
		Status:             status,
		Phase:              status,
		Submitted:          completed,
		Completed:          completed,
		Admission:          ReceiptAdmission,
		FinalizationPolicy: AutonomousFinalization,
	}
}

// TestCompactTerminalHistoryPrunesOldestPastCap proves (a): terminal
// (landed/rejected) records beyond the cap are pruned, and the *oldest* ones
// go first — the most recently completed candidates of each status survive.
func TestCompactTerminalHistoryPrunesOldestPastCap(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = 3
	var candidates []Candidate
	// 8 Landed and 8 Rejected, completed at strictly increasing times so
	// "oldest" and "newest" are unambiguous per status.
	for i := 0; i < 8; i++ {
		seq := uint64(i + 1)
		candidates = append(candidates, retentionCandidate(fmt.Sprintf("landed-%d", i), seq, Landed, base.Add(time.Duration(i)*time.Hour)))
		candidates = append(candidates, retentionCandidate(fmt.Sprintf("rejected-%d", i), seq+100, Rejected, base.Add(time.Duration(i)*time.Hour)))
	}
	state := State{Schema: Schema, Candidates: candidates}

	out := compactTerminalHistory(state, limit)

	var landed, rejected []Candidate
	for _, c := range out.Candidates {
		switch c.Status {
		case Landed:
			landed = append(landed, c)
		case Rejected:
			rejected = append(rejected, c)
		}
	}
	if len(landed) != limit {
		t.Fatalf("landed count=%d, want %d: %#v", len(landed), limit, landed)
	}
	if len(rejected) != limit {
		t.Fatalf("rejected count=%d, want %d: %#v", len(rejected), limit, rejected)
	}
	// The survivors must be the most recent `limit` of each status (indices
	// 5,6,7 out of 0..7) — the oldest ones (0..4) must be gone.
	wantLanded := map[string]bool{"landed-5": true, "landed-6": true, "landed-7": true}
	for _, c := range landed {
		if !wantLanded[c.ID] {
			t.Fatalf("unexpected surviving landed candidate %s (want only the 3 most recent); got %#v", c.ID, landed)
		}
	}
	wantRejected := map[string]bool{"rejected-5": true, "rejected-6": true, "rejected-7": true}
	for _, c := range rejected {
		if !wantRejected[c.ID] {
			t.Fatalf("unexpected surviving rejected candidate %s (want only the 3 most recent); got %#v", c.ID, rejected)
		}
	}
	for i := 0; i < 5; i++ {
		for _, id := range []string{fmt.Sprintf("landed-%d", i), fmt.Sprintf("rejected-%d", i)} {
			for _, c := range out.Candidates {
				if c.ID == id {
					t.Fatalf("old terminal candidate %s should have been pruned past the cap", id)
				}
			}
		}
	}
}

// TestCompactTerminalHistoryNeverPrunesParkedOrPending proves (b): parked
// (needs_input / needs_conflict_input) and still-active ("pending") phases
// are retained unconditionally, however small the terminal-history cap is
// and however many terminal records also need pruning.
func TestCompactTerminalHistoryNeverPrunesParkedOrPending(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = 2
	var candidates []Candidate

	// Far more terminal candidates than the cap, across both statuses.
	for i := 0; i < 20; i++ {
		candidates = append(candidates, retentionCandidate(fmt.Sprintf("landed-%d", i), uint64(i+1), Landed, base.Add(time.Duration(i)*time.Minute)))
		candidates = append(candidates, retentionCandidate(fmt.Sprintf("rejected-%d", i), uint64(i+201), Rejected, base.Add(time.Duration(i)*time.Minute)))
	}
	// Parked and every non-terminal ("pending") phase, none of them with a
	// Completed time (they haven't finished).
	pendingStatuses := []Status{NeedsInput, NeedsConflictInput, Queued, Preparing, Gating, AwaitingApproval, ReadyToFinalize, RetryWait, Reprepare}
	var wantAlwaysKept []string
	for i, st := range pendingStatuses {
		id := fmt.Sprintf("pending-%d-%s", i, st)
		candidates = append(candidates, retentionCandidate(id, uint64(i+1001), st, time.Time{}))
		wantAlwaysKept = append(wantAlwaysKept, id)
	}
	state := State{Schema: Schema, Candidates: candidates}

	out := compactTerminalHistory(state, limit)

	kept := map[string]bool{}
	for _, c := range out.Candidates {
		kept[c.ID] = true
	}
	for _, id := range wantAlwaysKept {
		if !kept[id] {
			t.Fatalf("parked/pending candidate %s was pruned; retention must never touch non-terminal candidates", id)
		}
	}
	// And the terminal side did actually get capped.
	var landedCount, rejectedCount int
	for _, c := range out.Candidates {
		switch c.Status {
		case Landed:
			landedCount++
		case Rejected:
			rejectedCount++
		}
	}
	if landedCount != limit || rejectedCount != limit {
		t.Fatalf("landed=%d rejected=%d, want %d each", landedCount, rejectedCount, limit)
	}
	if len(out.Candidates) != limit*2+len(pendingStatuses) {
		t.Fatalf("total candidates=%d, want %d", len(out.Candidates), limit*2+len(pendingStatuses))
	}
}

// TestTerminalRecencyFallsBackToSequenceWhenCompletedZero covers the one
// path that retires a candidate as Rejected without stamping Completed — a
// fresh resubmission superseding a still-active prior candidate of the same
// SHA (Store.Submit). Retention must still have a deterministic newest-first
// order in that case instead of treating every such record as equally
// "newest" (which would make pruning arbitrary).
func TestTerminalRecencyFallsBackToSequenceWhenCompletedZero(t *testing.T) {
	older := retentionCandidate("old", 1, Rejected, time.Time{})
	newer := retentionCandidate("new", 2, Rejected, time.Time{})
	if terminalRecency(newer) <= terminalRecency(older) {
		t.Fatalf("expected higher-sequence candidate to rank newer: old=%d new=%d", terminalRecency(older), terminalRecency(newer))
	}
	out := compactTerminalHistory(State{Schema: Schema, Candidates: []Candidate{older, newer}}, 1)
	if len(out.Candidates) != 1 || out.Candidates[0].ID != "new" {
		t.Fatalf("candidates=%#v, want only the higher-sequence (newer) survivor", out.Candidates)
	}
}

// TestStoreCompactionRunsAutomaticallyOnLoad proves that retention is not an
// operator command: a state.json that already grew past the cap (as would
// happen to any file written before this bound existed, or by an older
// binary) is compacted the moment anything next loads it through Store —
// here Store.List, standing in for what a worker calls at the start of a
// cycle — and (c) the compacted state remains loadable afterward, both via
// the Store API and by re-parsing the on-disk file directly.
func TestStoreCompactionRunsAutomaticallyOnLoad(t *testing.T) {
	root := t.TempDir()
	store := Store{ProjectRoot: root}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = DefaultTerminalHistoryLimit
	var candidates []Candidate
	for i := 0; i < limit+40; i++ {
		candidates = append(candidates, retentionCandidate(fmt.Sprintf("landed-%d", i), uint64(i+1), Landed, base.Add(time.Duration(i)*time.Minute)))
	}
	candidates = append(candidates, retentionCandidate("parked-1", uint64(len(candidates)+1), NeedsInput, time.Time{}))
	candidates = append(candidates, retentionCandidate("pending-1", uint64(len(candidates)+2), Queued, time.Time{}))
	seeded := State{Schema: Schema, Candidates: candidates}

	// Write the oversized file directly with plain json.Marshal, bypassing
	// this package's own write() (which would compact on the way in) — this
	// simulates a state.json that already grew unbounded before automatic
	// retention existed.
	raw, err := json.MarshalIndent(seeded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	// No prune/compact command exists or is called here — Store.List (a
	// plain read) is what triggers compaction.
	got, err := store.List()
	if err != nil {
		t.Fatalf("List after oversized seed: %v", err)
	}
	var landedCount int
	var sawParked, sawPending bool
	for _, c := range got.Candidates {
		switch {
		case c.Status == Landed:
			landedCount++
		case c.ID == "parked-1":
			sawParked = true
		case c.ID == "pending-1":
			sawPending = true
		}
	}
	if landedCount != limit {
		t.Fatalf("landed count=%d after auto-compaction, want cap %d", landedCount, limit)
	}
	if !sawParked {
		t.Fatal("parked candidate was pruned by compaction; parked candidates must always survive")
	}
	if !sawPending {
		t.Fatal("pending (queued) candidate was pruned by compaction; active candidates must always survive")
	}

	// (c) the compacted file itself is still valid, loadable JSON on disk —
	// not just correct in memory — and a second load is stable (idempotent).
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reparsed State
	if err := json.Unmarshal(onDisk, &reparsed); err != nil {
		t.Fatalf("state.json is not valid JSON after compaction: %v", err)
	}
	if len(reparsed.Candidates) != limit+2 {
		t.Fatalf("on-disk candidates=%d, want %d (cap + parked + pending)", len(reparsed.Candidates), limit+2)
	}

	again, err := store.List()
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(again.Candidates) != len(got.Candidates) {
		t.Fatalf("compaction was not idempotent: first=%d second=%d", len(got.Candidates), len(again.Candidates))
	}
}

// TestStoreSubmitCompactsOnSave proves compaction also runs on the ordinary
// mutation path (Submit -> write), not only when an already-oversized file
// happens to be loaded: a queue that keeps landing/rejecting candidates
// never lets state.json's terminal history grow past the cap in the first
// place.
func TestStoreSubmitCompactsOnSave(t *testing.T) {
	root := t.TempDir()
	store := Store{ProjectRoot: root}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = DefaultTerminalHistoryLimit
	var candidates []Candidate
	for i := 0; i < limit+10; i++ {
		candidates = append(candidates, retentionCandidate(fmt.Sprintf("rejected-%d", i), uint64(i+1), Rejected, base.Add(time.Duration(i)*time.Minute)))
	}
	// write() itself compacts on every save, so seed through it directly
	// (rather than the raw-json bypass above) to prove the save path, not
	// just the load path.
	if err := write(path, State{Schema: Schema, Candidates: candidates}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seeded State
	if err := json.Unmarshal(raw, &seeded); err != nil {
		t.Fatal(err)
	}
	var rejectedCount int
	for _, c := range seeded.Candidates {
		if c.Status == Rejected {
			rejectedCount++
		}
	}
	if rejectedCount != limit {
		t.Fatalf("rejected count immediately after write()=%d, want cap %d (compaction must run on every save)", rejectedCount, limit)
	}

	// A subsequent, ordinary Submit (a real mutate/write cycle) must not
	// regress this: the file stays at or under the cap plus the one new
	// candidate, and remains loadable.
	sha := fmt.Sprintf("%040d", limit+999)
	if _, err := store.Submit(Submit{Branch: "agent/new", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	final, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	rejectedCount = 0
	for _, c := range final.Candidates {
		if c.Status == Rejected {
			rejectedCount++
		}
	}
	if rejectedCount != limit {
		t.Fatalf("rejected count after Submit=%d, want cap %d", rejectedCount, limit)
	}
}

// retentionLanding builds a Landed candidate on an explicit target ref with
// the receipt-bearing landed tuple the durable consumers of Landed records
// require: PromoteExistingAuthority.sourceLanding matches on
// TargetRef + phase + ResultMainSHA + a non-empty ReceiptID, and
// Store.ReconcileLanding refuses a candidate missing any of them.
func retentionLanding(id, targetRef string, seq uint64, completed time.Time) Candidate {
	c := retentionCandidate(id, seq, Landed, completed)
	c.TargetRef = targetRef
	c.ReceiptID = "sha256:receipt-" + id
	c.BaseSHA = fmt.Sprintf("%040d", seq+900000)
	c.TreeSHA = fmt.Sprintf("%040d", seq+800000)
	c.ValidatedSHA = c.TreeSHA
	c.ResultMainSHA = fmt.Sprintf("%040d", seq+700000)
	return c
}

// sourceLandingResolves replays the exact predicate
// PromoteExistingAuthority.sourceLanding scans state.json with
// (internal/capsule/queue/promote_existing.go) so these tests fail if
// retention ever prunes a record that promotion still needs.
func sourceLandingResolves(state State, sourceTarget, landedSHA string) bool {
	for _, c := range state.Candidates {
		if c.TargetRef == sourceTarget && c.phase() == Landed && c.ResultMainSHA == landedSHA && c.ReceiptID != "" {
			return true
		}
	}
	return false
}

// TestCompactTerminalHistoryBudgetsPerTargetRef proves the terminal-history
// cap is per target ref, not one shared budget across every target: a flood
// of landings on the busy target must never evict the quiet target's
// landings. That is the promotion path of the integration train — the
// staging landing is the source record `capsule promote-existing` proves the
// staging->main promotion against, and staging landings are vastly
// outnumbered by main landings in a real queue (a 2026-07 production
// state.json held 201 landed candidates, every one of them on main).
func TestCompactTerminalHistoryBudgetsPerTargetRef(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = 5
	var candidates []Candidate
	// Three staging landings first, so they are the *oldest* terminal
	// records in the state and would be the first evicted under a single
	// shared per-status budget.
	for i := 0; i < 3; i++ {
		candidates = append(candidates, retentionLanding(fmt.Sprintf("staging-%d", i), "staging/local", uint64(i+1), base.Add(time.Duration(i)*time.Minute)))
	}
	for i := 0; i < 4*limit; i++ {
		candidates = append(candidates, retentionLanding(fmt.Sprintf("main-%d", i), "main", uint64(i+100), base.Add(time.Duration(i+10)*time.Minute)))
	}
	state := State{Schema: Schema, Candidates: candidates}

	out := compactTerminalHistory(state, limit)

	byTarget := map[string]int{}
	kept := map[string]bool{}
	for _, c := range out.Candidates {
		byTarget[c.TargetRef]++
		kept[c.ID] = true
	}
	if byTarget["main"] != limit {
		t.Fatalf("main landings kept=%d, want the cap %d", byTarget["main"], limit)
	}
	if byTarget["staging/local"] != 3 {
		t.Fatalf("staging/local landings kept=%d, want all 3 (a busy target must not consume the quiet target's budget)", byTarget["staging/local"])
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("staging-%d", i)
		if !kept[id] {
			t.Fatalf("staging landing %s was evicted by a flood of main landings", id)
		}
	}
	// And the record is not merely present but still *resolvable* by the
	// promotion authority's own lookup.
	for i := 0; i < 3; i++ {
		landedSHA := fmt.Sprintf("%040d", uint64(i+1)+700000)
		if !sourceLandingResolves(out, "staging/local", landedSHA) {
			t.Fatalf("promote-existing source landing for staging/local@%s no longer resolves after compaction", landedSHA)
		}
	}
}

// TestCompactTerminalHistoryKeepsReconcileLandingCandidate covers
// Store.ReconcileLanding, which repairs a *historical* managed staging
// finalization and requires its candidate to still be present and Landed with
// a complete receipt-bearing tuple. It only ever addresses "staging/local",
// so a main-target flood must not make the repair unreachable.
func TestCompactTerminalHistoryKeepsReconcileLandingCandidate(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = 4
	target := retentionLanding("reconcile-target", "staging/local", 1, base)
	candidates := []Candidate{target}
	for i := 0; i < 10*limit; i++ {
		candidates = append(candidates, retentionLanding(fmt.Sprintf("main-%d", i), "main", uint64(i+100), base.Add(time.Duration(i+1)*time.Minute)))
	}

	out := compactTerminalHistory(State{Schema: Schema, Candidates: candidates}, limit)

	var got *Candidate
	for i := range out.Candidates {
		if out.Candidates[i].ID == target.ID {
			got = &out.Candidates[i]
		}
	}
	if got == nil {
		t.Fatal("reconcile-landing target was pruned; `queue reconcile-landing` would become unresolvable")
	}
	// Replay ReconcileLanding's own completeness precondition (ops.go).
	if got.phase() != Landed || got.ReceiptID == "" || got.BaseSHA == "" || got.TreeSHA == "" || got.ValidatedSHA != got.TreeSHA || got.ResultMainSHA == "" {
		t.Fatalf("surviving reconcile-landing target lost its receipt-bearing landed tuple: %#v", *got)
	}
}

// TestCompactTerminalHistoryPinsLandedTwinOfParkedCandidate proves the parked
// pin end to end through the real Store: Sweep classifies a parked candidate
// as SweepSuperseded — the only classification ApplySweep acts on — solely
// because another candidate for the identical SHA is still present as Landed.
// Parked candidates are never pruned and can sit for months, so pruning their
// landed twin would silently downgrade them to "unclassified" and stop them
// being auto-rejected.
func TestCompactTerminalHistoryPinsLandedTwinOfParkedCandidate(t *testing.T) {
	root := t.TempDir()
	store := Store{ProjectRoot: root}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = DefaultTerminalHistoryLimit
	duplicateSHA := fmt.Sprintf("%040d", 424242)

	twin := retentionLanding("landed-twin", "main", 1, base)
	twin.SHA = duplicateSHA
	parked := retentionCandidate("parked-duplicate", 2, NeedsInput, time.Time{})
	parked.SHA = duplicateSHA
	parked.ParkedAt = base.Add(time.Hour)

	candidates := []Candidate{twin, parked}
	// Far more than the cap of newer landings on the same target, so the
	// twin is well past the count budget and survives only because it is
	// pinned.
	for i := 0; i < limit+25; i++ {
		candidates = append(candidates, retentionLanding(fmt.Sprintf("main-%d", i), "main", uint64(i+100), base.Add(time.Duration(i+2)*time.Hour)))
	}
	if err := write(path, State{Schema: Schema, Candidates: candidates}); err != nil {
		t.Fatal(err)
	}

	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var sawTwin bool
	for _, c := range state.Candidates {
		if c.ID == twin.ID {
			sawTwin = true
		}
	}
	if !sawTwin {
		t.Fatal("landed twin of a parked candidate was pruned; Sweep's superseded classification depends on it")
	}

	plan, err := store.Sweep(base.Add(48*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	var entry *SweepEntry
	for i := range plan.Entries {
		if plan.Entries[i].Candidate.ID == parked.ID {
			entry = &plan.Entries[i]
		}
	}
	if entry == nil {
		t.Fatalf("parked candidate missing from sweep plan: %#v", plan.Entries)
	}
	if entry.Classification != SweepSuperseded {
		t.Fatalf("classification=%q, want %q (the landed twin must survive retention)", entry.Classification, SweepSuperseded)
	}
	if entry.ProposedAction != "reject" {
		t.Fatalf("proposed action=%q, want reject", entry.ProposedAction)
	}
}

// TestCompactTerminalHistoryPinsPromotionSourceLanding proves the in-flight
// promotion pin: once `capsule promote-existing` has admitted the destination
// candidate, that candidate's SHA *is* the source target's landed result, and
// a restart re-verifies the source landing. Retention must therefore keep the
// source landing for as long as the destination candidate is live, even when
// the source target itself has since landed far more than the cap.
func TestCompactTerminalHistoryPinsPromotionSourceLanding(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const limit = 3
	source := retentionLanding("promotion-source", "staging/local", 1, base)
	destination := retentionCandidate("promotion-destination", 2, Queued, time.Time{})
	destination.TargetRef = "main"
	destination.SHA = source.ResultMainSHA

	candidates := []Candidate{source, destination}
	for i := 0; i < 10*limit; i++ {
		candidates = append(candidates, retentionLanding(fmt.Sprintf("staging-%d", i), "staging/local", uint64(i+100), base.Add(time.Duration(i+1)*time.Minute)))
	}

	out := compactTerminalHistory(State{Schema: Schema, Candidates: candidates}, limit)

	if !sourceLandingResolves(out, "staging/local", source.ResultMainSHA) {
		t.Fatal("source landing of an in-flight promotion was pruned; a resumed promote-existing would fail source_landing_unproven")
	}
	// The pin is scoped: it survives because the destination candidate is
	// live, not because retention stopped pruning. Once that candidate is
	// terminal, the ordinary cap applies again.
	for i := range candidates {
		if candidates[i].ID == destination.ID {
			candidates[i].Status, candidates[i].Phase = Landed, Landed
			candidates[i].Completed = base.Add(24 * time.Hour)
		}
	}
	after := compactTerminalHistory(State{Schema: Schema, Candidates: candidates}, limit)
	if sourceLandingResolves(after, "staging/local", source.ResultMainSHA) {
		t.Fatal("source landing survived past the cap with no live candidate depending on it; the pin must be scoped to in-flight promotions")
	}
}
