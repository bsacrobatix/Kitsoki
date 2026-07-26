package storydemo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/host"
	"kitsoki/internal/host/opschema"
	starlarkhost "kitsoki/internal/host/starlark"
)

type fakeResolver struct {
	plan       Plan
	material   Materialization
	projection MockupProjection
	calls      int
}

func (f *fakeResolver) Plan(context.Context, string, string, string) (Plan, error) {
	f.calls++
	return f.plan, nil
}
func (f *fakeResolver) Materialization(context.Context, string, string, string, string) (Materialization, error) {
	f.calls++
	return f.material, nil
}
func (f *fakeResolver) ProjectMockup(context.Context, string, string, string, string) (MockupProjection, error) {
	f.calls++
	return f.projection, nil
}

type fakeArtifacts struct {
	mu      sync.Mutex
	calls   int
	results map[string]ApplicationArtifactResult
}

func (f *fakeArtifacts) ExecuteApplicationArtifact(
	_ context.Context,
	request ApplicationArtifactRequest,
) (ApplicationArtifactResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(request)
	key := string(raw)
	if result, ok := f.results[key]; ok {
		return result, nil
	}
	f.calls++
	result := ApplicationArtifactResult{
		JobID: "job-opaque", SessionID: "session-opaque",
		ReceiptIDs: []string{"ar_0123456789abcdef0123456789abcdef"},
	}
	switch request.Operation {
	case "create_mockup":
		result.Primary = "demo-artifact:mockup"
		result.Bundle = "application-bundle:0123456789abcdef"
		result.Artifacts = []string{result.Primary}
	default:
		result.Primary = "demo-artifact:materialized"
		result.Artifacts = []string{result.Primary}
	}
	f.results[key] = result
	return result, nil
}

type fakeTools struct {
	mu          sync.Mutex
	root        string
	recordCalls int
	doctorCalls int
	lastRecord  Manifest
}

func (f *fakeTools) Record(_ context.Context, _ string, manifest Manifest) (ToolResult, error) {
	f.mu.Lock()
	f.recordCalls++
	f.lastRecord = manifest
	f.mu.Unlock()
	path := filepath.Join(f.root, "capture.json")
	if err := os.WriteFile(path, []byte(`{"events":[]}`), 0o600); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Artifacts: []Artifact{{Kind: "rrweb", Path: path}}}, nil
}

func (f *fakeTools) Check(context.Context, string, Manifest) (DoctorResult, error) {
	f.mu.Lock()
	f.doctorCalls++
	f.mu.Unlock()
	return DoctorResult{Report: map[string]any{"ok": true, "checks": []any{}}, OK: true}, nil
}

func testDependencies(t *testing.T) (Dependencies, *fakeResolver, *fakeArtifacts, *fakeTools) {
	t.Helper()
	root := t.TempDir()
	manifest := filepath.Join(root, "demo.json")
	artifact := filepath.Join(root, "evidence.txt")
	if err := os.WriteFile(manifest, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	scenario := json.RawMessage(`{"version":1,"title":"typed"}`)
	resolver := &fakeResolver{
		plan: Plan{
			CatalogDigest: "catalog", NodeID: "subject",
			ClosureOrder: []string{"dependency", "subject"},
			Manifest:     Manifest{Path: manifest},
			Artifacts:    []Artifact{{Kind: "text", Path: artifact}},
		},
		material: Materialization{
			CatalogDigest: "catalog", NodeID: "subject", Phase: "subject",
			Tasks:     []Task{{ID: "subject", Phase: "subject", Artifacts: []Artifact{{Kind: "text", Path: artifact}}}},
			Artifacts: []Artifact{{Kind: "text", Path: artifact}},
		},
		projection: MockupProjection{
			CatalogDigest: "catalog", NodeID: "subject", Audience: "internal", Scenario: scenario,
			Manifest: MockupManifest{Scenario: scenario},
		},
	}
	artifacts := &fakeArtifacts{results: map[string]ApplicationArtifactResult{}}
	tools := &fakeTools{root: root}
	deps := Dependencies{
		AppID: "typed-app", Root: root, CatalogPath: "catalog.yaml", CatalogRef: "product",
		MockupApplicationID: "artifact-producer",
		Authorizer:          BoundAuthorizer{AppID: "typed-app", Root: root},
		Resolver:            resolver,
		Artifacts:           artifacts,
		Capture:             tools,
		Doctor:              tools,
		Evidence: FileEvidenceStore{
			Dir: filepath.Join(root, ".artifacts", "refs"), Scope: ScopeID("typed-app", root),
		},
		Clock: clock.NewFake(time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)),
	}
	return deps, resolver, artifacts, tools
}

func actorContext() context.Context {
	return host.WithActor(context.Background(), "operator@example.test")
}

func TestTypedHandlerRejectsActorlessAndExecutableInputs(t *testing.T) {
	deps, resolver, _, _ := testDependencies(t)
	handler := NewHandler(deps)
	_, err := handler(context.Background(), map[string]any{
		"op": "plan", "node_id": "subject",
	})
	if err == nil || !strings.Contains(err.Error(), "authenticated actor") {
		t.Fatalf("actorless error = %v", err)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver called before authorization: %d", resolver.calls)
	}

	for _, key := range []string{"cmd", "command", "script", "path", "repo_path", "url", "args", "credentials"} {
		t.Run(key, func(t *testing.T) {
			_, err := handler(actorContext(), map[string]any{
				"op": "plan", "node_id": "subject", key: "unsafe",
			})
			if err == nil || !strings.Contains(err.Error(), `unknown argument "`+key+`"`) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, key := range []string{"catalog_path", "actor", "session_id", "transport", "provider"} {
		_, err := handler(actorContext(), map[string]any{
			"op": "materialize", "node_id": "subject", "phase": "subject", key: "unsafe",
		})
		if err == nil || !strings.Contains(err.Error(), `unknown argument "`+key+`"`) {
			t.Fatalf("authority field %q error = %v", key, err)
		}
	}
}

func TestTypedOperationsUseOpaqueRefsAndSurviveRestart(t *testing.T) {
	deps, _, _, tools := testDependencies(t)
	handler := NewHandler(deps)
	ctx := actorContext()

	planned, err := handler(ctx, map[string]any{"op": "plan", "node_id": "subject"})
	if err != nil {
		t.Fatal(err)
	}
	manifestRef := planned.Data["manifest_ref"].(string)
	if !strings.HasPrefix(manifestRef, "kitsoki://story-demo/") || strings.Contains(manifestRef, deps.Root) {
		t.Fatalf("unsafe manifest ref %q", manifestRef)
	}

	materialized, err := handler(ctx, map[string]any{
		"op": "materialize", "node_id": "subject", "phase": "subject",
	})
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Data["evidence_ref"] == "" {
		t.Fatalf("materialize = %#v", materialized.Data)
	}
	materializedReceipt, err := deps.Evidence.Resolve(
		ctx,
		materialized.Data["evidence_ref"].(string),
		"materialize",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(materializedReceipt), "job-opaque") ||
		strings.Contains(string(materializedReceipt), "session-opaque") {
		t.Fatalf("materialize receipt leaks runtime identity: %s", materializedReceipt)
	}

	projected, err := handler(ctx, map[string]any{
		"op": "project_mockup", "node_id": "subject", "audience": "internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectedManifest := projected.Data["manifest_ref"].(string)
	created, err := handler(ctx, map[string]any{"op": "create_mockup", "manifest_ref": projectedManifest})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Data["mockup_ref"].(string), "demo-artifact:") {
		t.Fatalf("mockup ref = %q", created.Data["mockup_ref"])
	}
	if !strings.HasPrefix(created.Data["bundle_ref"].(string), "application-bundle:") {
		t.Fatalf("bundle ref = %q", created.Data["bundle_ref"])
	}

	restarted := NewHandler(deps)
	if _, err := restarted(ctx, map[string]any{"op": "create_mockup", "manifest_ref": projectedManifest}); err != nil {
		t.Fatal(err)
	}
	if deps.Artifacts.(*fakeArtifacts).calls != 2 {
		t.Fatalf("artifact calls after restart = %d, want 2 operations", deps.Artifacts.(*fakeArtifacts).calls)
	}
	recorded, err := restarted(ctx, map[string]any{"op": "record", "manifest_ref": projectedManifest})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recorded.Data["record_ref"].(string); !ok {
		t.Fatalf("record result = %#v", recorded.Data)
	}
	if _, err := NewHandler(deps)(ctx, map[string]any{"op": "record", "manifest_ref": projectedManifest}); err != nil {
		t.Fatal(err)
	}
	if tools.recordCalls != 1 {
		t.Fatalf("record calls after restart = %d, want 1", tools.recordCalls)
	}
	if tools.lastRecord.Path != "" || tools.lastRecord.Capture == nil ||
		tools.lastRecord.Capture.ApplicationID != "artifact-producer" ||
		tools.lastRecord.Capture.ScenarioRef == "" {
		t.Fatalf("projected capture manifest = %#v", tools.lastRecord)
	}
	otherActor := host.WithActor(context.Background(), "other@example.test")
	if _, err := NewHandler(deps)(otherActor, map[string]any{
		"op": "record", "manifest_ref": projectedManifest,
	}); err != nil {
		t.Fatal(err)
	}
	if tools.recordCalls != 2 {
		t.Fatalf("cross-actor record reused another actor's receipt: calls = %d", tools.recordCalls)
	}
	checked, err := restarted(ctx, map[string]any{"op": "doctor", "manifest_ref": projectedManifest})
	if err != nil {
		t.Fatal(err)
	}
	if checked.Data["ok"] != true {
		t.Fatalf("doctor result = %#v", checked.Data)
	}
	if _, err := NewHandler(deps)(ctx, map[string]any{"op": "doctor", "manifest_ref": projectedManifest}); err != nil {
		t.Fatal(err)
	}
	if tools.doctorCalls != 1 {
		t.Fatalf("doctor calls after restart = %d, want 1", tools.doctorCalls)
	}
}

func TestConcurrentMaterializeExecutesOnce(t *testing.T) {
	deps, _, artifacts, _ := testDependencies(t)
	handler := NewHandler(deps)
	ctx := actorContext()
	const goroutines = 12
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := handler(ctx, map[string]any{
				"op": "materialize", "node_id": "subject", "phase": "subject",
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("materialize error = %v", err)
		}
	}
	if artifacts.calls != 1 {
		t.Fatalf("artifact calls = %d, want 1", artifacts.calls)
	}
}

func TestReferencesAreApplicationScoped(t *testing.T) {
	deps, _, _, _ := testDependencies(t)
	handler := NewHandler(deps)
	planned, err := handler(actorContext(), map[string]any{
		"op": "plan", "node_id": "subject",
	})
	if err != nil {
		t.Fatal(err)
	}
	other := deps
	other.AppID = "other-app"
	other.Authorizer = BoundAuthorizer{AppID: "other-app", Root: deps.Root}
	other.Evidence = FileEvidenceStore{
		Dir: filepath.Join(deps.Root, ".artifacts", "refs"), Scope: ScopeID("other-app", deps.Root),
	}
	_, err = NewHandler(other)(actorContext(), map[string]any{
		"op": "doctor", "manifest_ref": planned.Data["manifest_ref"],
	})
	if err == nil || !strings.Contains(err.Error(), "outside the bound application scope") {
		t.Fatalf("cross-app error = %v", err)
	}
}

func TestPhaseAndBoundsFailInsteadOfTruncating(t *testing.T) {
	deps, resolver, _, _ := testDependencies(t)
	handler := NewHandler(deps)
	_, err := handler(actorContext(), map[string]any{
		"op": "materialize", "node_id": "subject", "phase": "all",
	})
	if err == nil || !strings.Contains(err.Error(), "phase must be") {
		t.Fatalf("phase error = %v", err)
	}
	resolver.plan.ClosureOrder = make([]string, maxClosureNodes+1)
	_, err = handler(actorContext(), map[string]any{"op": "plan", "node_id": "subject"})
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("bounds error = %v", err)
	}
}

func TestTypedContractRegistrationAndEffects(t *testing.T) {
	wantOps := map[string][]string{
		"plan":           {"node_id"},
		"materialize":    {"node_id", "phase"},
		"project_mockup": {"node_id", "audience"},
		"create_mockup":  {"manifest_ref"},
		"record":         {"manifest_ref"},
		"doctor":         {"manifest_ref"},
	}
	schemas := opschema.Builtins()
	vocabulary := map[string]bool{}
	for _, name := range starlarkhost.BuiltinHostVerbVocabulary {
		vocabulary[name] = true
	}
	for op, fields := range wantOps {
		spec, ok := schemas.Lookup("host.demo", op)
		if !ok {
			t.Fatalf("missing opschema host.demo.%s", op)
		}
		for _, field := range fields {
			if _, ok := spec.Input[field]; !ok {
				t.Errorf("host.demo.%s missing input %q", op, field)
			}
		}
		for _, forbidden := range []string{"cmd", "command", "script", "path", "url", "args", "credentials"} {
			if _, ok := spec.Input[forbidden]; ok {
				t.Errorf("host.demo.%s exposes forbidden input %q", op, forbidden)
			}
		}
		if !vocabulary["host.demo."+op] {
			t.Errorf("host.demo.%s missing from Starlark vocabulary", op)
		}
	}
	effects := map[string]string{
		"plan": "read", "materialize": "write", "project_mockup": "read",
		"create_mockup": "write", "record": "write", "doctor": "read",
	}
	for op, want := range effects {
		got, _ := host.ClassifyDispatchedCall("host.demo."+op, nil)
		if string(got) != want {
			t.Errorf("host.demo.%s effect = %s, want %s", op, got, want)
		}
	}
}
