package vmpool

import "time"

// PreservedWorker is a PreserveFailed durable worker as classified by
// Classify, carrying the instance it is holding open for post-mortem and how
// much of Config.PreserveFailedTTL remains. TTLRemaining is zero or negative
// once the grace period has elapsed (the worker then appears under
// ReconcilePlan.PreservedExpired instead of PreservedProtected).
type PreservedWorker struct {
	WorkerID     string        `json:"worker_id"`
	InstanceID   string        `json:"instance_id,omitempty"`
	TTLRemaining time.Duration `json:"ttl_remaining"`
}

// ReconcilePlan is the read-only classification of a pool's durable worker
// records against its live, tag-matched cloud instances, as computed by
// Classify. It is exactly the analysis half of a reconcile pass -- no cloud
// calls, no mutation -- factored out so Pool.Reconcile (which acts on the
// plan) and any plan-only caller, such as `vmpool status`/`vmpool reap`
// without --repair, can never classify a worker differently. Every instance
// and worker lands in exactly one bucket:
//
//   - Active: known-active, non-preserved.
//   - PreservedProtected: preserved, still within its TTL.
//   - PreservedExpired: preserved, TTL elapsed, reclaimable.
//   - Lost: tracked but the instance is missing.
//   - Orphans: an untracked instance (matches no worker record at all).
type ReconcilePlan struct {
	// Active are cloud instance IDs backing a known, non-terminal,
	// non-preserved durable worker: no action needed.
	Active []string `json:"active"`
	// PreservedProtected are PreserveFailed workers still within
	// Config.PreserveFailedTTL: their instance is deliberately kept running
	// for post-mortem and must not be touched.
	PreservedProtected []PreservedWorker `json:"preserved_protected,omitempty"`
	// PreservedExpired are PreserveFailed workers whose
	// Config.PreserveFailedTTL has elapsed: reclaimable. --repair destroys
	// the instance (if still live) and clears Preserved.
	PreservedExpired []PreservedWorker `json:"preserved_expired,omitempty"`
	// Lost are durable worker IDs that are tracked and non-terminal but
	// whose cloud instance no longer exists in the tag sweep.
	Lost []string `json:"lost"`
	// Orphans are live, tag-matched cloud instance IDs that match no
	// durable worker record at all: untracked spend with no local trace.
	Orphans []string `json:"orphans"`
}

// Classify computes a ReconcilePlan for state against instances (the pool's
// live, tag-filtered cloud instances) as of now, using cfg for
// Config.PreserveFailedTTL. Classify is pure: it makes no store or provider
// calls and mutates neither argument -- callers fetch both first
// (Store.Load, Provisioner.ListByTag) and hand them in. This is the single
// source of truth both Pool.Reconcile (repair) and a plan-only caller
// (report) build on, so the two can never drift.
func Classify(state State, instances []Instance, cfg Config, now time.Time) ReconcilePlan {
	// knownInstance holds every live instance ID accounted for by a
	// non-terminal or still-protected-preserved worker.
	knownInstance := make(map[string]bool, len(state.Workers))
	// preservedInstance holds every instance ID any Preserved worker
	// (protected or expired) is holding, so the orphan pass below never
	// double-reports an expired-preserved worker's instance as untracked:
	// it is reclaimable, not an orphan.
	preservedInstance := make(map[string]bool, len(state.Workers))
	instanceExists := make(map[string]bool, len(instances))
	for _, inst := range instances {
		instanceExists[inst.ID] = true
	}

	var plan ReconcilePlan
	for _, w := range state.Workers {
		switch {
		case w.Preserved:
			// A preserved worker's instance is always reported through
			// PreservedProtected or PreservedExpired below, never through
			// Active or Orphans: preservedInstance keeps the instance-loop
			// below from double-bucketing it into either.
			if w.InstanceID != "" {
				preservedInstance[w.InstanceID] = true
			}
			pw := PreservedWorker{
				WorkerID:     w.ID,
				InstanceID:   w.InstanceID,
				TTLRemaining: cfg.PreserveFailedTTL - now.Sub(w.TerminalAt),
			}
			if preserveExpired(w, cfg, now) {
				plan.PreservedExpired = append(plan.PreservedExpired, pw)
				continue
			}
			plan.PreservedProtected = append(plan.PreservedProtected, pw)
		case !w.Status.Terminal():
			if w.InstanceID == "" {
				continue
			}
			knownInstance[w.InstanceID] = true
			if !instanceExists[w.InstanceID] {
				plan.Lost = append(plan.Lost, w.ID)
			}
		}
	}

	for _, inst := range instances {
		switch {
		case preservedInstance[inst.ID]:
			// Already accounted for under PreservedProtected/PreservedExpired
			// above; neither active nor orphaned.
		case knownInstance[inst.ID]:
			plan.Active = append(plan.Active, inst.ID)
		default:
			plan.Orphans = append(plan.Orphans, inst.ID)
		}
	}

	return plan
}

// preserveExpired reports whether w's PreserveFailed grace period
// (cfg.PreserveFailedTTL) has elapsed since it terminalized. A worker that
// was never preserved, or has no recorded TerminalAt yet, is never expired.
func preserveExpired(w Worker, cfg Config, now time.Time) bool {
	if !w.Preserved || w.TerminalAt.IsZero() {
		return false
	}
	return now.Sub(w.TerminalAt) > cfg.PreserveFailedTTL
}

// ReportFromPlan derives the ReconcileReport shape from plan. It is pure and
// performs no action itself: Pool.Reconcile's repair path and a plan-only
// caller's report path both call it on the exact same Classify output, so
// the plan an operator inspects before --repair can never drift from what
// --repair reports having done.
func ReportFromPlan(plan ReconcilePlan) ReconcileReport {
	report := ReconcileReport{
		Active:             append([]string(nil), plan.Active...),
		Orphans:            append([]string(nil), plan.Orphans...),
		Lost:               append([]string(nil), plan.Lost...),
		PreservedProtected: append([]PreservedWorker(nil), plan.PreservedProtected...),
	}
	for _, pw := range plan.PreservedExpired {
		report.ExpiredPreserved = append(report.ExpiredPreserved, pw.WorkerID)
	}
	return report
}
