package compliance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/host"
	"kitsoki/internal/materialize"
)

type staticResolver struct {
	resolved ResolvedNode
	err      error
	calls    int
}

func (r *staticResolver) Resolve(_ context.Context, _, _, _ string) (ResolvedNode, error) {
	r.calls++
	return r.resolved, r.err
}

type staticRunner struct {
	results map[string]materialize.CheckResult
	calls   int
}

func (r *staticRunner) Run(_ context.Context, _ string, check materialize.ResolvedCheck) materialize.CheckResult {
	r.calls++
	return r.results[check.ID]
}

type memoryEvidence struct {
	records map[string][]byte
	puts    int
}

func (s *memoryEvidence) Get(_ context.Context, digest string) (string, bool, error) {
	_, ok := s.records[digest]
	return evidenceRef(digest), ok, nil
}

func (s *memoryEvidence) Put(_ context.Context, digest string, raw []byte) (string, error) {
	s.puts++
	s.records[digest] = append([]byte(nil), raw...)
	return evidenceRef(digest), nil
}

func testDependencies(t *testing.T) (Dependencies, *staticResolver, *staticRunner, *memoryEvidence) {
	t.Helper()
	root := t.TempDir()
	resolver := &staticResolver{resolved: ResolvedNode{
		CatalogDigest: "catalog-digest",
		NodeID:        "control-1",
		TypeID:        "control",
		Checks: []materialize.ResolvedCheck{
			{ID: "policy", Script: "checks/policy.star", Inputs: map[string]any{"want": true}},
			{ID: "evidence", Script: "checks/evidence.star", Inputs: map[string]any{}},
		},
	}}
	runner := &staticRunner{results: map[string]materialize.CheckResult{
		"policy":   {OK: true, Script: "/private/policy.star", Reproduce: "kitsoki starlark run private"},
		"evidence": {OK: false, Reasons: []string{"missing approval"}},
	}}
	store := &memoryEvidence{records: map[string][]byte{}}
	deps := Dependencies{
		AppID: "review-app", Root: root,
		Authorizer: BoundAuthorizer{AppID: "review-app", Root: root},
		Catalogs:   resolver, Runner: runner, Evidence: store,
		Clock: clock.NewFake(time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC)),
	}
	return deps, resolver, runner, store
}

func TestHandlerReturnsTypedResultAndSafeEvidence(t *testing.T) {
	deps, _, runner, store := testDependencies(t)
	result, err := NewHandler(deps)(context.Background(), map[string]any{
		"op": "run", "catalog_path": "catalog", "node_id": "control-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("domain error = %q", result.Error)
	}
	if result.Data["passed"] != false {
		t.Fatalf("passed = %v, want false", result.Data["passed"])
	}
	if got := result.Data["summary"]; got != "1/2 compliance checks passed; failed: evidence" {
		t.Fatalf("summary = %q", got)
	}
	ref, _ := result.Data["evidence_ref"].(string)
	if !strings.HasPrefix(ref, "kitsoki://compliance/sha256/") || strings.Contains(ref, deps.Root) {
		t.Fatalf("unsafe evidence ref %q", ref)
	}
	if runner.calls != 2 || store.puts != 1 {
		t.Fatalf("runner calls = %d, puts = %d", runner.calls, store.puts)
	}
	for _, raw := range store.records {
		if strings.Contains(string(raw), "/private/") || strings.Contains(string(raw), "kitsoki starlark run") {
			t.Fatalf("evidence leaked execution paths: %s", raw)
		}
	}
}

func TestHandlerHonorsInjectedLimits(t *testing.T) {
	deps, _, runner, _ := testDependencies(t)
	deps.Limits = Limits{
		MaxChecks: 1, MaxResolvedBytes: maxResolvedBytes,
		MaxEvidenceBytes: maxEvidenceBytes,
	}
	_, err := NewHandler(deps)(context.Background(), map[string]any{
		"op": "run", "catalog_path": "catalog", "node_id": "control-1",
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds 1") {
		t.Fatalf("limit error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0 before bound failure", runner.calls)
	}
}

func TestStoryApplicationStarlarkInvokesTypedRun(t *testing.T) {
	deps, _, _, _ := testDependencies(t)
	registry := host.NewRegistry()
	registry.Register("host.compliance", NewHandler(deps))

	dir := t.TempDir()
	script := filepath.Join(dir, "application-handler.star")
	if err := os.WriteFile(script, []byte(
		"def main(ctx):\n"+
			"    return ctx.host.call(\"host.compliance.run\", {\"catalog_path\": ctx.inputs[\"catalog_path\"], \"node_id\": ctx.inputs[\"node_id\"]})\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script+".yaml", []byte(
		"inputs:\n"+
			"  catalog_path: { type: string }\n"+
			"  node_id: { type: string }\n"+
			"outputs:\n"+
			"  passed: { type: bool }\n"+
			"  evidence_ref: { type: string }\n"+
			"  summary: { type: string }\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := host.NewStarlarkRunHandler(registry)(context.Background(), map[string]any{
		"script": script,
		"inputs": map[string]any{
			"catalog_path": "catalog", "node_id": "control-1",
		},
		"capabilities": map[string]any{
			"host": map[string]any{"verbs": []any{"host.compliance.run"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("Starlark result error = %q", result.Error)
	}
	if result.Data["passed"] != false ||
		result.Data["summary"] != "1/2 compliance checks passed; failed: evidence" {
		t.Fatalf("typed application result = %#v", result.Data)
	}
}

func TestHandlerDeniesUnauthorizedBeforeCatalogAccess(t *testing.T) {
	deps, resolver, _, store := testDependencies(t)
	deps.Authorizer = AuthorizerFunc(func(context.Context, Scope) error {
		return os.ErrPermission
	})
	_, err := NewHandler(deps)(context.Background(), map[string]any{
		"catalog_path": "catalog", "node_id": "control-1",
	})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("error = %v, want unauthorized", err)
	}
	if resolver.calls != 0 || store.puts != 0 {
		t.Fatalf("unauthorized call reached dependencies: resolve=%d put=%d", resolver.calls, store.puts)
	}
}

func TestHandlerRejectsStoryExecutionAuthority(t *testing.T) {
	for _, key := range []string{"command", "script", "path", "capabilities", "app_id"} {
		t.Run(key, func(t *testing.T) {
			deps, resolver, runner, store := testDependencies(t)
			_, err := NewHandler(deps)(context.Background(), map[string]any{
				"catalog_path": "catalog", "node_id": "control-1", key: "untrusted",
			})
			if err == nil || !strings.Contains(err.Error(), "unknown argument") {
				t.Fatalf("error = %v", err)
			}
			if resolver.calls != 0 || runner.calls != 0 || store.puts != 0 {
				t.Fatal("denied input reached provider dependencies")
			}
		})
	}
}

func TestHandlerRejectsNondeterministicResolvedCapabilities(t *testing.T) {
	tests := []map[string]any{
		{"http": true},
		{"host": map[string]any{"verbs": []any{"host.agent.ask"}}},
		{"fs": map[string]any{"write": []any{"evidence/**"}}},
	}
	for _, capabilities := range tests {
		deps, resolver, runner, store := testDependencies(t)
		resolver.resolved.Checks[0].Capabilities = capabilities
		_, err := NewHandler(deps)(context.Background(), map[string]any{
			"catalog_path": "catalog", "node_id": "control-1",
		})
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("capabilities %#v error = %v", capabilities, err)
		}
		if runner.calls != 0 || store.puts != 0 {
			t.Fatal("unsafe resolved capability was executed or persisted")
		}
	}
}

func TestHandlerBoundsFailWithoutEvidence(t *testing.T) {
	t.Run("checks", func(t *testing.T) {
		deps, resolver, runner, store := testDependencies(t)
		resolver.resolved.Checks = make([]materialize.ResolvedCheck, maxChecks+1)
		for i := range resolver.resolved.Checks {
			resolver.resolved.Checks[i] = materialize.ResolvedCheck{ID: strings.Repeat("x", i+1)}
		}
		_, err := NewHandler(deps)(context.Background(), map[string]any{
			"catalog_path": "catalog", "node_id": "control-1",
		})
		if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
			t.Fatalf("error = %v", err)
		}
		if runner.calls != 0 || store.puts != 0 {
			t.Fatal("overflow was executed or persisted")
		}
	})

	t.Run("evidence", func(t *testing.T) {
		deps, _, runner, store := testDependencies(t)
		runner.results["policy"] = materialize.CheckResult{
			OK: true, Output: map[string]any{"payload": strings.Repeat("x", maxEvidenceBytes)},
		}
		_, err := NewHandler(deps)(context.Background(), map[string]any{
			"catalog_path": "catalog", "node_id": "control-1",
		})
		if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
			t.Fatalf("error = %v", err)
		}
		if store.puts != 0 {
			t.Fatal("oversized evidence was persisted")
		}
	})
}

func TestGraphResolverRunsOnlyTypedMaterializeCheck(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "materialize", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	storeDir := t.TempDir()
	deps := Dependencies{
		AppID: "integration", Root: root,
		Authorizer: BoundAuthorizer{AppID: "integration", Root: root},
		Catalogs:   GraphCatalogResolver{}, Runner: MaterializeCheckRunner{},
		Evidence: FileEvidenceStore{Dir: storeDir},
		Clock:    clock.NewFake(time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC)),
	}
	result, err := NewHandler(deps)(context.Background(), map[string]any{
		"catalog_path": "catalog.yaml", "node_id": "wi-check-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["passed"] != true || result.Data["summary"] != "1/1 compliance checks passed" {
		t.Fatalf("result = %#v", result.Data)
	}
}

func TestFileEvidenceStoreIsIdempotentAcrossHandlerRestart(t *testing.T) {
	deps, _, _, _ := testDependencies(t)
	dir := t.TempDir()
	deps.Evidence = FileEvidenceStore{Dir: dir}
	first, err := NewHandler(deps)(context.Background(), map[string]any{
		"catalog_path": "catalog", "node_id": "control-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	later := clock.NewFake(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	deps.Clock = later
	deps.Evidence = FileEvidenceStore{Dir: dir}
	second, err := NewHandler(deps)(context.Background(), map[string]any{
		"catalog_path": "catalog", "node_id": "control-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Data["evidence_ref"] != second.Data["evidence_ref"] {
		t.Fatalf("restart refs differ: %v != %v", first.Data["evidence_ref"], second.Data["evidence_ref"])
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("evidence files = %d, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var record evidenceRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.RecordedAt != "2026-07-26T01:02:03Z" {
		t.Fatalf("recorded_at changed across restart: %q", record.RecordedAt)
	}
}

func TestGraphResolverRejectsPathsOutsideAppRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(outside, []byte("schema: invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (GraphCatalogResolver{}).Resolve(context.Background(), root, outside, "node")
	if err == nil || !strings.Contains(err.Error(), "escapes application root") {
		t.Fatalf("error = %v", err)
	}
}
