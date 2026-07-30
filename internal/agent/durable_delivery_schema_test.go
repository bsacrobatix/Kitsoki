package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDurableDeliveryPassedResultRequiresValidGitBundle(t *testing.T) {
	schema, err := os.ReadFile(filepath.Join("..", "..", "stories", "durable-delivery", "schemas", "task_result.json"))
	if err != nil {
		t.Fatal(err)
	}
	valid := json.RawMessage(`{
		"passed": true,
		"summary": "fixed",
		"trace_ref": "trace-1",
		"bundle_ref": "bundles/fix.bundle",
		"bundle_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bundle_kind": "git-bundle"
	}`)
	if err := ValidateSubmission(schema, valid); err != nil {
		t.Fatalf("valid fixer result: %v", err)
	}
	for name, submission := range map[string]json.RawMessage{
		"missing_bundle": json.RawMessage(`{"passed":true,"summary":"fixed","trace_ref":"trace-1"}`),
		"wrong_digest":   json.RawMessage(`{"passed":true,"summary":"fixed","trace_ref":"trace-1","bundle_ref":"b","bundle_digest":"sha256:nope","bundle_kind":"git-bundle"}`),
		"wrong_kind":     json.RawMessage(`{"passed":true,"summary":"fixed","trace_ref":"trace-1","bundle_ref":"b","bundle_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","bundle_kind":"archive"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSubmission(schema, submission); err == nil {
				t.Fatal("schema accepted a passed result that cannot be admitted")
			}
		})
	}
}
