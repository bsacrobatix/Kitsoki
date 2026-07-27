package applicationassurance

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

func TestSemanticHandlerInjectsAuthority(t *testing.T) {
	var got map[string]any
	delegate := func(_ context.Context, args map[string]any) (host.Result, error) {
		got = args
		return host.Result{Data: map[string]any{"passed": true}}, nil
	}
	handler := NewSemanticHandler(
		"host.compliance", "run", "/server/catalog.yaml", delegate,
	)
	result, err := handler(context.Background(), map[string]any{"node_id": "control-one"})
	if err != nil || result.Data["passed"] != true {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if len(got) != 3 || got["op"] != "run" ||
		got["catalog_path"] != "/server/catalog.yaml" ||
		got["node_id"] != "control-one" {
		t.Fatalf("delegate args = %#v", got)
	}
}

func TestSemanticHandlerRejectsExtraAndNestedAuthority(t *testing.T) {
	handler := NewSemanticHandler(
		"host.flow_evidence", "record", "/server/catalog.yaml",
		func(context.Context, map[string]any) (host.Result, error) {
			t.Fatal("delegate called")
			return host.Result{}, nil
		},
	)
	cases := []map[string]any{
		{"node_id": "control-one", "catalog_path": "/tmp/other"},
		{"node_id": "control-one", "extra": map[string]any{"provider": "live"}},
		{"node_id": "control-one", "max_runs": 200},
		{"node_id": "control-one", "actor": "other"},
		{"node_id": "control-one", "transport": "mcp"},
	}
	for _, args := range cases {
		_, err := handler(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "authority key") {
			t.Fatalf("args %#v error = %v", args, err)
		}
	}
	if _, err := handler(
		context.Background(),
		map[string]any{"node_id": "control-one", "extra": "value"},
	); err == nil || !strings.Contains(err.Error(), "unknown argument") {
		t.Fatalf("unknown extra error = %v", err)
	}
}
