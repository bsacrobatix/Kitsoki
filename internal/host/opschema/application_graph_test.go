package opschema

import "testing"

func TestApplicationGraphOpschemasCoverAuthorityFreeStoryShapes(t *testing.T) {
	public := map[string][]string{
		"snapshot":  {"audience", "fields"},
		"get":       {"ids", "fields"},
		"changeset": {"action", "changeset_id", "node_id"},
		"project":   {"graph_id"},
		"propose":   {"title", "operations", "visibility", "validate_only"},
		"authorize": {"changeset_id"},
		"withdraw":  {"changeset_id"},
		"rebase":    {"changeset_id"},
		"apply":     {"changeset_id", "dry_run"},
	}
	registry := Builtins()
	for op, fields := range public {
		spec, ok := registry.Lookup("host.graph", op)
		if !ok {
			t.Errorf("host.graph.%s has no registered schema", op)
			continue
		}
		for _, field := range fields {
			if _, ok := spec.Input[field]; !ok {
				t.Errorf("host.graph.%s public input %q has no schema field", op, field)
			}
		}
		if len(spec.Output) == 0 {
			t.Errorf("host.graph.%s has no registered output schema", op)
		}
	}
}
