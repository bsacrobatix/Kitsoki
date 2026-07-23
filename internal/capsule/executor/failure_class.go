package executor

// FailureClass is a machine-readable classification for a terminal (failed)
// Capsule worker execution, letting an orchestrator branch on WHY a run
// failed instead of string-matching RunRecord.Error / ExecutionStatus.Error.
//
// A class is assigned only at the seam where the cause is definitively
// known: the job-start preflight (before the story launches), the
// materialized story's closure-digest verification, or the coding-agent
// subprocess's exit handling. A record with FailureClassNone predates this
// field, was cancelled, or failed at a seam this package does not yet
// classify — callers must keep treating an unclassified failure exactly as
// before (environmental/retryable), so adding this field is behavior- and
// wire-compatible with every existing reader.
type FailureClass string

const (
	// FailureClassNone is the zero value: unclassified (older records, a
	// cancelled run, or a failure seam this package does not yet
	// distinguish) or a non-terminal/successful run.
	FailureClassNone FailureClass = ""
	// FailureClassPreflightAuth means the job-start preflight found the
	// selected agent backend's auth material absent, unparsable, or
	// expired before any story turn ran.
	FailureClassPreflightAuth FailureClass = "preflight_auth"
	// FailureClassPreflightEnv means the job-start preflight (or the
	// pre-existing environment-lock verification stage) found the
	// worker's environment unfit to run the story: insufficient disk
	// headroom, a required PATH tool missing for the selected backend, an
	// engine/environment digest mismatch, or an incoherent
	// model/endpoint pairing.
	FailureClassPreflightEnv FailureClass = "preflight_env"
	// FailureClassVerifyStory means the materialized source's story
	// closure digest did not match the sealed envelope.
	FailureClassVerifyStory FailureClass = "verify_story"
	// FailureClassAgentAuth means the coding-agent CLI ran and exited
	// reporting an authentication/authorization failure once the story
	// was already running.
	FailureClassAgentAuth FailureClass = "agent_auth"
	// FailureClassAgentQuota means the coding-agent CLI ran and exited
	// reporting a rate limit, quota, or usage-budget failure once the
	// story was already running.
	FailureClassAgentQuota FailureClass = "agent_quota"
	// FailureClassInfra means a transport/process failure not
	// attributable to agent auth/quota: source materialization, a
	// checkpoint write, or a runner process that never produced a result.
	FailureClassInfra FailureClass = "infra"
	// FailureClassStory means the story ran to completion (the agent
	// subprocess, if any, exited normally) and the run still failed for a
	// genuine business/story-authored reason: a failed verdict, a
	// stalled drive, or a gate that did not pass.
	FailureClassStory FailureClass = "story"
)

// Valid reports whether c is one of the classes this package assigns.
// FailureClassNone is deliberately not "valid" here: it means "no
// classification", not a class a caller should persist or key off of.
func (c FailureClass) Valid() bool {
	switch c {
	case FailureClassPreflightAuth, FailureClassPreflightEnv, FailureClassVerifyStory,
		FailureClassAgentAuth, FailureClassAgentQuota, FailureClassInfra, FailureClassStory:
		return true
	default:
		return false
	}
}

// KnownFailureClasses lists every class this package assigns, in the fixed
// order documented above. Useful for validating a round-tripped record
// without hardcoding the set at each call site.
func KnownFailureClasses() []FailureClass {
	return []FailureClass{
		FailureClassPreflightAuth,
		FailureClassPreflightEnv,
		FailureClassVerifyStory,
		FailureClassAgentAuth,
		FailureClassAgentQuota,
		FailureClassInfra,
		FailureClassStory,
	}
}
