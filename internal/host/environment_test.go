package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"kitsoki/internal/environment"
)

type planInputsStub struct{ value environment.PlanInputs }

func (s planInputsStub) PlanInputs(context.Context) (environment.PlanInputs, error) {
	return s.value, nil
}

func TestEnvironmentPlanInputsHandlerLoadsPOGShapedBundleFromStoryRepo(t *testing.T) {
	repo := t.TempDir()
	profilePath := filepath.Join(repo, "ops", "deploy", "environment-profiles", "staging.json")
	profile := []byte(`{"tier":"staging","db_backend":"postgres","gh_app":"pog","dns_wildcard_guard_ip":"203.0.113.10","fqdn":"staging.example.test","live_ready":false,"provision_refused_reason":"not live"}`)
	require.NoError(t, os.MkdirAll(filepath.Dir(profilePath), 0o755))
	require.NoError(t, os.WriteFile(profilePath, profile, 0o644))
	digest := sha256.Sum256(profile)
	manifest := hex.EncodeToString(digest[:]) + "  ops/deploy/environment-profiles/staging.json\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, "ops", "deploy", "SHA256SUMS"), []byte(manifest), 0o644))

	handler := NewEnvironmentPlanInputsHandler(repo)
	result, err := handler(context.Background(), map[string]any{
		"op":                 "plan_inputs",
		"profile_root":       "ops/deploy/environment-profiles",
		"integrity_manifest": "ops/deploy/SHA256SUMS",
	})
	require.NoError(t, err)
	require.Empty(t, result.Error)
	documents := result.Data["profile_documents"].([]any)
	require.Len(t, documents, 1)
	document := documents[0].(map[string]any)["document"].(map[string]any)
	require.Equal(t, "staging", document["tier"])
	integrity := result.Data["integrity"].(map[string]any)
	require.Equal(t, true, integrity["verified"])
}

func TestEnvironmentPlanInputsHandlerRejectsPathsOutsideStoryRepo(t *testing.T) {
	handler := NewEnvironmentPlanInputsHandler(t.TempDir())
	for name, args := range map[string]map[string]any{
		"absolute profile root":   {"op": "plan_inputs", "profile_root": "/tmp/profiles", "integrity_manifest": "ops/deploy/SHA256SUMS"},
		"traversing profile root": {"op": "plan_inputs", "profile_root": "../profiles", "integrity_manifest": "ops/deploy/SHA256SUMS"},
		"absolute manifest":       {"op": "plan_inputs", "profile_root": "ops/deploy/environment-profiles", "integrity_manifest": "/tmp/SHA256SUMS"},
		"traversing manifest":     {"op": "plan_inputs", "profile_root": "ops/deploy/environment-profiles", "integrity_manifest": "../../SHA256SUMS"},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := handler(context.Background(), args)
			require.NoError(t, err)
			require.Equal(t, "environment plan-input paths must stay within the invoking Story repository", result.Error)
			integrity := result.Data["integrity"].(map[string]any)
			diagnostic := integrity["diagnostic"].(map[string]any)
			require.Equal(t, string(environment.ReasonInvalidRequest), diagnostic["reason"].(map[string]any)["code"])
		})
	}
}

func TestEnvironmentPlanInputsHandlerRejectsSymlinkEscapingStoryRepo(t *testing.T) {
	repo := t.TempDir()
	external := t.TempDir()
	require.NoError(t, os.Symlink(external, filepath.Join(repo, "profiles")))
	handler := NewEnvironmentPlanInputsHandler(repo)
	result, err := handler(context.Background(), map[string]any{
		"op":                 "plan_inputs",
		"profile_root":       "profiles",
		"integrity_manifest": "ops/deploy/SHA256SUMS",
	})
	require.NoError(t, err)
	require.Equal(t, "environment plan-input paths must stay within the invoking Story repository", result.Error)
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
