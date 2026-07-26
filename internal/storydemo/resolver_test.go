package storydemo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGraphResolverUsesServerDeclarations(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"dependency.demo.json", "subject.demo.json", "evidence.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(`{"ok":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog := `schema: project-object-graph/seed-catalog/v0
catalog:
  id: typed-demo-test
type_registry:
  - id: core-node
    schema: graph-type/v0
    extends: null
    required_fields: [id, schema, title, status, visibility]
  - id: demo
    schema: graph-type/v0
    extends: core-node
    edge_fields:
      - id: materializes_after
        target_type: demo
        cardinality: many
        acyclic: true
    artifact:
      schema: kitsoki/artifact/demo/v1
      format: json
      presentation: demo
    materialize:
      story: stories/demo-packet
      params:
        - id: manifest_path
          type: string
          required: true
          source_field: manifest_path
        - id: dependency_ids
          type: list
          source_edge: materializes_after
nodes:
  - schema: graph/demo/v0
    id: dependency
    title: Dependency
    status: active
    visibility: internal
    manifest_path: dependency.demo.json
    materialize_command: "printf dependency"
    materialize_verify_command: "printf verify"
  - schema: graph/demo/v0
    id: subject
    title: Subject
    status: active
    visibility: internal
    summary: Typed provider fixture
    manifest_path: subject.demo.json
    materialize_command: "printf subject"
    materialize_verify_command: "printf verify"
    materialize_dependencies:
      - id: dependency
        producer: "printf dependency"
    evidence:
      - kind: report
        path: evidence.txt
    edges:
      materializes_after: [dependency]
`
	if err := os.WriteFile(filepath.Join(root, "catalog.yaml"), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := GraphResolver{}
	plan, err := resolver.Plan(context.Background(), root, "catalog.yaml", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.ClosureOrder, []string{"dependency", "subject"}) {
		t.Fatalf("closure = %#v", plan.ClosureOrder)
	}
	wantManifest, err := filepath.EvalSymlinks(filepath.Join(root, "subject.demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Manifest.Path != wantManifest {
		t.Fatalf("manifest = %q", plan.Manifest.Path)
	}
	if len(plan.Artifacts) != 1 || plan.Artifacts[0].Kind != "report" {
		t.Fatalf("artifacts = %#v", plan.Artifacts)
	}
	materialization, err := resolver.Materialization(context.Background(), root, "catalog.yaml", "subject", "dependencies")
	if err != nil {
		t.Fatal(err)
	}
	if len(materialization.Tasks) != 1 || materialization.Tasks[0].ID != "dependency" ||
		len(materialization.Tasks[0].Artifacts) != 1 ||
		materialization.Tasks[0].Artifacts[0].Kind != "manifest" {
		t.Fatalf("tasks = %#v", materialization.Tasks)
	}
	materializationJSON, err := json.Marshal(materialization)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(materializationJSON), "printf") {
		t.Fatalf("catalog executable fields leaked into materialization: %s", materializationJSON)
	}
	_, err = resolver.ProjectMockup(context.Background(), root, "catalog.yaml", "subject", "public")
	if err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("public projection error = %v", err)
	}
	projection, err := resolver.ProjectMockup(context.Background(), root, "catalog.yaml", "subject", "internal")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projection.Scenario), "Typed provider fixture") {
		t.Fatalf("scenario = %s", projection.Scenario)
	}
}
