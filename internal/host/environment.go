package host

import (
	"context"
	"fmt"
	"sort"

	"kitsoki/internal/environment"
)

// EnvironmentHandler is the Story adapter for the provider-neutral environment
// boundary. The builtin is deliberately unavailable until session construction
// injects an operator-configured reader/controller; it never constructs a
// provider from Story arguments.
func EnvironmentHandler(ctx context.Context, args map[string]any) (Result, error) {
	return NewEnvironmentHandler(nil, nil)(ctx, args)
}

func NewEnvironmentHandler(plans environment.PlanInputReader, controller environment.Host) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		op, _ := args["op"].(string)
		switch op {
		case "plan_inputs":
			if plans == nil {
				return unavailableEnvironmentResult(), nil
			}
			inputs, err := plans.PlanInputs(ctx)
			if err != nil {
				return Result{}, fmt.Errorf("host.environment.plan_inputs: %w", err)
			}
			return Result{Data: planInputsData(inputs)}, nil
		case "read":
			if controller == nil {
				return unavailableEnvironmentResult(), nil
			}
			resourceRef, _ := args["resource_ref"].(string)
			out, err := controller.Read(ctx, environment.ReadRequest{ResourceRef: resourceRef})
			if err != nil {
				return Result{}, fmt.Errorf("host.environment.read: %w", err)
			}
			return Result{Data: map[string]any{"outcome": outcomeData(out.Outcome), "resource": resourceData(out.Resource)}}, nil
		case "submit", "poll", "verify":
			// These operations have typed Go methods now but intentionally no loose
			// map decoder yet. A provider adapter must be wired with its concrete
			// topology action vocabulary before it becomes Story-callable.
			if controller == nil {
				return unavailableEnvironmentResult(), nil
			}
			return Result{Error: "host.environment: operation is not configured for this session"}, nil
		default:
			return Result{Error: fmt.Sprintf("host.environment: unknown op %q (want plan_inputs, read, submit, poll, or verify)", op)}, nil
		}
	}
}

func unavailableEnvironmentResult() Result {
	outcome := environment.Blocked(environment.ReasonUnavailable, "environment host is unavailable outside a configured deployment session", "configure an environment plan-input reader or provider adapter")
	return Result{Data: map[string]any{"outcome": outcomeData(outcome)}, Error: outcome.Reason.Message}
}

func planInputsData(inputs environment.PlanInputs) map[string]any {
	documents := make([]any, 0, len(inputs.ProfileDocuments))
	for _, profile := range inputs.ProfileDocuments {
		documents = append(documents, map[string]any{"source": map[string]any{"path": profile.Source.Path, "digest": profile.Source.Digest}, "document": profile.Document})
	}
	return map[string]any{
		"profile_documents": documents,
		"integrity":         map[string]any{"outcome": integrityOutcome(inputs.Integrity), "verified": inputs.Integrity.Status == environment.OutcomePassed, "manifest_digest": inputs.ManifestDigest, "diagnostic": outcomeData(inputs.Integrity)},
		"observations": map[string]any{
			"dns":        map[string]any{"resolved_ips": stringsData(inputs.Observations.DNS.ResolvedIPs)},
			"deployment": map[string]any{"healthy": inputs.Observations.Deployment.Healthy, "current": inputs.Observations.Deployment.Current},
			"migration":  map[string]any{"source_configured": inputs.Observations.Migration.SourceConfigured},
		},
	}
}

func integrityOutcome(outcome environment.Outcome) string {
	if outcome.Status == environment.OutcomePassed {
		return "verified"
	}
	return "refused"
}

func outcomeData(outcome environment.Outcome) map[string]any {
	evidence := make([]any, 0, len(outcome.Evidence))
	for _, item := range outcome.Evidence {
		evidence = append(evidence, map[string]any{"kind": item.Kind, "ref": item.Ref, "detail": item.Detail})
	}
	return map[string]any{"status": string(outcome.Status), "reason": map[string]any{"code": string(outcome.Reason.Code), "message": outcome.Reason.Message, "recovery": outcome.Reason.Recovery}, "evidence": evidence}
}

func resourceData(value environment.Resource) map[string]any {
	return map[string]any{"ref": value.Ref, "kind": value.Kind, "healthy": value.Healthy, "current": value.Current, "attributes": value.Attributes}
}
func stringsData(values []string) []any {
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	result := make([]any, len(copy))
	for index, value := range copy {
		result[index] = value
	}
	return result
}
