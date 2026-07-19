package queue

import (
	"fmt"
	"strings"
	"time"
)

// SweepClassification explains why a sweep flagged a parked candidate, and
// implies what (if anything) the sweep proposes doing about it.
type SweepClassification string

const (
	// SweepSuperseded means another candidate already landed the exact same
	// commit — this one has nothing left to accomplish. The only
	// classification the sweep proposes an action for.
	SweepSuperseded SweepClassification = "superseded"
	// SweepEnvironmentDegraded means the candidate parked at A3's
	// environmental wall-clock bound, not because of a real product
	// problem. A human should confirm the underlying infrastructure issue
	// is actually resolved before resuming — the sweep never resumes a
	// parked candidate on its own (see the package doc: parked candidates
	// require explicit human input).
	SweepEnvironmentDegraded SweepClassification = "environment_degraded"
	// SweepStale means the candidate has sat parked longer than the sweep's
	// staleness threshold with no operator action yet.
	SweepStale SweepClassification = "stale"
	// SweepUnclassified is everything else: still worth a human look, no
	// heuristic applies.
	SweepUnclassified SweepClassification = "unclassified"
)

// DefaultSweepStaleAfter is how long a parked candidate sits untouched
// before Sweep classifies it as stale.
const DefaultSweepStaleAfter = 7 * 24 * time.Hour

type SweepEntry struct {
	Candidate      Candidate           `json:"candidate"`
	Classification SweepClassification `json:"classification"`
	Reason         string              `json:"reason"`
	// ProposedAction is "reject" for superseded entries and "" (none) for
	// everything else — the sweep only ever auto-actions the one
	// classification that carries zero judgment call: the underlying
	// commit already landed, so nothing else this candidate could do
	// matters anymore.
	ProposedAction string `json:"proposed_action,omitempty"`
}

type SweepPlan struct {
	Entries []SweepEntry `json:"entries"`
}

func (p SweepPlan) CountByClassification() map[SweepClassification]int {
	out := map[SweepClassification]int{}
	for _, e := range p.Entries {
		out[e.Classification]++
	}
	return out
}

// Sweep classifies every parked (needs_input / needs_conflict_input)
// candidate without mutating anything — it is always safe to call, and
// safe to call repeatedly. staleAfter <= 0 takes DefaultSweepStaleAfter.
func (s Store) Sweep(now time.Time, staleAfter time.Duration) (SweepPlan, error) {
	state, err := s.readState()
	if err != nil {
		return SweepPlan{}, err
	}
	if staleAfter <= 0 {
		staleAfter = DefaultSweepStaleAfter
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	landedSHAs := map[string]bool{}
	for _, c := range state.Candidates {
		if c.phase() == Landed {
			landedSHAs[c.SHA] = true
		}
	}
	var plan SweepPlan
	for _, c := range state.Candidates {
		if !parked(c.phase()) {
			continue
		}
		entry := SweepEntry{Candidate: c}
		switch {
		case landedSHAs[c.SHA]:
			entry.Classification = SweepSuperseded
			entry.Reason = "another candidate for the identical commit " + c.SHA + " already landed"
			entry.ProposedAction = "reject"
		case strings.Contains(c.RetryReason, "environment_degraded"):
			entry.Classification = SweepEnvironmentDegraded
			entry.Reason = "parked at the environmental wall-clock bound (" + c.RetryReason + "); confirm the underlying infrastructure issue is fixed before resuming"
		case !c.ParkedAt.IsZero() && !now.Before(c.ParkedAt.Add(staleAfter)):
			entry.Classification = SweepStale
			entry.Reason = fmt.Sprintf("parked for %s (>= %s threshold) with no operator action yet", now.Sub(c.ParkedAt).Round(time.Hour), staleAfter)
		default:
			entry.Classification = SweepUnclassified
			entry.Reason = "no sweep heuristic applies; needs a manual look"
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

// ApplySweep executes plan's proposed actions through the existing audited
// operator verbs — currently just Reject for superseded entries. Every
// other classification is report-only by design: a sweep must never
// silently resume or otherwise mutate a candidate that requires human
// judgment, only ever clear away the unambiguous case.
func (s Store) ApplySweep(plan SweepPlan, actor, reason string) ([]Candidate, error) {
	var results []Candidate
	for _, entry := range plan.Entries {
		if entry.ProposedAction != "reject" {
			continue
		}
		r := reason
		if strings.TrimSpace(r) == "" {
			r = "queue sweep: " + entry.Reason
		}
		c, err := s.Reject(Op{ID: entry.Candidate.ID, Actor: actor, Reason: r})
		if err != nil {
			return results, err
		}
		results = append(results, c)
	}
	return results, nil
}
