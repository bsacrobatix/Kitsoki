// Package host — host.agent.codeact handler.
//
// host.agent.codeact drives the bounded code-act loop in internal/host/codeact:
// instead of a fixed tool menu, the agent emits a Starlark snippet each step,
// observes its structured result, and eventually terminates by emitting
// done(payload) that must conform to the declared schema. See
// internal/host/codeact/executor.go for the loop itself and
// docs/goals/codeact/GOAL.md for the wider design.
//
// Mandatory args:
//   - agent: (string) — named agent from the agents: block.
//   - goal:  (string) — the natural-language objective handed to the agent.
//   - capabilities: ([]string) — the ctx proxy allow-list for this call;
//     validated at load time against the sanctioned v1 set (see
//     internal/app/loader.go's codeactCapabilityAllowlist). Not yet enforced
//     at runtime by this handler — see TODO below.
//   - budget: (int) — max steps before TerminatedBudgetExhausted.
//   - schema: (string) — path to the JSON schema validating done() payloads,
//     resolved overlay-first via resolvePromptPathCtx (same convention as
//     host.agent.task's acceptance.schema). When set, a schema-invalid done()
//     payload is rejected and fed back to the agent as an ErrorEnvelope
//     instead of terminating the loop. Optional — when omitted, done()
//     payloads are accepted unvalidated.
//
// Returns Result.Data with:
//   - terminated (string): "done" | "budget_exhausted".
//   - payload    (map[string]any): the schema-valid done() payload (empty
//     when terminated == "budget_exhausted").
//   - steps      ([]any): a journal of each step's snippet + observation, for
//     tracing/debugging.
//
// v1 STATUS: the Agent wired in here (RealCodeactAgent, see
// agent_codeact_real.go) drives one ONE-SHOT `claude -p` call per step,
// composing the Codeact verb contract (sysprompt.Codeact) plus a per-step
// addendum (goal, remaining budget, granted capability names, and the last
// observation or structured error), and reads back a discriminated-union
// {"action":"snippet"|"done",...} verdict through the same mcp-validator
// submit mechanism host.agent.decide uses. Runtime enforcement of the
// with.capabilities allowlist against the actual ctx proxies exposed to a
// snippet is follow-up work (see docs/goals/codeact/decomposition.yaml, later
// slices) — v1 only tells the model the granted names.
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	kitsokimcp "kitsoki/internal/mcp"

	"kitsoki/internal/host/codeact"
)

// defaultCodeactBudget is used when with.budget is absent or non-positive.
const defaultCodeactBudget = 5

// AgentCodeactHandler implements host.agent.codeact.
func AgentCodeactHandler(ctx context.Context, args map[string]any) (Result, error) {
	agentName, _ := args["agent"].(string)
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return Result{Error: "host.agent.codeact: agent: argument is required — declare a named agent in the agents: block", FailureKind: FailureFatal}, nil
	}
	agent, agentOK := resolveAgent(ctx, args)
	if !agentOK {
		return Result{Error: fmt.Sprintf("host.agent.codeact: unknown agent %q — check the agents: block in app.yaml", agentName), FailureKind: FailureFatal}, nil
	}

	goal, _ := args["goal"].(string)
	if strings.TrimSpace(goal) == "" {
		return Result{Error: "host.agent.codeact: goal: argument is required", FailureKind: FailureFatal}, nil
	}
	workingDir, _ := args["working_dir"].(string)
	workingDir = appendDefaultCwd(workingDir, agent)
	if _, policyErr := RequireAgentLaunchAllowed(ctx, "codeact", agentName, workingDir); policyErr != "" {
		return Result{Error: "host.agent.codeact: " + policyErr, FailureKind: FailureFatal}, nil
	}

	budget := defaultCodeactBudget
	switch v := args["budget"].(type) {
	case int:
		if v > 0 {
			budget = v
		}
	case int64:
		if v > 0 {
			budget = int(v)
		}
	case float64:
		if v > 0 {
			budget = int(v)
		}
	}

	var capabilities []string
	switch v := args["capabilities"].(type) {
	case []any:
		for _, c := range v {
			if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
				capabilities = append(capabilities, s)
			}
		}
	case []string:
		capabilities = v
	}

	worldArg, _ := args["world"].(map[string]any)

	var schemaFn func(payload map[string]any) error
	if schemaPath, _ := args["schema"].(string); strings.TrimSpace(schemaPath) != "" {
		fn, err := compileCodeactSchema(ctx, schemaPath)
		if err != nil {
			return Result{Error: fmt.Sprintf("host.agent.codeact: %v", err), FailureKind: FailureFatal}, nil
		}
		schemaFn = fn
	}

	// Direct-API path: when the effect's resolved plugin is a builtin.local_llm
	// transport (an OpenAI-compatible HTTP endpoint, e.g. GLM-5.2), drive the
	// loop over HTTP via ApiCodeactAgent instead of forking claude -p per step.
	// Opt-in: only when a plugin is named AND it resolves to a local_llm
	// backend. The default (no plugin named, or a claude_cli plugin) stays on
	// the CLI path below — IsLocalLLM("") and IsLocalLLM("agent.claude") are
	// both false, so existing stories are byte-identical. See
	// agent_codeact_api.go and .context/diy-harness-sandboxing-brief.md item 1.
	if reg := AgentRegistryFromCtx(ctx); reg != nil {
		if pluginName := AgentPluginNameFromCtx(ctx); reg.IsLocalLLM(pluginName) {
			plug, perr := reg.Resolve(pluginName)
			if perr != nil {
				return Result{Error: fmt.Sprintf("host.agent.codeact: resolve api plugin %q: %v", pluginName, perr), FailureKind: FailureInfra}, nil
			}
			return runCodeactLoop(ctx, worldArg, schemaFn, newApiCodeactAgent(ctx, plug, args, goal, budget, capabilities), budget)
		}
	}

	agentImpl, cleanup, err := newRealCodeactAgent(ctx, args, goal, budget, capabilities)
	if err != nil {
		return Result{Error: fmt.Sprintf("host.agent.codeact: %v", err), FailureKind: FailureInfra}, nil
	}
	defer cleanup()
	return runCodeactLoop(ctx, worldArg, schemaFn, agentImpl, budget)
}

// runCodeactLoop runs codeact.Run over impl and shapes the Result identically
// for both the CLI (RealCodeactAgent) and direct-API (ApiCodeactAgent) paths:
// terminated (done|budget_exhausted), the schema-valid done() payload, and a
// per-step journal of snippet + observation (+ error). Keeping the shaping in
// one place guarantees the two backends produce the same Result.Data shape.
func runCodeactLoop(ctx context.Context, worldArg map[string]any, schemaFn func(map[string]any) error, impl codeact.Agent, budget int) (Result, error) {
	res, err := codeact.Run(ctx, codeact.Params{
		Budget: budget,
		World:  worldArg,
		Agent:  impl,
		Schema: schemaFn,
	})
	if err != nil {
		return Result{}, fmt.Errorf("host.agent.codeact: %w", err)
	}

	steps := make([]any, 0, len(res.Steps))
	for _, s := range res.Steps {
		step := map[string]any{
			"snippet":     s.Emission.Snippet,
			"observation": s.Observation,
		}
		if s.Err != nil {
			step["error"] = s.Err.Message
		}
		steps = append(steps, step)
	}

	return Result{
		Data: map[string]any{
			"terminated": res.Terminated,
			"payload":    res.Payload,
			"steps":      steps,
		},
	}, nil
}

// compileCodeactSchema loads and compiles the JSON schema at schemaPath
// (resolved overlay-first via resolvePromptPathCtx, the same convention
// host.agent.task's acceptance.schema and host.agent.decide's schema use) and
// returns a codeact.Params.Schema validator func. The returned func marshals
// the candidate done() payload to JSON and validates it against the compiled
// schema, so a schema-invalid payload is rejected and fed back to the agent
// as an ErrorEnvelope instead of silently terminating the loop.
func compileCodeactSchema(ctx context.Context, schemaPath string) (func(map[string]any) error, error) {
	resolved := resolvePromptPathCtx(ctx, schemaPath)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("schema %q not found: %w", resolved, err)
	}
	compiled, err := kitsokimcp.CompileSchema(data)
	if err != nil {
		return nil, fmt.Errorf("compile schema %q: %w", resolved, err)
	}
	return func(payload map[string]any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("unmarshal payload: %w", err)
		}
		if err := compiled.Validate(doc); err != nil {
			return fmt.Errorf("%s", kitsokimcp.FormatValidationError(err))
		}
		return nil
	}, nil
}
