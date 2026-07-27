package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

type flowEvidenceResolverStub struct {
	resolve func(context.Context, FlowEvidenceResolveRequest) (FlowEvidenceTarget, error)
}

func (s flowEvidenceResolverStub) ResolveFlowEvidence(
	ctx context.Context,
	request FlowEvidenceResolveRequest,
) (FlowEvidenceTarget, error) {
	return s.resolve(ctx, request)
}

type flowEvidenceRunnerStub struct {
	run func(context.Context, FlowEvidenceSuite, FlowEvidenceLimits) (FlowEvidenceSuiteResult, error)
}

func (s flowEvidenceRunnerStub) RunFlowEvidence(
	ctx context.Context,
	suite FlowEvidenceSuite,
	limits FlowEvidenceLimits,
) (FlowEvidenceSuiteResult, error) {
	return s.run(ctx, suite, limits)
}

type memoryFlowEvidenceStore struct {
	mu        sync.Mutex
	records   map[string]FlowEvidenceRecord
	lookupErr error
	putErr    error
}

func (s *memoryFlowEvidenceStore) LookupFlowEvidence(
	_ context.Context,
	key string,
) (FlowEvidenceRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookupErr != nil {
		return FlowEvidenceRecord{}, false, s.lookupErr
	}
	record, ok := s.records[key]
	return record, ok, nil
}

func (s *memoryFlowEvidenceStore) PutFlowEvidenceIfAbsent(
	_ context.Context,
	key string,
	record FlowEvidenceRecord,
) (FlowEvidenceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return FlowEvidenceRecord{}, s.putErr
	}
	if s.records == nil {
		s.records = make(map[string]FlowEvidenceRecord)
	}
	if existing, ok := s.records[key]; ok {
		return existing, nil
	}
	s.records[key] = record
	return record, nil
}

func (s *memoryFlowEvidenceStore) onlyRecord(t *testing.T) FlowEvidenceRecord {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		t.Fatalf("stored records = %d, want 1", len(s.records))
	}
	for _, record := range s.records {
		return record
	}
	return FlowEvidenceRecord{}
}

func TestFlowEvidenceRecordIsScopedDeterministicAndPrivate(t *testing.T) {
	scope := flowEvidenceTestScope()
	target := flowEvidenceTestTarget(scope)
	var resolveRequest FlowEvidenceResolveRequest
	var suites []string
	store := &memoryFlowEvidenceStore{}
	now := time.Date(2026, 7, 26, 4, 5, 6, 7, time.UTC)
	provider := FlowEvidenceProvider{
		CatalogPath: scope.CatalogPath,
		Resolver: flowEvidenceResolverStub{resolve: func(
			_ context.Context,
			request FlowEvidenceResolveRequest,
		) (FlowEvidenceTarget, error) {
			resolveRequest = request
			return target, nil
		}},
		Runner: flowEvidenceRunnerStub{run: func(
			_ context.Context,
			suite FlowEvidenceSuite,
			limits FlowEvidenceLimits,
		) (FlowEvidenceSuiteResult, error) {
			suites = append(suites, suite.ID)
			if limits.MaxRuns < 1 || limits.MaxEvidenceBytes != flowEvidenceMaxBytes {
				t.Fatalf("runner limits = %#v", limits)
			}
			return passingFlowEvidenceSuite(suite.ID), nil
		}},
		Store: store,
		Clock: clock.NewFake(now),
	}
	handler := NewFlowEvidenceHandler(provider, scope)
	result, err := handler(
		WithActor(context.Background(), "private-actor"),
		map[string]any{
			"op": "record", "catalog_path": scope.CatalogPath, "node_id": "node-one",
		},
	)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if result.Error != "" || result.Data["passed"] != true || result.Data["run_count"] != 2 {
		t.Fatalf("result = %#v", result)
	}
	if got, _ := result.Data["evidence_ref"].(string); !strings.HasPrefix(got, "flow-evidence:") {
		t.Fatalf("evidence_ref = %q", got)
	}
	if resolveRequest.Scope != scope || resolveRequest.Actor != "private-actor" ||
		resolveRequest.NodeID != "node-one" {
		t.Fatalf("resolve request = %#v", resolveRequest)
	}
	if strings.Join(suites, ",") != "suite-a,suite-b" {
		t.Fatalf("suite order = %v", suites)
	}

	encoded, err := json.Marshal(result.Data)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, secret := range []string{
		scope.Owner, "private-actor", scope.CatalogPath,
		"/server/stories/a/app.yaml", "/server/stories/b/app.yaml",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("result leaked %q: %s", secret, encoded)
		}
	}
	record := store.onlyRecord(t)
	if record.RecordedAt != now || !record.Passed || record.RunCount != 2 ||
		record.Actor != "private-actor" || len(record.Suites) != 2 {
		t.Fatalf("stored record = %#v", record)
	}
}

func TestFlowEvidenceReplaysAcrossHandlerRestartAndConcurrentCalls(t *testing.T) {
	scope := flowEvidenceTestScope()
	target := flowEvidenceTestTarget(scope)
	store := &memoryFlowEvidenceStore{}
	var runs atomic.Int32
	provider := FlowEvidenceProvider{
		CatalogPath: scope.CatalogPath,
		Resolver: flowEvidenceResolverStub{resolve: func(
			context.Context,
			FlowEvidenceResolveRequest,
		) (FlowEvidenceTarget, error) {
			return target, nil
		}},
		Runner: flowEvidenceRunnerStub{run: func(
			_ context.Context,
			suite FlowEvidenceSuite,
			_ FlowEvidenceLimits,
		) (FlowEvidenceSuiteResult, error) {
			runs.Add(1)
			time.Sleep(10 * time.Millisecond)
			return passingFlowEvidenceSuite(suite.ID), nil
		}},
		Store: store,
		Clock: clock.NewFake(time.Unix(1, 0).UTC()),
	}
	handler := NewFlowEvidenceHandler(provider, scope)
	args := map[string]any{
		"op": "record", "catalog_path": scope.CatalogPath, "node_id": "node-one",
	}
	var wg sync.WaitGroup
	results := make([]Result, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = handler(WithActor(context.Background(), "actor"), args)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if runs.Load() != 2 {
		t.Fatalf("suite runs = %d, want 2 suites once", runs.Load())
	}
	if results[0].Data["evidence_ref"] != results[1].Data["evidence_ref"] {
		t.Fatalf("concurrent refs differ: %#v %#v", results[0], results[1])
	}

	restarted := NewFlowEvidenceHandler(provider, scope)
	replayed, err := restarted(WithActor(context.Background(), "other-actor"), args)
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if runs.Load() != 2 || replayed.Data["evidence_ref"] != results[0].Data["evidence_ref"] {
		t.Fatalf("restart replay = %#v, suite runs = %d", replayed, runs.Load())
	}
}

func TestFlowEvidencePreservesFailingSuite(t *testing.T) {
	scope := flowEvidenceTestScope()
	target := flowEvidenceTestTarget(scope)
	target.Suites = target.Suites[:1]
	store := &memoryFlowEvidenceStore{}
	provider := FlowEvidenceProvider{
		CatalogPath: scope.CatalogPath,
		Resolver:    flowEvidenceStaticResolver(target),
		Runner: flowEvidenceRunnerStub{run: func(
			_ context.Context,
			suite FlowEvidenceSuite,
			_ FlowEvidenceLimits,
		) (FlowEvidenceSuiteResult, error) {
			return FlowEvidenceSuiteResult{
				SuiteID:  suite.ID,
				Passed:   false,
				RunCount: 1,
				Runs: []FlowEvidenceRunResult{{
					Ref: "flow:failed", Passed: false,
					Failures: []string{"expected state complete, got failed"},
				}},
			}, nil
		}},
		Store: store,
		Clock: clock.NewFake(time.Unix(1, 0).UTC()),
	}
	result, err := NewFlowEvidenceHandler(provider, scope)(
		WithActor(context.Background(), "actor"),
		map[string]any{"op": "record", "catalog_path": scope.CatalogPath, "node_id": "node-one"},
	)
	if err != nil {
		t.Fatalf("record failed suite: %v", err)
	}
	if result.Data["passed"] != false || result.Data["run_count"] != 1 {
		t.Fatalf("result = %#v", result)
	}
	record := store.onlyRecord(t)
	if record.Passed || len(record.Suites) != 1 ||
		record.Suites[0].Runs[0].Failures[0] != "expected state complete, got failed" {
		t.Fatalf("failure evidence = %#v", record)
	}
}

func TestFlowEvidenceHonorsInjectedSuiteLimit(t *testing.T) {
	scope := flowEvidenceTestScope()
	target := flowEvidenceTestTarget(scope)
	provider := FlowEvidenceProvider{
		CatalogPath: scope.CatalogPath,
		Resolver:    flowEvidenceStaticResolver(target),
		Runner: flowEvidenceRunnerStub{run: func(
			_ context.Context,
			suite FlowEvidenceSuite,
			_ FlowEvidenceLimits,
		) (FlowEvidenceSuiteResult, error) {
			return passingFlowEvidenceSuite(suite.ID), nil
		}},
		Store: &memoryFlowEvidenceStore{},
		Clock: clock.Real(),
		Limits: FlowEvidenceLimits{
			MaxSuites: 1, MaxRuns: flowEvidenceMaxRuns,
			MaxEvidenceBytes: flowEvidenceMaxBytes,
		},
	}
	_, err := NewFlowEvidenceHandler(provider, scope)(
		WithActor(context.Background(), "actor"),
		flowEvidenceTestArgs(scope),
	)
	if err == nil || !strings.Contains(err.Error(), "exceeds 1") {
		t.Fatalf("suite limit error = %v", err)
	}
}

func TestFlowEvidencePersistsRunnerErrorAsFailure(t *testing.T) {
	scope := flowEvidenceTestScope()
	target := flowEvidenceTestTarget(scope)
	target.Suites = target.Suites[:1]
	store := &memoryFlowEvidenceStore{}
	provider := FlowEvidenceProvider{
		CatalogPath: scope.CatalogPath,
		Resolver:    flowEvidenceStaticResolver(target),
		Runner: flowEvidenceRunnerStub{run: func(
			context.Context,
			FlowEvidenceSuite,
			FlowEvidenceLimits,
		) (FlowEvidenceSuiteResult, error) {
			return FlowEvidenceSuiteResult{}, errors.New("fixture startup failed")
		}},
		Store: store,
		Clock: clock.NewFake(time.Unix(1, 0).UTC()),
	}
	handler := NewFlowEvidenceHandler(provider, scope)
	result, err := handler(
		WithActor(context.Background(), "actor"),
		flowEvidenceTestArgs(scope),
	)
	if err != nil {
		t.Fatalf("record runner error: %v", err)
	}
	if result.Data["passed"] != false || result.Data["run_count"] != 0 {
		t.Fatalf("result = %#v", result)
	}
	record := store.onlyRecord(t)
	if record.Passed || record.RunCount != 0 ||
		len(record.Suites) != 1 ||
		record.Suites[0].Error != "fixture startup failed" {
		t.Fatalf("runner error evidence = %#v", record)
	}

	replayed, err := handler(
		WithActor(context.Background(), "other-actor"),
		flowEvidenceTestArgs(scope),
	)
	if err != nil || replayed.Data["evidence_ref"] != result.Data["evidence_ref"] {
		t.Fatalf("replay runner error = %#v, %v", replayed, err)
	}
}

func TestFlowEvidenceRejectsOversizedEvidenceWithoutTruncation(t *testing.T) {
	scope := flowEvidenceTestScope()
	target := flowEvidenceTestTarget(scope)
	target.Suites = target.Suites[:1]
	store := &memoryFlowEvidenceStore{}
	failures := make([]string, flowEvidenceMaxFailures)
	for i := range failures {
		failures[i] = strings.Repeat("x", flowEvidenceMaxFailureLen)
	}
	provider := FlowEvidenceProvider{
		CatalogPath: scope.CatalogPath,
		Resolver:    flowEvidenceStaticResolver(target),
		Runner: flowEvidenceRunnerStub{run: func(
			_ context.Context,
			suite FlowEvidenceSuite,
			_ FlowEvidenceLimits,
		) (FlowEvidenceSuiteResult, error) {
			return FlowEvidenceSuiteResult{
				SuiteID:  suite.ID,
				Passed:   false,
				RunCount: 1,
				Runs: []FlowEvidenceRunResult{{
					Ref: "flow:oversized", Passed: false, Failures: failures,
				}},
			}, nil
		}},
		Store: store,
		Clock: clock.NewFake(time.Unix(1, 0).UTC()),
	}
	_, err := NewFlowEvidenceHandler(provider, scope)(
		WithActor(context.Background(), "actor"),
		flowEvidenceTestArgs(scope),
	)
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("error = %v, want fail-not-truncate boundary", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("oversized evidence was stored: %#v", store.records)
	}
}

func TestFlowEvidenceRejectsScopeAndBoundViolations(t *testing.T) {
	scope := flowEvidenceTestScope()
	baseTarget := flowEvidenceTestTarget(scope)
	tests := []struct {
		name   string
		args   map[string]any
		target func() FlowEvidenceTarget
		run    func(FlowEvidenceSuite) FlowEvidenceSuiteResult
		want   string
	}{
		{
			name: "catalog mismatch",
			args: map[string]any{
				"op": "record", "catalog_path": "/other/catalog.yaml", "node_id": "node-one",
			},
			target: func() FlowEvidenceTarget { return baseTarget },
			want:   "outside the registered application scope",
		},
		{
			name: "node traversal",
			args: map[string]any{
				"op": "record", "catalog_path": scope.CatalogPath, "node_id": "../node",
			},
			target: func() FlowEvidenceTarget { return baseTarget },
			want:   "catalog node id",
		},
		{
			name: "too many suites",
			args: flowEvidenceTestArgs(scope),
			target: func() FlowEvidenceTarget {
				target := baseTarget
				target.Suites = make([]FlowEvidenceSuite, flowEvidenceMaxSuites+1)
				for i := range target.Suites {
					target.Suites[i] = FlowEvidenceSuite{
						ID: "suite-" + string(rune('a'+i)), Revision: "1",
						AppPath: "/app.yaml", FlowGlob: "/flows/*.yaml",
					}
				}
				return target
			},
			want: "refusing to truncate",
		},
		{
			name:   "runner exceeds run limit",
			args:   flowEvidenceTestArgs(scope),
			target: func() FlowEvidenceTarget { return baseTarget },
			run: func(suite FlowEvidenceSuite) FlowEvidenceSuiteResult {
				return FlowEvidenceSuiteResult{
					SuiteID:  suite.ID,
					RunCount: flowEvidenceMaxRuns + 1,
					Runs:     make([]FlowEvidenceRunResult, flowEvidenceMaxRuns+1),
				}
			},
			want: "refusing to truncate",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryFlowEvidenceStore{}
			runnerCalls := 0
			provider := FlowEvidenceProvider{
				CatalogPath: scope.CatalogPath,
				Resolver: flowEvidenceResolverStub{resolve: func(
					context.Context,
					FlowEvidenceResolveRequest,
				) (FlowEvidenceTarget, error) {
					return test.target(), nil
				}},
				Runner: flowEvidenceRunnerStub{run: func(
					_ context.Context,
					suite FlowEvidenceSuite,
					_ FlowEvidenceLimits,
				) (FlowEvidenceSuiteResult, error) {
					runnerCalls++
					if test.run != nil {
						return test.run(suite), nil
					}
					return passingFlowEvidenceSuite(suite.ID), nil
				}},
				Store: store,
				Clock: clock.NewFake(time.Unix(1, 0).UTC()),
			}
			_, err := NewFlowEvidenceHandler(provider, scope)(
				WithActor(context.Background(), "actor"),
				test.args,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if len(store.records) != 0 {
				t.Fatalf("invalid request stored evidence: %#v", store.records)
			}
			if test.name != "runner exceeds run limit" && runnerCalls != 0 {
				t.Fatalf("runner calls = %d", runnerCalls)
			}
		})
	}
}

func TestFlowEvidenceFailsClosedBeforeRunAndAfterPersistenceFailure(t *testing.T) {
	scope := flowEvidenceTestScope()
	target := flowEvidenceTestTarget(scope)
	args := flowEvidenceTestArgs(scope)
	result, err := FlowEvidenceHandler(WithActor(context.Background(), "actor"), args)
	if err != nil || !strings.Contains(result.Error, "unavailable") {
		t.Fatalf("unavailable result = %#v, %v", result, err)
	}
	provider := FlowEvidenceProvider{
		CatalogPath: scope.CatalogPath,
		Resolver:    flowEvidenceStaticResolver(target),
		Runner: flowEvidenceRunnerStub{run: func(
			_ context.Context,
			suite FlowEvidenceSuite,
			_ FlowEvidenceLimits,
		) (FlowEvidenceSuiteResult, error) {
			return passingFlowEvidenceSuite(suite.ID), nil
		}},
		Store: &memoryFlowEvidenceStore{},
		Clock: clock.NewFake(time.Unix(1, 0).UTC()),
	}
	result, err = NewFlowEvidenceHandler(provider, scope)(context.Background(), args)
	if err != nil || !strings.Contains(result.Error, "authenticated actor") {
		t.Fatalf("anonymous result = %#v, %v", result, err)
	}
	result, err = NewFlowEvidenceHandler(provider, scope)(
		WithActor(context.Background(), strings.Repeat("x", 513)),
		args,
	)
	if err != nil || !strings.Contains(result.Error, "safe evidence boundary") {
		t.Fatalf("unsafe actor result = %#v, %v", result, err)
	}

	var runs atomic.Int32
	provider.Runner = flowEvidenceRunnerStub{run: func(
		_ context.Context,
		suite FlowEvidenceSuite,
		_ FlowEvidenceLimits,
	) (FlowEvidenceSuiteResult, error) {
		runs.Add(1)
		return passingFlowEvidenceSuite(suite.ID), nil
	}}
	provider.Store = &memoryFlowEvidenceStore{lookupErr: errors.New("store offline")}
	_, err = NewFlowEvidenceHandler(provider, scope)(WithActor(context.Background(), "actor"), args)
	if err == nil || !strings.Contains(err.Error(), "store offline") || runs.Load() != 0 {
		t.Fatalf("lookup failure = %v, runs = %d", err, runs.Load())
	}

	provider.Store = &memoryFlowEvidenceStore{putErr: errors.New("write failed")}
	_, err = NewFlowEvidenceHandler(provider, scope)(WithActor(context.Background(), "actor"), args)
	if err == nil || !strings.Contains(err.Error(), "write failed") || runs.Load() != 2 {
		t.Fatalf("put failure = %v, runs = %d", err, runs.Load())
	}
}

func TestFlowEvidenceRegistrationSchemaAndEffect(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	result, err := registry.Invoke(
		WithActor(context.Background(), "actor"),
		"host.flow_evidence.record",
		map[string]any{"catalog_path": "catalog", "node_id": "node"},
	)
	if err != nil {
		t.Fatalf("invoke sentinel: %v", err)
	}
	if !strings.Contains(result.Error, "unavailable") {
		t.Fatalf("sentinel error = %q", result.Error)
	}

	class, deterministic := ClassifyDispatchedCall(
		"host.flow_evidence",
		map[string]any{"op": "record"},
	)
	if class != effect.Write || deterministic {
		t.Fatalf("classification = (%q, %v)", class, deterministic)
	}
	class, deterministic = ClassifyDispatchedCall("host.flow_evidence.record", nil)
	if class != effect.Write || deterministic {
		t.Fatalf("leaf classification = (%q, %v)", class, deterministic)
	}
	spec, ok := opschema.Builtins().Lookup("host.flow_evidence", "record")
	if !ok || len(spec.Input) != 1 ||
		spec.Input["node_id"].Type != "string" ||
		spec.Output["evidence_ref"].Type != "string" ||
		spec.Output["passed"].Type != "bool" ||
		spec.Output["run_count"].Type != "int" {
		t.Fatalf("opschema = %#v, %v", spec, ok)
	}
}

func flowEvidenceTestScope() FlowEvidenceScope {
	return FlowEvidenceScope{
		ApplicationID: "review-app",
		Owner:         "private-owner",
		Revision:      "1.2.3",
		CatalogPath:   "/server/private/catalog.yaml",
	}
}

func flowEvidenceTestTarget(scope FlowEvidenceScope) FlowEvidenceTarget {
	return FlowEvidenceTarget{
		Scope: scope, CatalogRevision: "catalog-rev-1", NodeID: "node-one",
		Suites: []FlowEvidenceSuite{
			{
				ID: "suite-b", Revision: "suite-b-rev",
				AppPath: "/server/stories/b/app.yaml", FlowGlob: "/server/stories/b/flows/*.yaml",
			},
			{
				ID: "suite-a", Revision: "suite-a-rev",
				AppPath: "/server/stories/a/app.yaml", FlowGlob: "/server/stories/a/flows/*.yaml",
			},
		},
	}
}

func flowEvidenceStaticResolver(target FlowEvidenceTarget) flowEvidenceResolverStub {
	return flowEvidenceResolverStub{resolve: func(
		context.Context,
		FlowEvidenceResolveRequest,
	) (FlowEvidenceTarget, error) {
		return target, nil
	}}
}

func passingFlowEvidenceSuite(suiteID string) FlowEvidenceSuiteResult {
	return FlowEvidenceSuiteResult{
		SuiteID:  suiteID,
		Passed:   true,
		RunCount: 1,
		Runs: []FlowEvidenceRunResult{{
			Ref: "flow:" + suiteID, Passed: true,
		}},
	}
}

func flowEvidenceTestArgs(scope FlowEvidenceScope) map[string]any {
	return map[string]any{
		"op": "record", "catalog_path": scope.CatalogPath, "node_id": "node-one",
	}
}
