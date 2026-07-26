package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCatalogTypedMaterializePhases(t *testing.T) {
	path := writeTypedMaterializeCatalog(t, `
      application_id: evidence-recorder
      phases:
        - id: record
          handler: evidence.record
          artifact_outputs: [evidence_ref]
        - id: publish
          action: evidence.publish
          artifact_outputs: [packet_ref, manifest_ref]
`)
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	effective, ok := cat.Registry.Effective("deliverable")
	if !ok || effective.Materialize == nil {
		t.Fatal("typed materialize declaration was not registered")
	}
	got := effective.Materialize
	if got.ApplicationID != "evidence-recorder" || got.Story != "" || len(got.Phases) != 2 {
		t.Fatalf("materialize = %#v", got)
	}
	if got.Phases[1].Action != "evidence.publish" ||
		strings.Join(got.Phases[1].ArtifactOutputs, ",") != "packet_ref,manifest_ref" {
		t.Fatalf("publish phase = %#v", got.Phases[1])
	}
}

func TestLoadCatalogRejectsAmbiguousTypedMaterializePhases(t *testing.T) {
	cases := map[string]string{
		"legacy and typed": `
      story: stories/legacy
      application_id: evidence-recorder
      phases:
        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref]}
`,
		"missing application": `
      phases:
        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref]}
`,
		"handler and action": `
      application_id: evidence-recorder
      phases:
        - {id: record, handler: evidence.record, action: evidence.record, artifact_outputs: [evidence_ref]}
`,
		"duplicate phase": `
      application_id: evidence-recorder
      phases:
        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref]}
        - {id: record, handler: evidence.again, artifact_outputs: [other_ref]}
`,
		"missing outputs": `
      application_id: evidence-recorder
      phases:
        - {id: record, handler: evidence.record}
`,
		"duplicate output": `
      application_id: evidence-recorder
      phases:
        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref, evidence_ref]}
`,
		"path operation": `
      application_id: evidence-recorder
      phases:
        - {id: record, handler: ../scripts/record.sh, artifact_outputs: [evidence_ref]}
`,
		"legacy params": `
      application_id: evidence-recorder
      params:
        - {id: command, type: string}
      phases:
        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref]}
`,
		"legacy script check": `
      application_id: evidence-recorder
      phases:
        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref]}
      checks:
        - {id: verify, script: checks/verify.star}
`,
	}
	cases["too many phases"] = `
      application_id: evidence-recorder
      phases:
` + strings.Repeat("        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref]}\n", maxMaterializePhases+1)
	outputs := make([]string, maxMaterializeOutputsPerPhase+1)
	for i := range outputs {
		outputs[i] = "artifact_" + string(rune('a'+i))
	}
	cases["too many outputs"] = `
      application_id: evidence-recorder
      phases:
        - id: record
          handler: evidence.record
          artifact_outputs: [` + strings.Join(outputs, ", ") + `]
`
	for name, declaration := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadCatalog(writeTypedMaterializeCatalog(t, declaration))
			if err == nil {
				t.Fatal("LoadCatalog accepted an ambiguous typed materialize declaration")
			}
		})
	}
}

func writeTypedMaterializeCatalog(t *testing.T, declaration string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	source := `schema: project-object-graph/seed-catalog/v0
catalog: {id: typed-materialize-test}
type_registry:
  - id: core-node
    schema: graph-type/v0
    required_fields: [id, schema, title, status, visibility]
  - id: deliverable
    schema: graph-type/v0
    extends: core-node
    artifact: {schema: test/artifact/v0, format: json, presentation: document}
    materialize:
` + declaration + `
nodes:
  - schema: graph/deliverable/v0
    id: deliverable-one
    title: Deliverable
    status: planned
    visibility: internal
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
