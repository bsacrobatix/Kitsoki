package queue

import "strings"

// ReasonCode is a small, closed classification of why a candidate could not
// proceed. It is carried alongside — never instead of — the free-text
// RetryReason/Failure detail string: RetryReason stays exactly what it always
// was (a human-readable stage tag, unconstrained going forward), and
// ReasonCode gives an operator, `queue status`, or a future medic a stable
// enum to switch on instead of parsing hand-typed prose (the motivating case:
// 8 parked POG candidates carrying free-text retry_reason from interactive
// sessions, with no machine-checkable classification at all).
//
// The set is deliberately small and closed. New failure modes should map onto
// an existing code with a more specific RetryReason string rather than
// growing this enum, unless the queue itself produces a genuinely new
// mechanism of failure.
type ReasonCode string

const (
	// ReasonGateFailed: the deterministic gate ran (no harness break) and
	// returned red. Used both while a candidate is still retrying
	// (retry_wait) and, when no Repairer is configured, at the point
	// attempts are exhausted — the gate result is exactly as specific either
	// way, so the code does not change just because the budget ran out.
	ReasonGateFailed ReasonCode = "gate-failed"

	// ReasonMergeConflict: conflict markers remain in the integration
	// instance after rerere replay and the configured or embedded git-ops
	// resolver both had their turn; a human must resolve the conflict or
	// supply a continuation. See resolver.go's resolveConflicts.
	ReasonMergeConflict ReasonCode = "merge-conflict"

	// ReasonLeaseLost: a worker's lease on an in-flight candidate expired
	// and the candidate was reclaimed (see Worker.claimPreparation's expiry
	// sweep and Worker.leaseLost) before the original worker's result
	// returned. The reclaimed candidate is requeued into Reprepare, not
	// parked — this code records *why* a reprepare was needed, alongside
	// Failure, rather than driving a retry_wait/needs_human transition
	// itself.
	ReasonLeaseLost ReasonCode = "lease-lost"

	// ReasonSealMismatch: the prepared receipt/tuple seal (base, tree, gate
	// version, dependency fingerprint — see preparedFingerprint and
	// validatePreparedTuple) no longer matches the candidate's current
	// durable identity. Reserved for the day validatePreparedTuple's
	// mismatch is wired to an explicit park/retry transition instead of
	// silently withholding finalization authorization; not yet emitted by
	// any current code path.
	ReasonSealMismatch ReasonCode = "seal-mismatch"

	// ReasonResolverExhausted: preparation (Integration.Speculate, which
	// includes the queue's conflict-resolution stage — see resolver.go) kept
	// failing until the bounded product-failure attempt budget ran out.
	ReasonResolverExhausted ReasonCode = "resolver-exhausted"

	// ReasonRepairerExhausted: a configured Repairer kept being unable to
	// turn a red deterministic gate green until the bounded attempt budget
	// ran out. Only used when a Repairer was actually configured; a gate
	// that exhausts its attempts with no repairer in play stays
	// ReasonGateFailed.
	ReasonRepairerExhausted ReasonCode = "repairer-exhausted"

	// ReasonFinalizationFailed: the protected compare-and-swap kept failing
	// (no harness break, no stale-base reprepare) whether still retrying or
	// once the attempt budget for it ran out.
	ReasonFinalizationFailed ReasonCode = "finalization-failed"

	// ReasonBudgetExhausted: the bounded attempt budget ran out for a stage
	// with no more specific code above. A defensive fallback, not expected
	// to be reached by any stage the queue currently produces.
	ReasonBudgetExhausted ReasonCode = "budget-exhausted"

	// ReasonEnvironmentDegraded: a transient-infrastructure failure streak
	// (fetch/lock/workspace-create — see the EnvError/Environmental family)
	// persisted past ProcessDeps.MaxEnvDuration without ever burning the
	// product-failure attempt budget it is deliberately kept separate from.
	ReasonEnvironmentDegraded ReasonCode = "environment-degraded"

	// ReasonHarnessFailure: the queue's own machinery — a resolver, gate, or
	// finalizer launch path — could not run at all (queue.HarnessError),
	// distinct from a harness that ran and returned a red result. Parks
	// immediately (no retry burn on a broken launch path).
	ReasonHarnessFailure ReasonCode = "harness-failure"

	// ReasonOperatorParked: a human explicitly parked this candidate via the
	// `queue park` verb (Store.Park) for a reason of their own — not an
	// automation failure. The code names *who/how*, independent of whatever
	// free-text reason the operator supplied.
	ReasonOperatorParked ReasonCode = "operator-parked"

	// ReasonLegacyFreeform: a durable record written before this enum
	// existed carries a free-text RetryReason with no typed code. normalize
	// stamps this onto any RetryReason found without a ReasonCode on load —
	// it is backward-compatibility only and is never assigned by new code.
	ReasonLegacyFreeform ReasonCode = "legacy-freeform"
)

// reasonCodeForStage maps a queue-internal, still-retrying stage tag (the
// `reason` string retryOrPark/retryOrParkEnv are called with, e.g.
// "gate_failed", "speculation_failed", "finalization_failed") to its typed
// code. It is also used for the corresponding harness-free exhaustion case
// via exhaustionReasonCode, which starts from this and only elevates to a
// *-exhausted code where a distinct remediation subsystem was actually in
// play.
func reasonCodeForStage(stage string) ReasonCode {
	switch stage {
	case "gate_failed":
		return ReasonGateFailed
	case "finalization_failed":
		return ReasonFinalizationFailed
	case "speculation_failed":
		return ReasonResolverExhausted
	default:
		return ReasonBudgetExhausted
	}
}

// exhaustionReasonCode maps the stage a candidate's bounded attempt budget
// ran out on to its needs_human ReasonCode. hasRepairer reports whether
// ProcessDeps.Repairer was configured for this run: a gate stage only earns
// the more specific ReasonRepairerExhausted when a repairer actually had
// attempts to exhaust.
func exhaustionReasonCode(stage string, hasRepairer bool) ReasonCode {
	if strings.HasSuffix(stage, "_environment_degraded") {
		return ReasonEnvironmentDegraded
	}
	if stage == "gate_failed" && hasRepairer {
		return ReasonRepairerExhausted
	}
	return reasonCodeForStage(stage)
}
