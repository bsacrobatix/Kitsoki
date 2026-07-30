package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"kitsoki/internal/environment"
)

type planInputsStub struct{ value environment.PlanInputs }

func (s planInputsStub) PlanInputs(context.Context) (environment.PlanInputs, error) {
	return s.value, nil
}

func TestEnvironmentPlanInputsHandlerPreservesPlannerEnvelope(t *testing.T) {
	handler := NewEnvironmentHandler(planInputsStub{value: environment.PlanInputs{
		ProfileDocuments: []environment.ProfileDocument{{Source: environment.ProfileSource{Path: "environment-profiles/alpha.json", Digest: "abc"}, Document: map[string]any{"tier": "test"}}},
		Integrity:        environment.Passed(environment.Evidence{Kind: "profile_bundle", Ref: "environment-profiles"}),
		Observations:     environment.Observations{DNS: environment.DNSObservation{ResolvedIPs: []string{"2.2.2.2", "1.1.1.1"}}, Deployment: environment.DeploymentObservation{Healthy: true, Current: false}},
	}}, nil)
	result, err := handler(context.Background(), map[string]any{"op": "plan_inputs"})
	require.NoError(t, err)
	require.Empty(t, result.Error)
	documents := result.Data["profile_documents"].([]any)
	require.Len(t, documents, 1)
	observations := result.Data["observations"].(map[string]any)
	require.Equal(t, false, observations["deployment"].(map[string]any)["current"])
	require.Equal(t, []any{"1.1.1.1", "2.2.2.2"}, observations["dns"].(map[string]any)["resolved_ips"])
}

func TestEnvironmentBuiltinFailsClosed(t *testing.T) {
	result, err := EnvironmentHandler(context.Background(), map[string]any{"op": "plan_inputs"})
	require.NoError(t, err)
	require.Equal(t, "environment host is unavailable outside a configured deployment session", result.Error)
	outcome := result.Data["outcome"].(map[string]any)
	require.Equal(t, string(environment.ReasonUnavailable), outcome["reason"].(map[string]any)["code"])
}
