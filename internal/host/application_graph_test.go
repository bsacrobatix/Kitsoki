package host

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationGraphHandlerInjectsAuthorityAndBoundsSnapshot(t *testing.T) {
	catalog, err := filepath.Abs(graphSnapshotFixture)
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewApplicationGraphHandlers(ApplicationGraphBinding{
		ApplicationID: "pog", ProjectRoot: filepath.Dir(catalog),
		CatalogPath: catalog, MaxNodes: 2, MaxBytes: 1 << 20,
		WritePolicy: ApplicationGraphWriteRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := handlers.Operations["snapshot"]
	args := map[string]any{
		"audience": "public", "fields": []any{"title", "status", "visibility"},
	}
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	nodes := result.Data["snapshot"].(map[string]any)["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want configured bound 2", len(nodes))
	}
	if _, leaked := args["catalog_path"]; leaked {
		t.Fatal("handler mutated caller args with server catalog path")
	}
}

func TestApplicationGraphHandlerRejectsCallerAuthorityRecursively(t *testing.T) {
	catalog, err := filepath.Abs(graphSnapshotFixture)
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewApplicationGraphHandlers(ApplicationGraphBinding{
		ApplicationID: "pog", ProjectRoot: filepath.Dir(catalog),
		CatalogPath: catalog, MaxNodes: 3, MaxBytes: 1 << 20,
		WritePolicy: ApplicationGraphWriteSteward,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := handlers.Operations["propose"]
	for _, key := range []string{
		"catalog_path", "overlay_path", "url", "command", "provider", "profile",
		"actor", "session_id", "transport",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := handler(context.Background(), map[string]any{
				"title": "unsafe", "operations": []any{map[string]any{
					"kind": "set_field", "value": map[string]any{key: "caller-selected"},
				}},
			})
			if err == nil || !strings.Contains(err.Error(), "forbidden caller authority") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestApplicationGraphExactLeafRejectsOperationRetargeting(t *testing.T) {
	catalog, err := filepath.Abs(graphSnapshotFixture)
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewApplicationGraphHandlers(ApplicationGraphBinding{
		ApplicationID: "pog", ProjectRoot: filepath.Dir(catalog),
		CatalogPath: catalog, MaxNodes: 3, MaxBytes: 1 << 20,
		WritePolicy: ApplicationGraphWriteSteward,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handlers.Operations["snapshot"](context.Background(), map[string]any{
		"op": "authorize", "changeset_id": "cs-caller-selected",
	})
	if err == nil || !strings.Contains(err.Error(), "forbidden caller authority") {
		t.Fatalf("error = %v", err)
	}
	if _, err := handlers.Prefix(context.Background(), map[string]any{"op": "snapshot"}); err == nil {
		t.Fatal("configured prefix accepted caller-selected operation")
	}
}

func TestApplicationGraphWritePolicies(t *testing.T) {
	tests := []struct {
		policy string
		op     string
		dryRun bool
		want   bool
	}{
		{ApplicationGraphWriteRead, "snapshot", false, true},
		{ApplicationGraphWriteRead, "propose", false, false},
		{ApplicationGraphWritePropose, "propose", false, true},
		{ApplicationGraphWritePropose, "authorize", false, false},
		{ApplicationGraphWritePropose, "apply", true, true},
		{ApplicationGraphWritePropose, "apply", false, false},
		{ApplicationGraphWriteSteward, "authorize", false, true},
		{ApplicationGraphWriteSteward, "apply", false, true},
	}
	for _, test := range tests {
		if got := applicationGraphOperationAllowed(test.policy, test.op, test.dryRun); got != test.want {
			t.Errorf("policy=%s op=%s dry_run=%v = %v, want %v", test.policy, test.op, test.dryRun, got, test.want)
		}
	}
}

func TestApplicationGraphPublicShapesContainNoAuthoritySelectors(t *testing.T) {
	for op, fields := range applicationGraphPublicInputs {
		for field := range fields {
			if applicationGraphAuthorityKey(field) || field == "max_nodes" || field == "max_bytes" {
				t.Errorf("%s exposes authority or configured bound %q", op, field)
			}
		}
	}
}
