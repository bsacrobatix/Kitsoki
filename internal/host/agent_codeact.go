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
//   - schema: (string) — path to the JSON schema validating done() payloads.
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
	"fmt"
	"strings"

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
	if _, agentOK := resolveAgent(ctx, args); !agentOK {
		return Result{Error: fmt.Sprintf("host.agent.codeact: unknown agent %q — check the agents: block in app.yaml", agentName), FailureKind: FailureFatal}, nil
	}

	goal, _ := args["goal"].(string)
	if strings.TrimSpace(goal) == "" {
		return Result{Error: "host.agent.codeact: goal: argument is required", FailureKind: FailureFatal}, nil
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

	agentImpl, cleanup, err := newRealCodeactAgent(ctx, args, goal, budget, capabilities)
	if err != nil {
		return Result{Error: fmt.Sprintf("host.agent.codeact: %v", err), FailureKind: FailureInfra}, nil
	}
	defer cleanup()

	res, err := codeact.Run(ctx, codeact.Params{
		Budget: budget,
		World:  worldArg,
		Agent:  agentImpl,
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
