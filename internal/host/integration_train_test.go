package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestIntegrationTrainHandlerWithoutAuthorityReturnsObservableNeedsInput(t *testing.T) {
	t.Setenv(IntegrationTrainAuthorityConfigEnv, "")
	request := integrationTrainTestRequest("validate", nil)
	result, err := IntegrationTrainHandler(context.Background(), map[string]any{"request": request})
	if err != nil || result.Error != "" {
		t.Fatalf("handler result %#v, %v", result, err)
	}
	evidence := result.Data["evidence"].(map[string]any)
	if evidence["status"] != "needs_input" || evidence["phase"] != "validate" {
		t.Fatalf("evidence %#v", evidence)
	}
	checkpoint := evidence["checkpoint"].(map[string]any)
	if checkpoint["checkpoint_digest"] == "" || checkpoint["previous_digest"] != "" {
		t.Fatalf("checkpoint %#v", checkpoint)
	}
}

func TestIntegrationTrainAuthorityCachesExactRequestEvidence(t *testing.T) {
	request := integrationTrainTestRequest("validate", nil)
	calls := 0
	authority := IntegrationTrainAuthority{
		Config: integrationTrainAuthorityConfig{
			Schema:    integrationTrainAuthoritySchema,
			StateRoot: t.TempDir(),
			Phases: map[string]integrationTrainAuthorityPhase{
				"validate": {Command: []string{"/usr/bin/true"}},
			},
		},
		Run: func(_ context.Context, _ []string, _ []byte) ([]byte, error) {
			calls++
			evidence := integrationTrainNeedsInput(integrationTrainRequestFromMap(t, request), "waiting for steward")
			return json.Marshal(evidence)
		},
	}
	first, err := authority.Reconcile(context.Background(), integrationTrainRequestFromMap(t, request))
	if err != nil {
		t.Fatal(err)
	}
	second, err := authority.Reconcile(context.Background(), integrationTrainRequestFromMap(t, request))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("authority command calls = %d, want 1", calls)
	}
	if first["checkpoint"].(map[string]any)["checkpoint_digest"] != second["checkpoint"].(map[string]any)["checkpoint_digest"] {
		t.Fatalf("cached evidence changed: %#v %#v", first, second)
	}
	files, err := filepath.Glob(filepath.Join(authority.Config.StateRoot, "integration-train", "*", "validate", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("durable authority records = %v, %v", files, err)
	}
}

func TestIntegrationTrainAuthorityRejectsMismatchedDriverEvidence(t *testing.T) {
	request := integrationTrainTestRequest("validate", nil)
	authority := IntegrationTrainAuthority{
		Config: integrationTrainAuthorityConfig{StateRoot: t.TempDir(), Phases: map[string]integrationTrainAuthorityPhase{"validate": {Command: []string{"/usr/bin/true"}}}},
		Run: func(context.Context, []string, []byte) ([]byte, error) {
			return []byte(`{"schema":"kitsoki/integration-train-authority/v1","phase":"validate","train_id":"other","manifest_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"ready","checkpoint":{}}`), nil
		},
	}
	if _, err := authority.Reconcile(context.Background(), integrationTrainRequestFromMap(t, request)); err == nil {
		t.Fatal("mismatched evidence was accepted")
	}
}

func integrationTrainTestRequest(phase string, checkpoint map[string]any) map[string]any {
	return map[string]any{
		"phase":      phase,
		"checkpoint": checkpoint,
		"job": map[string]any{
			"train_id": "train-test",
			"manifest": map[string]any{"manifest_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}
}

func integrationTrainRequestFromMap(t *testing.T, raw map[string]any) integrationTrainRequest {
	t.Helper()
	request, err := integrationTrainRequestFromArgs(map[string]any{"request": raw})
	if err != nil {
		t.Fatal(err)
	}
	return request
}
