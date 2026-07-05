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
// v1 STATUS: the Agent implementation wired in here (codeactStubAgent) is a
// placeholder that immediately emits done() with an empty payload — there is
// no live LLM loop yet. Flow-fixture tests stub host.agent.codeact entirely
// at the host_handlers: layer (see
// internal/testrunner/testdata/codeact_demo/flows/happy_path.yaml), so they
// never exercise this path. Wiring a real LLM-backed codeact.Agent is
// follow-up work (see docs/goals/codeact/decomposition.yaml, later slices).
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

	worldArg, _ := args["world"].(map[string]any)

	res, err := codeact.Run(ctx, codeact.Params{
		Budget: budget,
		World:  worldArg,
		Agent:  codeactStubAgent{goal: goal},
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

// codeactStubAgent is the v1 placeholder Agent: it immediately terminates
// with an empty done() payload on the first step, without calling any LLM.
// TODO(codeact-s3+): replace with a real LLM-backed Agent that emits
// Starlark snippets scoped to the declared with.capabilities allowlist.
type codeactStubAgent struct {
	goal string
}

func (a codeactStubAgent) Next(_ context.Context, _ int, _ map[string]any, _ *codeact.ErrorEnvelope) (codeact.Emission, error) {
	return codeact.Emission{Done: true, Payload: map[string]any{}}, nil
}
