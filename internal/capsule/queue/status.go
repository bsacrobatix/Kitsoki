package queue

import "time"

// StatusSummary rolls up the durable candidate list into the counts an
// operator actually wants at a glance — "where did the last N minutes go"
// — without them having to eyeball every StatusLine themselves.
type StatusSummary struct {
	PhaseCounts       map[Status]int `json:"phase_counts"`
	RetryReasonCounts map[string]int `json:"retry_reason_counts,omitempty"`
	// ReasonCodeCounts rolls up the same RetryWait/parked population as
	// RetryReasonCounts, keyed by the typed ReasonCode instead of the raw
	// free-text string — the stable, closed-set view an operator or medic
	// can switch on. Zero-value (unset) ReasonCode never contributes an
	// entry.
	ReasonCodeCounts map[ReasonCode]int `json:"reason_code_counts,omitempty"`
	TrainDepth       int                `json:"train_depth"`
	ParkedCount      int                `json:"parked_count"`
	// NeedsHumanCount is ParkedCount's subset that is specifically
	// NeedsHuman — automation's own declaration that it is out of options,
	// as opposed to an operator's deliberate NeedsInput park or an
	// unresolved NeedsConflictInput. Broken out so it is visible at a
	// glance in `queue status` rather than requiring a PhaseCounts lookup.
	NeedsHumanCount int    `json:"needs_human_count,omitempty"`
	OldestParkedAge string `json:"oldest_parked_age,omitempty"`
	// MedicActionCounts rolls up MedicLastAction (P1.7 part 2's medic — see
	// medic.go) across every candidate the medic has ever touched:
	// "dispatch_resolver", "kick_gate_retry", or "escalate:<reason>" —
	// "what did the medic do last, and how often" at a glance, without
	// eyeballing every candidate's StatusLine. A candidate the medic never
	// touched contributes nothing.
	MedicActionCounts map[string]int `json:"medic_action_counts,omitempty"`
}

// Summarize computes a StatusSummary as of at.
func Summarize(state State, at time.Time) StatusSummary {
	summary := StatusSummary{PhaseCounts: map[Status]int{}, RetryReasonCounts: map[string]int{}, ReasonCodeCounts: map[ReasonCode]int{}}
	var oldestParked time.Time
	for _, c := range state.Candidates {
		phase := c.phase()
		summary.PhaseCounts[phase]++
		if !terminal(phase) {
			summary.TrainDepth++
		}
		if parked(phase) {
			summary.ParkedCount++
			if phase == NeedsHuman {
				summary.NeedsHumanCount++
			}
			if !c.ParkedAt.IsZero() && (oldestParked.IsZero() || c.ParkedAt.Before(oldestParked)) {
				oldestParked = c.ParkedAt
			}
		}
		if c.RetryReason != "" && (phase == RetryWait || parked(phase)) {
			summary.RetryReasonCounts[c.RetryReason]++
		}
		if c.ReasonCode != "" && (phase == RetryWait || parked(phase)) {
			summary.ReasonCodeCounts[c.ReasonCode]++
		}
		if c.MedicLastAction != "" {
			if summary.MedicActionCounts == nil {
				summary.MedicActionCounts = map[string]int{}
			}
			summary.MedicActionCounts[c.MedicLastAction]++
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
