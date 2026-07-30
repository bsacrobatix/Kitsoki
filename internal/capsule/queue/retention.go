package queue

import "sort"

// DefaultTerminalHistoryLimit bounds how many Landed candidates and how many
// Rejected candidates state.json retains *per target ref* (each terminal
// status counted separately within each target) once compaction prunes
// history beyond it. Everything else is exempt: parked candidates
// (needs_input / needs_conflict_input) and every actively-worked phase
// (queued, preparing, gating, awaiting_approval, retry_wait, ...) are never
// pruned by retention, no matter how large the queue's terminal history
// grows — only Landed and Rejected records age out, and only past this cap.
// A 2026-07 production state.json grew to 2.1MB with 230 of 252 candidates
// terminal and never compacted, slowing every worker cycle; this bound exists
// to keep that from recurring while still leaving enough recent history around
// for post-hoc debugging.
//
// The resulting size bound is (terminal statuses) x (distinct target refs) x
// limit, plus the pins below. Target refs are the project's protected
// branches, a small fixed set (POG runs two: staging/local and main), so this
// stays bounded — while making the budget per-target is what keeps a busy
// target from evicting a quiet one's load-bearing records (see
// retentionPinnedSHAs and compactTerminalHistory).
const DefaultTerminalHistoryLimit = 50

// terminalGroup is the retention budget key. Grouping by target ref as well
// as status is load-bearing, not cosmetic: Landed records are durable *state*
// for several consumers, not just history, and every one of those consumers
// looks a landing up within a single target ref.
//
//   - PromoteExistingAuthority.sourceLanding scans for
//     `TargetRef == SourceTarget && phase() == Landed && ResultMainSHA == LandedSHA`
//     to prove the source landing of a staging->main promotion (POG's
//     integration train calls this via `capsule promote-existing`). With one
//     shared per-status budget, a flood of landings on the *destination*
//     target evicts the source target's landing and the promotion fails
//     closed with "no receipt-bearing landed queue result". Per target, the
//     source landing can only be evicted by `limit` newer landings on the
//     source target itself — and those move the source ref, which the
//     promotion authority already rejects as target_moved.
//   - Store.ReconcileLanding requires its candidate to still be present and
//     Landed, and only ever addresses the managed "staging/local" target;
//     that target now has its own budget instead of competing with main's.
type terminalGroup struct {
	status    Status
	targetRef string
}

// compactTerminalHistory prunes Landed and Rejected candidates beyond limit
// per (terminal status, target ref), keeping the most recently completed ones
// and dropping the rest. limit <= 0 takes DefaultTerminalHistoryLimit. It is a
// pure function over State — callers apply it before every persist (write, in
// particular) so state.json is compacted automatically on every save, and
// opportunistically on read so a queue that already grew unbounded before this
// existed shrinks the moment anything next looks at it — no operator command
// is required either way.
//
// Landed records pinned by retentionPinned are exempt entirely: they are
// still referenced by a retained non-terminal candidate, so dropping them
// would silently change queue behavior rather than merely forget history.
func compactTerminalHistory(state State, limit int) State {
	if limit <= 0 {
		limit = DefaultTerminalHistoryLimit
	}
	pinned := retentionPinned(state)
	byGroup := map[terminalGroup][]int{}
	for i, c := range state.Candidates {
		p := c.phase()
		if !terminal(p) || pinned[i] {
			continue
		}
		g := terminalGroup{status: p, targetRef: c.TargetRef}
		byGroup[g] = append(byGroup[g], i)
	}
	drop := map[int]bool{}
	for _, idxs := range byGroup {
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

// retentionPinned returns the state.Candidates indexes of Landed records that
// are still load-bearing for a candidate retention will keep, and therefore
// must not be pruned no matter how far past the cap they have aged. A Landed
// record is pinned when a retained *non-terminal* candidate still depends on
// it, in either of two shapes:
//
//   - The Landed record shares its SHA with a live candidate. The motivating
//     case is Sweep: it classifies a parked (needs_input /
//     needs_conflict_input) candidate as SweepSuperseded — the one
//     classification ApplySweep acts on, by rejecting it — only while another
//     candidate for the identical SHA is still present as Landed. Parked
//     candidates are themselves never pruned and can sit for months, so
//     without this pin a long-parked duplicate silently loses its superseded
//     classification and stops being auto-rejected. Matched by SHA alone,
//     across target refs, because Sweep's own landedSHAs index is keyed by
//     SHA alone; and against every live candidate rather than just the parked
//     ones, because Store.Submit's idempotent-resubmission check is likewise
//     a SHA scan over the whole state.
//   - A non-terminal candidate's SHA, matched against a Landed record's
//     ResultMainSHA. That is exactly the shape of a promotion in flight: the
//     destination candidate's SHA is the source target's landed result, so
//     the source landing stays resolvable for the whole life of the
//     destination candidate (restart-resumed promote-existing re-verifies it).
//
// Both pin sets are bounded by the number of retained non-terminal
// candidates, which retention never grows. Pins are resolved against
// non-terminal candidates only (never against another Landed record), so the
// result cannot chain and does not depend on map iteration order.
func retentionPinned(state State) map[int]bool {
	live := map[string]bool{}
	for _, c := range state.Candidates {
		if c.SHA == "" || terminal(c.phase()) {
			continue
		}
		live[c.SHA] = true
	}
	pinned := map[int]bool{}
	if len(live) == 0 {
		return pinned
	}
	for i, c := range state.Candidates {
		if c.phase() != Landed {
			continue
		}
		if live[c.SHA] || (c.ResultMainSHA != "" && live[c.ResultMainSHA]) {
			pinned[i] = true
		}
	}
	return pinned
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
