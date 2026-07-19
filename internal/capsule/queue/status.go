package queue

import "time"

// StatusSummary rolls up the durable candidate list into the counts an
// operator actually wants at a glance — "where did the last N minutes go"
// — without them having to eyeball every StatusLine themselves.
type StatusSummary struct {
	PhaseCounts       map[Status]int `json:"phase_counts"`
	RetryReasonCounts map[string]int `json:"retry_reason_counts,omitempty"`
	TrainDepth        int            `json:"train_depth"`
	ParkedCount       int            `json:"parked_count"`
	OldestParkedAge   string         `json:"oldest_parked_age,omitempty"`
}

// Summarize computes a StatusSummary as of at.
func Summarize(state State, at time.Time) StatusSummary {
	summary := StatusSummary{PhaseCounts: map[Status]int{}, RetryReasonCounts: map[string]int{}}
	var oldestParked time.Time
	for _, c := range state.Candidates {
		phase := c.phase()
		summary.PhaseCounts[phase]++
		if !terminal(phase) {
			summary.TrainDepth++
		}
		if parked(phase) {
			summary.ParkedCount++
			if !c.ParkedAt.IsZero() && (oldestParked.IsZero() || c.ParkedAt.Before(oldestParked)) {
				oldestParked = c.ParkedAt
			}
		}
		if c.RetryReason != "" && (phase == RetryWait || parked(phase)) {
			summary.RetryReasonCounts[c.RetryReason]++
		}
	}
	if !oldestParked.IsZero() {
		summary.OldestParkedAge = at.Sub(oldestParked).Round(time.Second).String()
	}
	return summary
}

// CandidateView is a Candidate plus fields computed relative to a report
// time — never persisted, only ever rendered. Embedding keeps every
// existing durable field at the same JSON key a consumer of a bare
// Candidate already expects.
type CandidateView struct {
	Candidate
	// PhaseDuration is how long the candidate has been in its current
	// phase as of the report time, using the same PhaseStartedAt every
	// lease/reprepare transition already stamps.
	PhaseDuration string `json:"phase_duration,omitempty"`
}

// ViewCandidates computes CandidateViews for cs as of at.
func ViewCandidates(cs []Candidate, at time.Time) []CandidateView {
	views := make([]CandidateView, len(cs))
	for i, c := range cs {
		views[i] = CandidateView{Candidate: c}
		if !c.PhaseStartedAt.IsZero() {
			views[i].PhaseDuration = at.Sub(c.PhaseStartedAt).Round(time.Second).String()
		}
	}
	return views
}

// StatusReport is the full `queue status --json` payload: the same schema
// and candidate list a bare State already carried, plus the roll-up
// summary and per-candidate phase durations.
type StatusReport struct {
	Schema     string          `json:"schema"`
	Summary    StatusSummary   `json:"summary"`
	Candidates []CandidateView `json:"candidates"`
}

// Report builds the full StatusReport for state as of at.
func Report(state State, at time.Time) StatusReport {
	return StatusReport{Schema: state.Schema, Summary: Summarize(state, at), Candidates: ViewCandidates(state.Candidates, at)}
}
