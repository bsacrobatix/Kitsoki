package queue

import "sort"

// DefaultTerminalHistoryLimit bounds how many Landed candidates and how many
// Rejected candidates state.json retains (each counted separately) once
// compaction prunes history beyond it. Everything else is exempt: parked
// candidates (needs_input / needs_conflict_input) and every actively-worked
// phase (queued, preparing, gating, awaiting_approval, retry_wait, ...) are
// never pruned by retention, no matter how large the queue's terminal
// history grows — only Landed and Rejected records age out, and only past
// this cap. A 2026-07 production state.json grew to 2.1MB with 230 of 252
// candidates terminal and never compacted, slowing every worker cycle; this
// bound exists to keep that from recurring while still leaving enough
// recent history around for post-hoc debugging.
const DefaultTerminalHistoryLimit = 50

// compactTerminalHistory prunes Landed and Rejected candidates beyond limit
// per status, keeping the most recently completed ones and dropping the
// rest. limit <= 0 takes DefaultTerminalHistoryLimit. It is a pure function
// over State — callers apply it before every persist (write, in particular)
// so state.json is compacted automatically on every save, and opportunistically
// on read so a queue that already grew unbounded before this existed shrinks
// the moment anything next looks at it — no operator command is required
// either way.
func compactTerminalHistory(state State, limit int) State {
	if limit <= 0 {
		limit = DefaultTerminalHistoryLimit
	}
	byStatus := map[Status][]int{}
	for i, c := range state.Candidates {
		if p := c.phase(); terminal(p) {
			byStatus[p] = append(byStatus[p], i)
		}
	}
	drop := map[int]bool{}
	for _, idxs := range byStatus {
		if len(idxs) <= limit {
			continue
		}
		sort.Slice(idxs, func(a, b int) bool {
			return terminalRecency(state.Candidates[idxs[a]]) > terminalRecency(state.Candidates[idxs[b]])
		})
		for _, i := range idxs[limit:] {
			drop[i] = true
		}
	}
	if len(drop) == 0 {
		return state
	}
	kept := make([]Candidate, 0, len(state.Candidates)-len(drop))
	for i, c := range state.Candidates {
		if drop[i] {
			continue
		}
		kept = append(kept, c)
	}
	state.Candidates = kept
	return state
}

// terminalRecency orders terminal candidates newest-first for retention:
// Completed time when set (the normal case — both Reject and a successful
// finalization stamp it), falling back to the durable admission Sequence for
// the one path that retires a candidate as Rejected without a completion
// timestamp (a fresh resubmission superseding a still-active prior
// candidate of the same SHA; see Store.Submit).
func terminalRecency(c Candidate) int64 {
	if !c.Completed.IsZero() {
		return c.Completed.UnixNano()
	}
	return int64(c.Sequence)
}

// readCompacted reads durable state and reports whether it differs from what
// is currently on disk — either because Store.read migrated a legacy schema,
// or because retention would prune terminal history — so callers that only
// rewrite state.json when something durable actually changed (avoiding a
// perpetual marshal/fsync loop on an otherwise idle, large queue) also pick
// up compaction of history that predates this cap without needing a fresh
// mutation to trigger it.
func (s Store) readCompacted(path string) (State, bool, error) {
	state, migrated, err := s.read(path)
	if err != nil {
		return State{}, false, err
	}
	compacted := compactTerminalHistory(state, DefaultTerminalHistoryLimit)
	dirty := migrated || len(compacted.Candidates) != len(state.Candidates)
	return compacted, dirty, nil
}
