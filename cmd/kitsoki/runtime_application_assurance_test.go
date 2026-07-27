package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/testrunner"
	"kitsoki/internal/webconfig"
)

func TestRuntimeWiresConfiguredPathFreeFlowEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".kitsoki-root"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storyDir := filepath.Join(root, "stories", "assurance")
	flowsDir := filepath.Join(storyDir, "flows")
	if err := os.MkdirAll(flowsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(appPath, []byte(flowEvidenceRunnerAppYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(flowsDir, "happy.yaml"),
		[]byte(flowEvidenceRunnerFlowYAML),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pog"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".kitsoki"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "pog", "catalog.yaml"),
		[]byte(applicationAssuranceCatalog),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	def, err := app.Load(appPath)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := buildSessionRuntime(runtimeConfig{
		AppPath:  appPath,
		Def:      def,
		DBPath:   filepath.Join(root, ".kitsoki", "sessions.db"),
		ExecMode: orchestrator.ExecOneShot,
		Flow:     &testrunner.FlowFixture{},
		ApplicationAssurance: &webconfig.StoryApplicationAssuranceConfig{
			Catalog: "pog/catalog.yaml",
			FlowEvidence: &webconfig.ApplicationFlowEvidence{
				Suites: []webconfig.ApplicationFlowSuite{{
					ID: "assurance", App: "stories/assurance/app.yaml",
					Flows: "stories/assurance/flows/*.yaml", Version: "v1",
				}},
				MaxSuites: 2, MaxRuns: 4,
				MaxSuiteBytes: 64 * 1024, MaxEvidenceBytes: 64 * 1024,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	result, err := rt.HostRegistry.Invoke(
		host.WithActor(context.Background(), "operator"),
		"host.flow_evidence.record",
		map[string]any{"node_id": "node-one"},
	)
	if err != nil || result.Error != "" ||
		result.Data["passed"] != true || result.Data["run_count"] != 1 {
		t.Fatalf("path-free result = %#v, err = %v", result, err)
	}
	replayed, err := rt.HostRegistry.Invoke(
		host.WithActor(context.Background(), "operator"),
		"host.flow_evidence.record",
		map[string]any{"node_id": "node-one"},
	)
	if err != nil || replayed.Data["evidence_ref"] != result.Data["evidence_ref"] {
		t.Fatalf("replay = %#v, err = %v", replayed, err)
	}
	_, err = rt.HostRegistry.Invoke(
		host.WithActor(context.Background(), "operator"),
		"host.flow_evidence.record",
		map[string]any{"node_id": "node-one", "catalog_path": "/tmp/private"},
	)
	if err == nil || !strings.Contains(err.Error(), "authority key") {
		t.Fatalf("catalog authority error = %v", err)
	}
}

func TestRegistrySelectsAssuranceByExactApplicationID(t *testing.T) {
	cfg := webconfig.WebConfig{
		StoryApplicationAssurance: map[string]webconfig.StoryApplicationAssuranceConfig{
			"bound-app": {Catalog: "pog/catalog.yaml"},
		},
	}
	registry := NewRegistry(cfg, nil, runtimeBase{})
	bound := registry.base.config(
		"/repo/stories/bound/app.yaml",
		&app.AppDef{App: app.AppMeta{ID: "bound-app"}},
	)
	if bound.ApplicationAssurance == nil {
		t.Fatal("exact application did not receive assurance binding")
	}
	other := registry.base.config(
		"/repo/stories/other/app.yaml",
		&app.AppDef{App: app.AppMeta{ID: "other-app"}},
	)
	if other.ApplicationAssurance != nil {
		t.Fatalf("other application received binding %#v", other.ApplicationAssurance)
	}
}

const applicationAssuranceCatalog = `
schema: project-object-graph/seed-catalog/v0
type_registry:
  - id: core-node
    schema: graph-type/v0
nodes:
  - schema: graph/core-node/v0
    id: node-one
    title: Node one
    status: active
    visibility: public
`
