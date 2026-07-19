package queue

import (
	"strings"
	"testing"
	"time"
)

// setParked writes id's phase/status to needs_input, with the given
// RetryReason and ParkedAt, directly into durable state — standing in for
// whatever originally parked it (this file is about Sweep's
// classification, not about reproducing every park path).
func setParked(t *testing.T, store Store, id, retryReason string, parkedAt time.Time) {
	t.Helper()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range state.Candidates {
		if state.Candidates[i].ID == id {
			state.Candidates[i].Phase, state.Candidates[i].Status = NeedsInput, NeedsInput
			state.Candidates[i].RetryReason = retryReason
			state.Candidates[i].ParkedAt = parkedAt
			found = true
		}
	}
	if !found {
		t.Fatalf("candidate %s not found", id)
	}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := write(path, state); err != nil {
		t.Fatal(err)
	}
}

// setLanded marks id Landed with the given SHA — standing in for a real
// finalize, since Sweep's superseded check only looks at Phase and SHA.
func setLanded(t *testing.T, store Store, id, sha string) {
	t.Helper()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range state.Candidates {
		if state.Candidates[i].ID == id {
			state.Candidates[i].Phase, state.Candidates[i].Status = Landed, Landed
			state.Candidates[i].SHA = sha
			found = true
		}
	}
	if !found {
		t.Fatalf("candidate %s not found", id)
	}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := write(path, state); err != nil {
		t.Fatal(err)
	}
}

func TestSweepClassifiesSupersededEnvironmentDegradedStaleAndUnclassified(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)

	dupSHA := strings.Repeat("d", 40)
	landedTwin, err := store.Submit(Submit{Branch: "agent/landed-twin", SHA: dupSHA, Receipt: testReceipt(t, dupSHA)})
	if err != nil {
		t.Fatal(err)
	}
	setLanded(t, store, landedTwin.ID, dupSHA)

	superseded, err := store.Submit(Submit{Branch: "agent/superseded", SHA: dupSHA, Admission: EmergencySkipTestsAdmission})
	if err != nil {
		t.Fatal(err)
	}
	setParked(t, store, superseded.ID, "gate_failed", now.Add(-time.Hour))

	envSHA := strings.Repeat("e", 40)
	envDegraded, err := store.Submit(Submit{Branch: "agent/env", SHA: envSHA, Receipt: testReceipt(t, envSHA)})
	if err != nil {
		t.Fatal(err)
	}
	setParked(t, store, envDegraded.ID, "speculation_failed_environment_degraded", now.Add(-time.Hour))

	staleSHA := strings.Repeat("f", 40)
	stale, err := store.Submit(Submit{Branch: "agent/stale", SHA: staleSHA, Receipt: testReceipt(t, staleSHA)})
	if err != nil {
		t.Fatal(err)
	}
	setParked(t, store, stale.ID, "max_attempts_exhausted", now.Add(-10*24*time.Hour))

	freshSHA := strings.Repeat("1", 40)
	fresh, err := store.Submit(Submit{Branch: "agent/fresh", SHA: freshSHA, Receipt: testReceipt(t, freshSHA)})
	if err != nil {
		t.Fatal(err)
	}
	setParked(t, store, fresh.ID, "max_attempts_exhausted", now.Add(-time.Hour))

	plan, err := store.Sweep(now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SweepEntry{}
	for _, e := range plan.Entries {
		got[e.Candidate.ID] = e
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 parked entries, got %d: %#v", len(got), got)
	}
	if e := got[superseded.ID]; e.Classification != SweepSuperseded || e.ProposedAction != "reject" {
		t.Fatalf("superseded entry=%#v", e)
	}
	if e := got[envDegraded.ID]; e.Classification != SweepEnvironmentDegraded || e.ProposedAction != "" {
		t.Fatalf("environment_degraded entry=%#v", e)
	}
	if e := got[stale.ID]; e.Classification != SweepStale || e.ProposedAction != "" {
		t.Fatalf("stale entry=%#v", e)
	}
	if e := got[fresh.ID]; e.Classification != SweepUnclassified || e.ProposedAction != "" {
		t.Fatalf("unclassified entry=%#v", e)
	}
	counts := plan.CountByClassification()
	if counts[SweepSuperseded] != 1 || counts[SweepEnvironmentDegraded] != 1 || counts[SweepStale] != 1 || counts[SweepUnclassified] != 1 {
		t.Fatalf("counts=%#v", counts)
	}
}

func TestApplySweepOnlyRejectsSupersededAndLeavesEverythingElseParked(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)

	dupSHA := strings.Repeat("d", 40)
	landedTwin, err := store.Submit(Submit{Branch: "agent/landed-twin", SHA: dupSHA, Receipt: testReceipt(t, dupSHA)})
	if err != nil {
		t.Fatal(err)
	}
	setLanded(t, store, landedTwin.ID, dupSHA)
	superseded, err := store.Submit(Submit{Branch: "agent/superseded", SHA: dupSHA, Admission: EmergencySkipTestsAdmission})
	if err != nil {
		t.Fatal(err)
	}
	setParked(t, store, superseded.ID, "gate_failed", now.Add(-time.Hour))

	staleSHA := strings.Repeat("f", 40)
	stale, err := store.Submit(Submit{Branch: "agent/stale", SHA: staleSHA, Receipt: testReceipt(t, staleSHA)})
	if err != nil {
		t.Fatal(err)
	}
	setParked(t, store, stale.ID, "max_attempts_exhausted", now.Add(-10*24*time.Hour))

	plan, err := store.Sweep(now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	acted, err := store.ApplySweep(plan, "brad", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(acted) != 1 || acted[0].ID != superseded.ID || acted[0].Phase != Rejected {
		t.Fatalf("acted=%#v", acted)
	}

	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range state.Candidates {
		if c.ID == stale.ID && c.Phase != NeedsInput {
			t.Fatalf("stale candidate must remain parked for a human, got phase=%s", c.Phase)
		}
	}
	rejected := mustGet(t, store, superseded.ID)
	if !strings.Contains(rejected.EjectionReason, "identical commit") {
		t.Fatalf("rejection reason should carry the sweep's evidence: %q", rejected.EjectionReason)
	}
}
