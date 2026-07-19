package queue

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSummarizeCountsPhasesTrainDepthAndOldestParked(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	state := State{Candidates: []Candidate{
		{ID: "a", Phase: ReadyToFinalize},
		{ID: "b", Phase: Gating},
		{ID: "c", Phase: Landed},
		{ID: "d", Phase: NeedsInput, RetryReason: "gate_failed", ParkedAt: now.Add(-2 * time.Hour)},
		{ID: "e", Phase: NeedsInput, RetryReason: "gate_failed", ParkedAt: now.Add(-30 * time.Minute)},
		{ID: "f", Phase: NeedsConflictInput, ParkedAt: now.Add(-5 * time.Hour)},
	}}
	summary := Summarize(state, now)

	if summary.PhaseCounts[ReadyToFinalize] != 1 || summary.PhaseCounts[Gating] != 1 || summary.PhaseCounts[Landed] != 1 || summary.PhaseCounts[NeedsInput] != 2 || summary.PhaseCounts[NeedsConflictInput] != 1 {
		t.Fatalf("phase counts=%#v", summary.PhaseCounts)
	}
	// Train depth: everything non-terminal (excludes only the landed one).
	if summary.TrainDepth != 5 {
		t.Fatalf("train depth=%d, want 5", summary.TrainDepth)
	}
	if summary.ParkedCount != 3 {
		t.Fatalf("parked count=%d, want 3", summary.ParkedCount)
	}
	if summary.OldestParkedAge != (5 * time.Hour).String() {
		t.Fatalf("oldest parked age=%q, want the 5h-old entry (f)", summary.OldestParkedAge)
	}
	if summary.RetryReasonCounts["gate_failed"] != 2 {
		t.Fatalf("retry reason counts=%#v", summary.RetryReasonCounts)
	}
}

func TestSummarizeEmptyStateHasNoOldestParkedAge(t *testing.T) {
	summary := Summarize(State{}, time.Now())
	if summary.OldestParkedAge != "" {
		t.Fatalf("oldest parked age=%q, want empty for an empty state", summary.OldestParkedAge)
	}
	if summary.TrainDepth != 0 || summary.ParkedCount != 0 {
		t.Fatalf("summary=%#v, want all zero", summary)
	}
}

func TestViewCandidatesComputesPhaseDuration(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	views := ViewCandidates([]Candidate{
		{ID: "a", PhaseStartedAt: now.Add(-90 * time.Second)},
		{ID: "b"}, // zero PhaseStartedAt: never entered a leased phase
	}, now)
	if views[0].PhaseDuration != (90 * time.Second).String() {
		t.Fatalf("a.PhaseDuration=%q", views[0].PhaseDuration)
	}
	if views[1].PhaseDuration != "" {
		t.Fatalf("b.PhaseDuration=%q, want empty for zero PhaseStartedAt", views[1].PhaseDuration)
	}
	// Embedding must round-trip every durable field untouched.
	if views[0].ID != "a" {
		t.Fatalf("embedded Candidate field lost: %#v", views[0])
	}
}

func TestReportCarriesSchemaSummaryAndCandidateViews(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	state := State{Schema: Schema, Candidates: []Candidate{{ID: "a", Phase: Queued, PhaseStartedAt: now.Add(-time.Minute)}}}
	report := Report(state, now)
	if report.Schema != Schema {
		t.Fatalf("schema=%q", report.Schema)
	}
	if report.Summary.TrainDepth != 1 {
		t.Fatalf("summary=%#v", report.Summary)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].PhaseDuration != time.Minute.String() {
		t.Fatalf("candidates=%#v", report.Candidates)
	}
}

func TestReportJSONHasSummaryAndCandidatesKeys(t *testing.T) {
	report := Report(State{Schema: Schema, Candidates: []Candidate{{ID: "a", Phase: Queued}}}, time.Now())
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"schema"`, `"summary"`, `"candidates"`, `"train_depth"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("json output missing %s: %s", key, raw)
		}
	}
}
