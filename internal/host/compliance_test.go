package host

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestComplianceRegistrationSchemaAndEffect(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	result, err := registry.Invoke(context.Background(), "host.compliance.run", map[string]any{
		"catalog_path": "catalog", "node_id": "control",
	})
	if err != nil {
		t.Fatalf("invoke builtin: %v", err)
	}
	if !strings.Contains(result.Error, "app-scoped compliance provider is unavailable") {
		t.Fatalf("builtin error = %q", result.Error)
	}

	class, deterministic := ClassifyDispatchedCall("host.compliance", map[string]any{"op": "run"})
	if class != effect.Write || !deterministic {
		t.Fatalf("classification = (%q, %v)", class, deterministic)
	}
	class, deterministic = ClassifyDispatchedCall("host.compliance.run", nil)
	if class != effect.Write || !deterministic {
		t.Fatalf("leaf classification = (%q, %v)", class, deterministic)
	}
	spec, ok := opschema.Builtins().Lookup("host.compliance", "run")
	if !ok ||
		spec.Input["catalog_path"].Type != "string" ||
		spec.Input["node_id"].Type != "string" ||
		spec.Output["passed"].Type != "bool" ||
		spec.Output["evidence_ref"].Type != "string" ||
		spec.Output["summary"].Type != "string" {
		t.Fatalf("opschema = %#v, %v", spec, ok)
	}
}
