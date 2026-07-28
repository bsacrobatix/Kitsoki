package effect

// hostverbs.go is the builtin classification table for every host verb
// internal/host.RegisterBuiltins registers (docs/architecture/hosts.md). It
// lives in this leaf package — not internal/host — so internal/machine
// (which stamps the effect class onto the HostInvoked event before the host
// registry is ever consulted) can read the same table without importing
// internal/host.
//
// These are DEFAULTS: an app can override a verb's classification via a
// `host_interfaces:` operation's `effect:`/`deterministic:` declaration (the
// escape hatch named in agent-capability-model.md's cross-cutting open
// question 1). Nothing in this package enforces that override; it is the
// loader's (internal/app) concern.
//
// A handful of verbs — host.git, host.gh.ticket, host.local,
// host.local_files.ticket, host.local_github.ticket, host.ticket_federation,
// host.cypilot_artifacts —
// are prefix-fallback
// handlers that dispatch several operations via an `op` argument, and one
// verb spans multiple effect tiers (e.g. "git log" reads; "git push" is
// external). Those classify PER-OP via the entry's Ops map; ClassifyVerb
// falls back to the entry's own Effect/Deterministic when args carries no
// recognised op (or the verb takes none).

// verbEffect is one builtin verb's default classification, optionally
// specialised per operation for a prefix-fallback handler.
type verbEffect struct {
	class         Effect
	deterministic bool
	// ops overrides class/deterministic for one named operation within a
	// multi-op verb, keyed by the call's "op" argument. Nil for
	// single-purpose verbs.
	ops map[string]opEffect
}

type opEffect struct {
	class         Effect
	deterministic bool
}

// hostAgentVerbs classifies the five host.agent.* verbs by KIND, not by the
// dispatched agent's actual tool surface: ask/decide/extract are read-only
// verbs by construction (agentMutationTools rejects a mutator in their tool
// surface at load time — internal/app/loader.go); task/converse dispatch a
// full agentic session that may mutate. Every agent call is non-deterministic
// (it is an LLM call). The PRECISE per-agent join (accounting for the
// specific agent's tool surface) is what AgentDecl.Effect / host.Agent.Effect
// carry — see internal/host/agent_task_replay.go's inferReplayMode, which
// remains the authoritative per-call replay-mode classification recorded on
// task.end. This table is deliberately the coarser, story-author-invisible
// default used to stamp the general Host* trace events.
var builtinVerbTable = map[string]verbEffect{
	"host.workspace_manager.get":     {class: Read, deterministic: true},
	"host.run":                       {class: Write, deterministic: false},
	"host.agent.ask":                 {class: Read, deterministic: false},
	"host.agent.extract":             {class: Read, deterministic: false},
	"host.agent.decide":              {class: Read, deterministic: false},
	"host.agent.task":                {class: Write, deterministic: false},
	"host.agent.converse":            {class: Write, deterministic: false},
	"host.agent.codeact":             {class: Write, deterministic: false},
	"host.agent.search":              {class: Read, deterministic: true},
	"host.transport.post":            {class: External, deterministic: false},
	"host.jobs.answer_clarification": {class: Write, deterministic: true},

	"host.chat.resolve":       {class: Read, deterministic: true},
	"host.chat.list":          {class: Read, deterministic: true},
	"host.chat.transcript":    {class: Read, deterministic: true},
	"host.chat.fork":          {class: Write, deterministic: true},
	"host.chat.archive":       {class: Write, deterministic: true},
	"host.chat.create":        {class: Write, deterministic: true},
	"host.chat.rename":        {class: Write, deterministic: true},
	"host.chat.suggest_title": {class: Read, deterministic: false},
	"host.chat.resolve_ref":   {class: Read, deterministic: true},
	"host.chat.drive":         {class: Write, deterministic: false},

	"host.git_worktree": {class: Write, deterministic: true},
	// host.capsule_ci.project_checks executes the project's declared local
	// test/build commands. Those commands may write build outputs and their
	// result depends on the checked-out source and toolchain state.
	"host.capsule_ci.project_checks": {class: Write, deterministic: false},
	"host.capsule_workspace": {
		class: Write, deterministic: true,
		ops: map[string]opEffect{
			"get":    {class: Read, deterministic: true},
			"create": {class: Write, deterministic: true},
			"commit": {class: Write, deterministic: true},
			"close":  {class: Write, deterministic: true},
		},
	},
	"host.append_to_file": {class: Write, deterministic: true},
	"host.artifacts_dir":  {class: Write, deterministic: true},
	"host.inbox.add":      {class: Write, deterministic: true},

	// host.fs.writable_dir probes a directory's writability (a create+remove
	// of a hidden marker file — net-zero on-disk effect) and returns which of
	// path/fallback to use; it durably persists nothing, so it classifies as
	// Read, same as host.workspace_manager.get.
	"host.fs.writable_dir": {class: Read, deterministic: true},

	// host.ide.* — a connected-editor query is Read; the two open_* verbs
	// are a benign, reversible mutation of the OPERATOR'S IDE, resolved
	// as Write per effect-taxonomy.md's open question 2.
	"host.ide.get_diagnostics":  {class: Read, deterministic: true},
	"host.ide.get_selection":    {class: Read, deterministic: true},
	"host.ide.get_open_editors": {class: Read, deterministic: true},
	"host.ide.open_file":        {class: Write, deterministic: true},
	"host.ide.open_diff":        {class: Write, deterministic: true},
	"host.diff.open":            {class: Write, deterministic: true},

	"host.starlark.run":          {class: Write, deterministic: true},
	"host.corpus.prove":          {class: Write, deterministic: true},
	"host.corpus.freeze_receipt": {class: Write, deterministic: true},
	"host.punch.verify":          {class: Read, deterministic: true},
	"host.proposal.publish":      {class: Write, deterministic: true},
	"host.dev.profile_setup":     {class: Write, deterministic: true},
	"host.dev.onboarding":        {class: Write, deterministic: true},
	"host.decomposition.update":  {class: Write, deterministic: true},
	"host.tour.plan":             {class: Read, deterministic: true},
	"host.tour.validate":         {class: Read, deterministic: true},
	"host.product_journey.run":   {class: External, deterministic: false},
	"host.bakeoff.run":           {class: External, deterministic: false},
	"host.session_mining.run":    {class: External, deterministic: false},
	"host.ui_qa.run":             {class: External, deterministic: false},
	"host.slidey.render":         {class: Write, deterministic: true},
	"host.contact_sheet":         {class: Write, deterministic: true},
	"host.video.frame":           {class: Write, deterministic: true},
	// This carrier may materialize bundles, submit queue candidates, and run a
	// deploy verifier. Its configured commands are operator-owned but external
	// and time-dependent, so classify conservatively.
	"host.integration_train": {class: External, deterministic: false},

	// host.git — 7 ops, per-op tiers: local mutations (branch/commit) are
	// Write; anything that reaches GitHub (push/open_pr/pr_comment) is
	// External; diff/pr_status are Read.
	"host.git": {
		class: Write, deterministic: true, // fallback for an unrecognised op
		ops: map[string]opEffect{
			"branch":     {class: Write, deterministic: true},
			"diff":       {class: Read, deterministic: true},
			"commit":     {class: Write, deterministic: true},
			"push":       {class: External, deterministic: false},
			"open_pr":    {class: External, deterministic: false},
			"pr_status":  {class: Read, deterministic: false},
			"pr_comment": {class: External, deterministic: false},
		},
	},

	// host.gh.ticket — GitHub issue ops: read-only queries vs. mutations
	// that land on GitHub (create/comment/comment_edit/transition), which
	// are External (irreversible, third-party-visible).
	"host.gh.ticket": {
		class: External, deterministic: false,
		ops: map[string]opEffect{
			"create":       {class: External, deterministic: false},
			"search":       {class: Read, deterministic: false},
			"get":          {class: Read, deterministic: false},
			"comment":      {class: External, deterministic: false},
			"comment_edit": {class: External, deterministic: false},
			"transition":   {class: External, deterministic: false},
			"list_mine":    {class: Read, deterministic: false},
		},
	},

	// host.local_files.ticket — the filesystem-backed ticket provider
	// mirrors host.gh.ticket's op vocabulary, but every mutation stays
	// local (Write, not External).
	"host.local_files.ticket": {
		class: Write, deterministic: true,
		ops: map[string]opEffect{
			"search":     {class: Read, deterministic: true},
			"get":        {class: Read, deterministic: true},
			"comment":    {class: Write, deterministic: true},
			"transition": {class: Write, deterministic: true},
			"list_mine":  {class: Read, deterministic: true},
		},
	},

	// host.local_github.ticket — dogfood composite provider. Reads combine
	// local files with GitHub Issues; mutations may target GitHub when the
	// selected ticket is remote, so classify mutating ops as External.
	"host.local_github.ticket": {
		class: External, deterministic: false,
		ops: map[string]opEffect{
			"create":       {class: External, deterministic: false},
			"search":       {class: Read, deterministic: false},
			"get":          {class: Read, deterministic: false},
			"comment":      {class: External, deterministic: false},
			"comment_edit": {class: External, deterministic: false},
			"transition":   {class: External, deterministic: false},
			"list_mine":    {class: Read, deterministic: false},
		},
	},

	// host.ticket_federation composes explicitly marked providers. A read can
	// fan out to remote services, while any mutation may select such a remote
	// source; therefore reads are non-deterministic Read and mutations fail
	// closed to External.
	"host.ticket_federation": {
		class: External, deterministic: false,
		ops: map[string]opEffect{
			"create":            {class: External, deterministic: false},
			"search":            {class: Read, deterministic: false},
			"get":               {class: Read, deterministic: false},
			"comment":           {class: External, deterministic: false},
			"comment_edit":      {class: External, deterministic: false},
			"comment_reactions": {class: Read, deterministic: false},
			"transition":        {class: External, deterministic: false},
			"list_mine":         {class: Read, deterministic: false},
		},
	},

	// host.local — local CI provider. run_tests/build execute local
	// commands whose outcome can vary run to run; remote_status is a
	// read-only query.
	"host.local": {
		class: Write, deterministic: false,
		ops: map[string]opEffect{
			"run_tests":     {class: Write, deterministic: false},
			"build":         {class: Write, deterministic: false},
			"remote_status": {class: Read, deterministic: false},
		},
	},

	// host.cypilot_artifacts — SDLC artifact provider (cpt CLI).
	"host.cypilot_artifacts": {
		class: Write, deterministic: true,
		ops: map[string]opEffect{
			"list":      {class: Read, deterministic: true},
			"get":       {class: Read, deterministic: true},
			"create":    {class: Write, deterministic: true},
			"validate":  {class: Read, deterministic: true},
			"decompose": {class: Write, deterministic: true},
		},
	},

	// host.graph — S5 (.context/kits-implementation-plan.md D1/D4): the
	// project object graph engine substrate (internal/graph) exposed as a
	// generic host verb. load/lint/diff/project read a catalog off disk;
	// apply writes a changeset's operations back to it; presentation
	// delegates to a kit-shipped starlark script (the D2.1 mechanism) that
	// serves presentation-layer data — deterministic (cassette-replayable)
	// but not itself a further host call.
	"host.graph": {
		class: Read, deterministic: true, // fallback for an unrecognised op
		ops: map[string]opEffect{
			"load":         {class: Read, deterministic: true},
			"lint":         {class: Read, deterministic: true},
			"diff":         {class: Read, deterministic: true},
			"query":        {class: Write, deterministic: true},
			"project":      {class: Read, deterministic: true},
			"snapshot":     {class: Read, deterministic: true},
			"apply":        {class: Write, deterministic: true},
			"presentation": {class: Read, deterministic: true},
			"propose":      {class: Write, deterministic: true},
			"authorize":    {class: Write, deterministic: true},
			"withdraw":     {class: Write, deterministic: true},
			"rebase":       {class: Write, deterministic: true},
		},
	},

	// host.runstatus snapshots an injected durable store. It performs no
	// process probing or writes, and its projection is deterministic for the
	// captured store state.
	"host.runstatus": {
		class: Read, deterministic: true,
		ops: map[string]opEffect{
			"snapshot": {class: Read, deterministic: true},
		},
	},
	// Machine effects classify the fully-qualified invoke before registry
	// prefix fallback injects args["op"], so keep the leaf classified too.
	"host.runstatus.snapshot": {class: Read, deterministic: true},

	"host.streams": {
		class: Read, deterministic: true,
		ops: map[string]opEffect{
			"snapshot": {class: Read, deterministic: true},
		},
	},
	"host.streams.snapshot": {class: Read, deterministic: true},
	"host.federation": {
		class: Read, deterministic: true,
		ops: map[string]opEffect{
			"snapshot": {class: Read, deterministic: true},
		},
	},
	"host.federation.snapshot": {class: Read, deterministic: true},
	"host.materialization": {
		class: Read, deterministic: true,
		ops: map[string]opEffect{
			"snapshot": {class: Read, deterministic: true},
		},
	},
	"host.materialization.snapshot": {class: Read, deterministic: true},

	// host.feedback reads a captured reviewed-report view or asks an injected
	// governed backend to create an idempotent dispatch job. Dispatch grants no
	// source-landing authority.
	"host.feedback": {
		class: External, deterministic: false,
		ops: map[string]opEffect{
			"list_reviewed": {class: Read, deterministic: true},
			"dispatch":      {class: External, deterministic: false},
		},
	},
	"host.feedback.list_reviewed":               {class: Read, deterministic: true},
	"host.feedback.dispatch":                    {class: External, deterministic: false},
	"host.reviewed_feedback_campaign.reconcile": {class: External, deterministic: false},
	"host.feedback_intake.reconcile":            {class: Write, deterministic: false},
	"host.feedback_federation.reconcile":        {class: External, deterministic: false},

	// Story Application maintenance providers carry no caller authority.
	// Session reconciliation may interrupt local process-owned stale rows;
	// worker fleet and campaign supervision expose bounded observations only.
	"host.session_reconciliation.reconcile": {class: Write, deterministic: false},
	"host.worker_fleet.reconcile":           {class: External, deterministic: false},
	"host.campaign_supervision.reconcile":   {class: Read, deterministic: true},

	// host.compliance evaluates server-resolved Starlark materialize checks and
	// writes immutable local evidence. It has no command, LLM, or network lane.
	"host.compliance": {
		class: Write, deterministic: true,
		ops: map[string]opEffect{
			"run": {class: Write, deterministic: true},
		},
	},
	"host.compliance.run": {class: Write, deterministic: true},

	// host.campaign reads an app-scoped graph and starts exact story intents
	// through the daemon. The target intent may be external, so watch fails
	// closed to the highest tier. Snapshot reads only durable control state.
	"host.campaign": {
		class: External, deterministic: false,
		ops: map[string]opEffect{
			"watch":    {class: External, deterministic: false},
			"snapshot": {class: Read, deterministic: true},
		},
	},
	"host.campaign.watch":    {class: External, deterministic: false},
	"host.campaign.snapshot": {class: Read, deterministic: true},

	// host.work_queue is application-scoped durable control state. Enqueue is
	// idempotent and does not execute work; get/snapshot are redacted reads.
	"host.work_queue": {
		class: Write, deterministic: true,
		ops: map[string]opEffect{
			"enqueue":  {class: Write, deterministic: true},
			"get":      {class: Read, deterministic: true},
			"snapshot": {class: Read, deterministic: true},
		},
	},
	"host.work_queue.enqueue":  {class: Write, deterministic: true},
	"host.work_queue.get":      {class: Read, deterministic: true},
	"host.work_queue.snapshot": {class: Read, deterministic: true},

	// Worker leases are durable control-state mutation. Claim is intentionally
	// a write because it allocates a lease even though it returns a payload.
	"host.work_queue_worker": {
		class: Write, deterministic: false,
		ops: map[string]opEffect{
			"claim":     {class: Write, deterministic: false},
			"heartbeat": {class: Write, deterministic: false},
			"complete":  {class: Write, deterministic: false},
			"fail":      {class: Write, deterministic: false},
		},
	},
	"host.work_queue_worker.claim":     {class: Write, deterministic: false},
	"host.work_queue_worker.heartbeat": {class: Write, deterministic: false},
	"host.work_queue_worker.complete":  {class: Write, deterministic: false},
	"host.work_queue_worker.fail":      {class: Write, deterministic: false},

	// host.flow_evidence runs only server-resolved deterministic Kitsoki flow
	// suites and writes a durable, idempotent evidence receipt.
	"host.flow_evidence": {
		class: Write, deterministic: false,
		ops: map[string]opEffect{
			"record": {class: Write, deterministic: false},
		},
	},
	"host.flow_evidence.record": {class: Write, deterministic: false},

	// host.application_conversation.ask performs an LLM call through a
	// daemon-owned binding and persists its transcript and replay receipt.
	"host.application_conversation": {
		class: External, deterministic: false,
		ops: map[string]opEffect{
			"ask": {class: External, deterministic: false},
		},
	},
	"host.application_conversation.ask": {class: External, deterministic: false},

	// host.queue — capsule merge queue operator surface
	// (internal/host/queue_handlers.go over internal/capsule/queue). status
	// reads the durable state file; the six operator verbs mutate it under
	// the state lock, appending audited evidence. All local-file, no LLM —
	// but evidence lines carry wall-clock timestamps, so mutations are not
	// replay-deterministic.
	"host.queue": {
		class: Write, deterministic: false, // fallback for an unrecognised op
		ops: map[string]opEffect{
			"status":    {class: Read, deterministic: true},
			"kick":      {class: Write, deterministic: false},
			"park":      {class: Write, deterministic: false},
			"resume":    {class: Write, deterministic: false},
			"emergency": {class: Write, deterministic: false},
			"override":  {class: Write, deterministic: false},
			"reject":    {class: Write, deterministic: false},
		},
	},

	// host.demo is replaced in Story Application runtimes by an app-scoped
	// typed provider. The legacy create operation remains classified for the
	// deprecated path-based compatibility handler.
	"host.demo": {
		class: Write, deterministic: false, // fallback for an unrecognised op
		ops: map[string]opEffect{
			"plan":           {class: Read, deterministic: true},
			"materialize":    {class: Write, deterministic: false},
			"project_mockup": {class: Read, deterministic: true},
			"create_mockup":  {class: Write, deterministic: true},
			"record":         {class: Write, deterministic: false},
			"doctor":         {class: Read, deterministic: true},
			"create":         {class: Write, deterministic: true},
		},
	},
	"host.demo.plan":           {class: Read, deterministic: true},
	"host.demo.materialize":    {class: Write, deterministic: false},
	"host.demo.project_mockup": {class: Read, deterministic: true},
	"host.demo.create_mockup":  {class: Write, deterministic: true},
	"host.demo.record":         {class: Write, deterministic: false},
	"host.demo.doctor":         {class: Read, deterministic: true},
}

// ClassifyVerb returns the default (effect, deterministic) pair for a
// registered host verb given its call args (consulted only for the "op" key,
// on the handful of verbs that dispatch multiple operations — see
// builtinVerbTable). An unregistered/unclassified verb fails closed to
// (External, false) — an unknown capability is assumed maximally privileged
// and non-repeatable, never silently trusted.
func ClassifyVerb(namespace string, args map[string]any) (Effect, bool) {
	entry, ok := builtinVerbTable[namespace]
	if !ok {
		return External, false
	}
	if entry.ops != nil {
		if op, _ := args["op"].(string); op != "" {
			if oe, ok := entry.ops[op]; ok {
				return oe.class, oe.deterministic
			}
		}
	}
	return entry.class, entry.deterministic
}

// RegisteredVerbs returns the sorted set of verb names this package
// classifies. Used by internal/host's coverage test to assert every verb
// RegisterBuiltins registers has an entry here.
func RegisteredVerbs() map[string]bool {
	out := make(map[string]bool, len(builtinVerbTable))
	for name := range builtinVerbTable {
		out[name] = true
	}
	return out
}
