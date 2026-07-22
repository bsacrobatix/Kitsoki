package storylauncher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"kitsoki/internal/app"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/effect"
	"kitsoki/internal/harness"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

type Launcher struct {
	StoryPath      string
	ProjectRoot    string
	ConfigureHosts func(*host.Registry) error
	// EventSink receives the real story/orchestrator and host.agent.* events.
	// Capsule workers supply a durable JSONL sink so a process stall leaves the
	// last dispatched host call and agent breadcrumb on disk.
	EventSink store.EventSink
	// AgentBackend is the only coding-agent backend the worker dispatches. Empty
	// preserves the existing Claude default.
	AgentBackend string
	// AgentLaunchPolicy confines any story-declared agent call to the
	// materialized Capsule source. The zero value keeps no launch policy.
	AgentLaunchPolicy host.AgentLaunchPolicy
}

func (l Launcher) Launch(ctx context.Context, prepared executor.Prepared) (ci.Verdict, error) {
	if l.StoryPath == "" {
		return ci.Verdict{}, fmt.Errorf("capsule ci: story path is required")
	}
	path, err := filepath.Abs(l.StoryPath)
	if err != nil {
		return ci.Verdict{}, err
	}
	def, err := app.Load(path)
	if err != nil {
		return ci.Verdict{}, fmt.Errorf("capsule ci: load story: %w", err)
	}
	m, err := machine.New(def)
	if err != nil {
		return ci.Verdict{}, err
	}
	s, err := store.OpenMemory()
	if err != nil {
		return ci.Verdict{}, err
	}
	defer s.Close()
	reg := host.NewRegistry()
	host.RegisterBuiltins(reg)
	host.RegisterStarlarkBindings(reg, def.StarlarkHostBindings)
	if l.ConfigureHosts != nil {
		if err := l.ConfigureHosts(reg); err != nil {
			return ci.Verdict{}, err
		}
	}
	// Apply the sealed policy last so a cassette/test or embedding hook cannot
	// accidentally replace a guarded host.agent.* handler and bypass CI policy.
	if err := applyAgentPolicy(reg, prepared.Envelope.Policy.Agents); err != nil {
		return ci.Verdict{}, err
	}
	applyHostEffectPolicy(reg, prepared.Envelope.Policy)
	if err := reg.ValidateAllowList(def.Hosts); err != nil {
		return ci.Verdict{}, err
	}
	opts := []orchestrator.Option{orchestrator.WithHostRegistry(reg)}
	if l.EventSink != nil {
		opts = append(opts, orchestrator.WithEventSink(l.EventSink), orchestrator.WithEventSinkAuthority(true))
	}
	if l.AgentBackend != "" {
		opts = append(opts, orchestrator.WithAgentBackendName(l.AgentBackend))
	}
	if l.AgentLaunchPolicy.Enabled {
		opts = append(opts, orchestrator.WithAgentLaunchPolicy(l.AgentLaunchPolicy))
	}
	orch := orchestrator.New(def, m, s, directHarness{}, opts...)
	projectRoot := l.ProjectRoot
	if projectRoot == "" {
		projectRoot = findProjectRoot(path)
	}
	// Drive the story THROUGH its rooms to a terminal verdict (not a single
	// operator turn): a CI/worker run has no human to advance each phase, so the
	// bugfix loop per-room emit_intent auto-advances and gate self-arcs must
	// settle to rest. DriveToRest is the existing multi-round drive (its
	// WithInterceptDrive re-fires effectful self-arc on_enter); WorldAfter
	// exposes the settled world so we can read the ci_verdict the story emitted.
	out, err := orch.DriveToRest(ctx, "run", nil, orchestrator.DriveOptions{InitialWorld: envelopeWorld(prepared.Envelope, projectRoot), TeleportState: app.StatePath(fmt.Sprint(def.Root))})
	if err != nil {
		return ci.Verdict{}, err
	}
	if limit := prepared.Envelope.Policy.Agents.MaxCostUSD; limit > 0 {
		if observed := numeric(out.WorldAfter["session_cost_usd"]); observed > limit {
			return ci.Verdict{}, fmt.Errorf("capsule ci: agent cost %.6f exceeded sealed budget %.6f", observed, limit)
		}
	}
	raw, ok := out.WorldAfter["ci_verdict"]
	if !ok || raw == nil {
		// The story settled without ever emitting a ci_verdict — it stalled at a
		// (usually non-terminal) room instead of driving to a terminal @exit.
		// DriveToRest folds a broken emit chain into outcome=resolved and leaves
		// the real cause on Last.HarnessError, so without surfacing it here the
		// worker fails with a bare, undebuggable `verdict schema ""`. Name the
		// stuck room AND why nothing advanced it so the bucket-mirrored error is
		// self-diagnosing (no SSH-to-worker required).
		return ci.Verdict{}, stalledVerdictError(l.StoryPath, out)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ci.Verdict{}, err
	}
	var verdict ci.Verdict
	if err := json.Unmarshal(encoded, &verdict); err != nil {
		return ci.Verdict{}, fmt.Errorf("capsule ci: parse story verdict: %w", err)
	}
	return verdict, nil
}

// applyHostEffectPolicy is installed after builtins, embedding hooks, and the
// agent guard so no later replacement can reopen an external-effect handler.
// Unknown handlers classify as External and therefore fail closed under deny.
func applyHostEffectPolicy(reg *host.Registry, policy executor.Policy) {
	for _, namespace := range reg.Names() {
		original, ok := reg.Get(namespace)
		if !ok {
			continue
		}
		name := namespace
		reg.Replace(name, func(ctx context.Context, args map[string]any) (host.Result, error) {
			class, _ := effect.ClassifyVerb(name, args)
			if class == effect.External && policy.ExternalWrite != "allow" {
				return host.Result{Error: fmt.Sprintf("capsule ci: host effect %q is denied by the sealed external-write policy", name), FailureKind: host.FailureFatal}, nil
			}
			return original(ctx, args)
		})
	}
}

// stalledVerdictError builds the self-diagnosing error returned when a drive
// settles without ci_verdict. It names the stuck room (out.FinalState) and the
// drive outcome, then appends stallDiagnostics — so the failure that the worker
// mirrors to the bucket explains the stall instead of the bare, causeless
// `capsule ci: verdict schema ""`.
func stalledVerdictError(storyPath string, out orchestrator.DriveOutcome) error {
	return fmt.Errorf("capsule ci: story %s stalled without emitting ci_verdict — final state %s (outcome %s); %s",
		filepath.ToSlash(storyPath), out.FinalState, out.Outcome, stallDiagnostics(out))
}

// stallDiagnostics explains WHY a drive settled without emitting ci_verdict.
// It surfaces the swallowed settle error (the emit_intent / transition failure
// the machine recorded but DriveToRest reports as outcome=resolved) plus the
// last agent verdict the story bound, so the stall is debuggable from the
// mirrored error alone.
func stallDiagnostics(out orchestrator.DriveOutcome) string {
	parts := []string{fmt.Sprintf("%d round(s)", out.Rounds)}
	// The settle error is the real "why": e.g. `emit_intent "accept" at
	// "verdict_not_reproducible": no transition arm matched`. It is otherwise
	// lost because DriveToRest classifies the turn as resolved.
	if out.Last != nil && strings.TrimSpace(out.Last.HarnessError) != "" {
		parts = append(parts, "settle error: "+strings.TrimSpace(out.Last.HarnessError))
	}
	if v := lastAgentVerdict(out.WorldAfter); v != "" {
		parts = append(parts, "last agent verdict: "+v)
	} else {
		parts = append(parts, "no agent verdict bound")
	}
	return strings.Join(parts, "; ")
}

// lastAgentVerdict summarises the judge/triage verdicts the story bound (world
// keys ending in "verdict", excluding ci_verdict) — e.g.
// "bf__llm_verdict(verdict=accept intent=accept confidence=0.95)" — so a stall
// can report "a verdict was accepted but no advance intent fired".
func lastAgentVerdict(world map[string]any) string {
	keys := make([]string, 0, len(world))
	for k := range world {
		if k == "ci_verdict" {
			continue
		}
		if strings.HasSuffix(k, "verdict") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		m, ok := world[k].(map[string]any)
		if !ok {
			continue
		}
		fields := make([]string, 0, 3)
		for _, f := range []string{"verdict", "intent", "confidence"} {
			if val, present := m[f]; present {
				fields = append(fields, fmt.Sprintf("%s=%v", f, val))
			}
		}
		if len(fields) > 0 {
			parts = append(parts, k+"("+strings.Join(fields, " ")+")")
		}
	}
	return strings.Join(parts, ", ")
}

func hostCallDiagnostics(calls []orchestrator.HostCallSummary) string {
	if len(calls) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		status := "ok"
		if call.Error != "" {
			status = call.Error
		}
		parts = append(parts, call.Namespace+"="+status)
	}
	return strings.Join(parts, ", ")
}
func envelopeWorld(e executor.Envelope, projectRoot string) map[string]any {
	trigger := make(map[string]any, len(e.Trigger)+2)
	for key, value := range e.Trigger {
		trigger[key] = value
	}
	trigger["envelope_digest"] = e.Digest
	trigger["story_digest"] = e.StoryDigest
	return map[string]any{"ci_job_id": e.JobID, "ci_pipeline": trigger["requested_pipeline"], "ci_trigger": trigger, "ci_source": map[string]any{"digest": e.SourceDigest}, "ci_workspace": map[string]any{"id": e.Instance.ID, "generation": e.Instance.Generation, "path": projectRoot}, "ci_environment": map[string]any{"id": e.Environment.ID, "digest": e.Environment.Digest}, "ci_policy": map[string]any{"network": e.Policy.Network, "external_write": e.Policy.ExternalWrite, "command_timeout": e.Policy.CommandTimeout, "agents": map[string]any{"policy": e.Policy.Agents.Policy, "profiles": e.Policy.Agents.Profiles, "max_cost_usd": e.Policy.Agents.MaxCostUSD, "on_unavailable": e.Policy.Agents.OnUnavailable}}}
}

func findProjectRoot(storyPath string) string {
	dir := filepath.Dir(storyPath)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".kitsoki", "project-profile.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(storyPath)
		}
		dir = parent
	}
}

// ProjectRootForStory returns the enclosing project root used by the launcher
// for workspace confinement. It is exported for trusted front doors that
// construct a launcher from a project-relative story path but must install the
// same agent launch boundary as the remote worker.
func ProjectRootForStory(storyPath string) string { return findProjectRoot(storyPath) }

func applyAgentPolicy(reg *host.Registry, policy executor.AgentPolicy) error {
	posture := policy.Policy
	if posture == "" {
		posture = "deny"
	}
	if posture != "deny" && posture != "allow" {
		return fmt.Errorf("capsule ci: invalid sealed agent policy %q", posture)
	}
	allowed := map[string]bool{}
	for _, profile := range policy.Profiles {
		profile = strings.TrimSpace(profile)
		if profile != "" {
			allowed[profile] = true
		}
	}
	for _, namespace := range []string{"host.agent.ask", "host.agent.extract", "host.agent.decide", "host.agent.task", "host.agent.converse", "host.agent.codeact"} {
		original, ok := reg.Get(namespace)
		if !ok {
			continue
		}
		reg.Replace(namespace, func(ctx context.Context, args map[string]any) (host.Result, error) {
			if posture == "deny" {
				return host.Result{Error: "capsule ci: agent calls are denied by the sealed pipeline policy", FailureKind: host.FailureFatal}, nil
			}
			selected := agentProfiles(args)
			if len(selected) == 0 {
				return host.Result{Error: "capsule ci: an explicit agent profile is required by the sealed allowlist", FailureKind: host.FailureFatal}, nil
			}
			for _, profile := range selected {
				if !allowed[profile] {
					return host.Result{Error: fmt.Sprintf("capsule ci: agent profile %q is outside the sealed allowlist", profile), FailureKind: host.FailureFatal}, nil
				}
			}
			if policy.MaxCostUSD > 0 {
				spent := numeric(host.WorldSnapshotFromContext(ctx)["session_cost_usd"])
				if spent >= policy.MaxCostUSD {
					return host.Result{Error: fmt.Sprintf("capsule ci: sealed agent budget %.6f is exhausted", policy.MaxCostUSD), FailureKind: host.FailureFatal}, nil
				}
			}
			return original(ctx, args)
		})
	}
	return nil
}

func agentProfiles(values map[string]any) []string {
	seen := map[string]bool{}
	profiles := make([]string, 0, 1)
	add := func(value any) {
		profile := strings.TrimSpace(fmt.Sprint(value))
		if profile != "" && profile != "<nil>" && !seen[profile] {
			seen[profile] = true
			profiles = append(profiles, profile)
		}
	}
	add(values["agent"])
	// host.agent.extract may declare several LLM resolvers. Checking only its
	// top-level field would let a nested resolver select an unsealed profile.
	if resolvers, ok := values["resolvers"].([]any); ok {
		for _, raw := range resolvers {
			resolver, _ := raw.(map[string]any)
			llm, _ := resolver["llm"].(map[string]any)
			add(llm["agent"])
		}
	}
	return profiles
}

func numeric(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

type directHarness struct{}

func (directHarness) RunTurn(context.Context, harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, fmt.Errorf("capsule ci: direct story invoked harness unexpectedly")
}
func (directHarness) Close() error { return nil }
