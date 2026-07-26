package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

type registryFlowEvidenceResolver struct {
	request host.FlowEvidenceResolveRequest
	target  host.FlowEvidenceTarget
}

func (r *registryFlowEvidenceResolver) ResolveFlowEvidence(
	_ context.Context,
	request host.FlowEvidenceResolveRequest,
) (host.FlowEvidenceTarget, error) {
	r.request = request
	target := r.target
	target.Scope = request.Scope
	target.NodeID = request.NodeID
	return target, nil
}

type registryFlowEvidenceRunner struct{}

func (registryFlowEvidenceRunner) RunFlowEvidence(
	_ context.Context,
	suite host.FlowEvidenceSuite,
	_ host.FlowEvidenceLimits,
) (host.FlowEvidenceSuiteResult, error) {
	return host.FlowEvidenceSuiteResult{
		SuiteID:  suite.ID,
		Passed:   true,
		RunCount: 1,
		Runs: []host.FlowEvidenceRunResult{{
			Ref: "flow:one", Passed: true,
		}},
	}, nil
}

type registryFlowEvidenceStore struct {
	mu      sync.Mutex
	records map[string]host.FlowEvidenceRecord
}

func (s *registryFlowEvidenceStore) LookupFlowEvidence(
	_ context.Context,
	key string,
) (host.FlowEvidenceRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	return record, ok, nil
}

func (s *registryFlowEvidenceStore) PutFlowEvidenceIfAbsent(
	_ context.Context,
	key string,
	record host.FlowEvidenceRecord,
) (host.FlowEvidenceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]host.FlowEvidenceRecord)
	}
	if existing, ok := s.records[key]; ok {
		return existing, nil
	}
	s.records[key] = record
	return record, nil
}

func TestRegisterAndWireFlowEvidenceUsesLoadedApplicationScope(t *testing.T) {
	resolver := &registryFlowEvidenceResolver{target: host.FlowEvidenceTarget{
		CatalogRevision: "catalog-rev",
		Suites: []host.FlowEvidenceSuite{{
			ID: "suite", Revision: "suite-rev",
			AppPath: "/server/app.yaml", FlowGlob: "/server/flows/*.yaml",
		}},
	}}
	provider := host.FlowEvidenceProvider{
		CatalogPath: "/server/catalog.yaml",
		Resolver:    resolver,
		Runner:      registryFlowEvidenceRunner{},
		Store:       &registryFlowEvidenceStore{},
		Clock:       clock.NewFake(time.Unix(1, 0).UTC()),
	}
	registry := NewRegistry(webconfig.WebConfig{}, nil, runtimeBase{})
	if err := registry.RegisterFlowEvidenceProvider("app-a", provider); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	if err := registry.RegisterFlowEvidenceProvider("app-a", provider); err == nil ||
		!strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate registration error = %v", err)
	}

	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireFlowEvidence(
		&sessionRuntime{HostRegistry: hostRegistry},
		"app-a",
		"server-owner",
		"2.1.0",
	)
	result, err := hostRegistry.Invoke(
		host.WithActor(context.Background(), "operator"),
		"host.flow_evidence.record",
		map[string]any{"catalog_path": provider.CatalogPath, "node_id": "node-one"},
	)
	if err != nil {
		t.Fatalf("invoke record: %v", err)
	}
	if result.Error != "" || result.Data["passed"] != true || result.Data["run_count"] != 1 {
		t.Fatalf("result = %#v", result)
	}
	wantScope := host.FlowEvidenceScope{
		ApplicationID: "app-a",
		Owner:         "server-owner",
		Revision:      "2.1.0",
		CatalogPath:   provider.CatalogPath,
	}
	if resolver.request.Scope != wantScope || resolver.request.Actor != "operator" {
		t.Fatalf("resolver request = %#v", resolver.request)
	}
}

func TestWireFlowEvidenceLeavesUnregisteredAppUnavailable(t *testing.T) {
	registry := &SessionRegistry{}
	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireFlowEvidence(
		&sessionRuntime{HostRegistry: hostRegistry},
		"unregistered",
		"owner",
		"1",
	)
	result, err := hostRegistry.Invoke(
		host.WithActor(context.Background(), "actor"),
		"host.flow_evidence.record",
		map[string]any{"catalog_path": "catalog", "node_id": "node"},
	)
	if err != nil {
		t.Fatalf("invoke sentinel: %v", err)
	}
	if !strings.Contains(result.Error, "unavailable") {
		t.Fatalf("sentinel error = %q", result.Error)
	}
	if err := registry.RegisterFlowEvidenceProvider("app", host.FlowEvidenceProvider{}); err == nil {
		t.Fatal("incomplete provider registration succeeded")
	}
}

func TestTestrunnerFlowEvidenceRunnerUsesInProcessDeterministicAPI(t *testing.T) {
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.yaml")
	flowPath := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(appPath, []byte(flowEvidenceRunnerAppYAML), 0o644); err != nil {
		t.Fatalf("write app: %v", err)
	}
	if err := os.WriteFile(flowPath, []byte(flowEvidenceRunnerFlowYAML), 0o644); err != nil {
		t.Fatalf("write flow: %v", err)
	}
	runner := newTestrunnerFlowEvidenceRunner(nil)
	result, err := runner.RunFlowEvidence(context.Background(), host.FlowEvidenceSuite{
		ID: "suite", Revision: "1", AppPath: appPath, FlowGlob: flowPath,
	}, host.FlowEvidenceLimits{MaxRuns: 1, MaxEvidenceBytes: 64 * 1024})
	if err != nil {
		t.Fatalf("run suite: %v", err)
	}
	if !result.Passed || result.RunCount != 1 || len(result.Runs) != 1 ||
		!strings.HasPrefix(result.Runs[0].Ref, "flow:") {
		t.Fatalf("runner result = %#v", result)
	}

	_, err = runner.RunFlowEvidence(context.Background(), host.FlowEvidenceSuite{
		ID: "suite", Revision: "1", AppPath: appPath, FlowGlob: flowPath,
	}, host.FlowEvidenceLimits{MaxRuns: 0, MaxEvidenceBytes: 64 * 1024})
	if err == nil || !strings.Contains(err.Error(), "positive run") {
		t.Fatalf("zero run limit error = %v", err)
	}
}

const flowEvidenceRunnerAppYAML = `
app:
  id: flow-evidence-test
  version: 0.1.0
  title: Flow evidence test
  author: test
  license: CC0
hosts: []
world: {}
root: idle
intents:
  finish:
    description: Finish
    examples: [finish]
states:
  idle:
    on:
      finish:
        - target: done
  done:
    terminal: true
`

const flowEvidenceRunnerFlowYAML = `
test_kind: flow
app: flow-evidence-test
initial_state: idle
turns:
  - intent: { name: finish }
    expect_state: done
expect_terminal: true
expect_no_errors: true
`
