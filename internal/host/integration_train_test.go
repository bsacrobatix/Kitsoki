package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestIntegrationTrainAuthorityRejectsNonCanonicalManifestSealBeforeCommand(t *testing.T) {
	request := integrationTrainRequestFromMap(t, integrationTrainTestRequest("validate", nil))
	request.Job["manifest"].(map[string]any)["manifest_digest"] = "sha256:" + strings.Repeat("f", 64)
	calls := 0
	authority := IntegrationTrainAuthority{
		Config: integrationTrainAuthorityConfig{
			StateRoot: t.TempDir(),
			Phases:    map[string]integrationTrainAuthorityPhase{"validate": {Command: []string{"/usr/bin/true"}}},
		},
		Run: func(context.Context, []string, []byte) ([]byte, error) {
			calls++
			return nil, nil
		},
	}
	if _, err := authority.Reconcile(context.Background(), request); err == nil || !strings.Contains(err.Error(), "canonical manifest") {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("unsealed manifest reached authority command: calls=%d", calls)
	}
}

func TestIntegrationTrainManifestDigestCanonicalVector(t *testing.T) {
	manifest := map[string]any{
		"stable_order": "candidate_id-asc",
		"candidates": []any{
			map[string]any{"candidate_id": "b", "note": "<one>"},
			map[string]any{"note": "&two", "candidate_id": "a"},
		},
		"schema":          "kitsoki/integration-train-manifest/v1",
		"max_items":       10,
		"manifest_digest": "sha256:" + strings.Repeat("0", 64),
	}
	got, err := integrationTrainManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:9a1dad7d044ae64cde29102d2f185d5bee3cc5d5ba772a20f3f0328b488158ee"
	if got != want {
		t.Fatalf("canonical digest=%s want=%s", got, want)
	}
	if manifest["manifest_digest"] == "" {
		t.Fatal("digest computation mutated caller manifest")
	}

	// Cross-runtime vector shared with POG's JavaScript assembler. This catches
	// drift in key ordering, compactness, omission of manifest_digest, or UTF-8
	// encoding before an effectful authority sees the job.
	pogVector := map[string]any{
		"schema":       "kitsoki/integration-train-manifest/v1",
		"stable_order": "candidate_id-asc",
		"max_items":    2,
		"candidates": []any{map[string]any{
			"candidate_id":  "candidate-a",
			"report_ref":    "feedback/a",
			"report_digest": "sha256:" + strings.Repeat("1", 64),
			"execution_id":  "worker-a",
			"shipped_sha":   strings.Repeat("2", 40),
			"base_sha":      strings.Repeat("0", 40),
			"bundle_digest": "sha256:" + strings.Repeat("3", 64),
		}},
	}
	crossRuntime, err := integrationTrainManifestDigest(pogVector)
	if err != nil {
		t.Fatal(err)
	}
	const pogWant = "sha256:aa43f04cee0e6326c9fd814667027981b9b57932f68c1f975b3bb1a363f2e46d"
	if crossRuntime != pogWant {
		t.Fatalf("POG cross-runtime digest=%s want=%s", crossRuntime, pogWant)
	}
}

func integrationTrainTestRequest(phase string, checkpoint map[string]any) map[string]any {
	manifest := map[string]any{
		"schema":       "kitsoki/integration-train-manifest/v1",
		"stable_order": "candidate_id-asc",
		"max_items":    1,
		"candidates": []any{
			map[string]any{
				"candidate_id":  "candidate-test",
				"report_ref":    "reports/test.md",
				"report_digest": "sha256:" + strings.Repeat("1", 64),
				"execution_id":  "worker-test",
				"shipped_sha":   strings.Repeat("2", 40),
				"base_sha":      strings.Repeat("3", 40),
				"bundle_digest": "sha256:" + strings.Repeat("4", 64),
			},
		},
	}
	digest, err := integrationTrainManifestDigest(manifest)
	if err != nil {
		panic(err)
	}
	manifest["manifest_digest"] = digest
	return map[string]any{
		"phase":      phase,
		"checkpoint": checkpoint,
		"job": map[string]any{
			"train_id": "train-test",
			"manifest": manifest,
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
