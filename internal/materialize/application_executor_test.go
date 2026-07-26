package materialize

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/effect"
	"kitsoki/internal/graph"
	"kitsoki/internal/jobs"
)

type recordingApplicationExecutor struct {
	requests []ApplicationPhaseRequest
}

type recordingApplicationWriteback struct {
	appendErr   error
	completeErr error
	records     []MaterializationRecord
}

func (w *recordingApplicationWriteback) AppendEvidence(string, string, EvidenceEntry, string, string) error {
	return w.appendErr
}

func (w *recordingApplicationWriteback) WriteMaterialization(_ string, _ string, record MaterializationRecord) error {
	w.records = append(w.records, record)
	if record.Status == "complete" {
		return w.completeErr
	}
	return nil
}

func (e *recordingApplicationExecutor) ExecuteApplicationPhase(_ context.Context, request ApplicationPhaseRequest) (appplatform.OutcomeEnvelope, error) {
	e.requests = append(e.requests, request)
	handle := "flow-evidence:" + strings.Repeat(string('a'+rune(len(e.requests)-1)), 64)
	output, _ := json.Marshal(map[string]any{request.Phase.ArtifactOutputs[0]: handle})
	return appplatform.OutcomeEnvelope{
		Outcome: "ok",
		Output:  output,
		Receipt: appplatform.Receipt{ID: "ar_" + strings.Repeat(string('1'+rune(len(e.requests)-1)), 32)},
	}, nil
}

func TestPrepareTypedApplicationDoesNotResolveStoryPath(t *testing.T) {
	root, catalogPath := writeTypedMaterializeCatalog(t)
	prep, err := Prepare(Request{
		CatalogPath: catalogPath,
		CatalogRef:  "product",
		RepoRoot:    filepath.Join(root, "must-not-be-read"),
		NodeID:      graph.NodeID("app-one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if prep.Def != nil || prep.StoryAppPath != "" {
		t.Fatalf("typed preparation loaded story authority: def=%v path=%q", prep.Def != nil, prep.StoryAppPath)
	}
	if got, want := prep.Stages, []string{"record", "publish"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
}

func TestTypedApplicationExecutionPublishesOnlyHandlesAndReceipts(t *testing.T) {
	root, catalogPath := writeTypedMaterializeCatalog(t)
	prep, err := Prepare(Request{
		CatalogPath: catalogPath,
		CatalogRef:  "product",
		RepoRoot:    root,
		NodeID:      graph.NodeID("app-one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := &recordingApplicationExecutor{}
	sched := jobs.NewInMemoryScheduler()
	jobID, _, err := prep.Submit(context.Background(), sched, nil, "session-opaque", executor)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sched.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	job, ok := sched.Get(jobID)
	if !ok || job.Status != jobs.JobDone {
		t.Fatalf("job = %+v, found=%v", job, ok)
	}
	if len(executor.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(executor.requests))
	}
	for _, request := range executor.requests {
		if request.Input.CatalogRef != "product" || request.Input.NodeID != "app-one" || request.Input.ContextDigest == "" {
			t.Fatalf("bounded phase input = %+v", request.Input)
		}
		raw, _ := json.Marshal(request.Input)
		if strings.Contains(string(raw), root) || strings.Contains(string(raw), catalogPath) {
			t.Fatalf("phase input leaked resolved path: %s", raw)
		}
	}
	if _, ok := job.Result.Data["artifact_path"]; ok {
		t.Fatalf("typed result exposed artifact_path: %#v", job.Result.Data)
	}
	if _, ok := job.Result.Data["world"]; ok {
		t.Fatalf("typed result exposed world: %#v", job.Result.Data)
	}
	stages, ok := job.Result.Data["stages"].([]Stage)
	if !ok || len(stages) != 2 || stages[0].Status != "complete" || stages[1].Status != "complete" {
		t.Fatalf("typed result stages = %#v, want two complete stages", job.Result.Data["stages"])
	}
	rawResult, _ := json.Marshal(job.Result.Data)
	if strings.Contains(string(rawResult), root) || strings.Contains(string(rawResult), catalogPath) {
		t.Fatalf("typed result leaked path: %s", rawResult)
	}

	rawCatalog, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rawCatalog)
	for _, forbidden := range []string{root, "artifact_path", "catalog_path", "story_path", "file://", "../", "command:", "script:", "url:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("typed writeback leaked authority %q:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "flow-evidence:") || !strings.Contains(text, "receipt_ids") {
		t.Fatalf("typed writeback omitted handles or receipts:\n%s", text)
	}
}

func TestValidateApplicationBindingRequiresExactExports(t *testing.T) {
	binding := &Binding{
		ApplicationID: "artifact-producer",
		Phases: []graph.MaterializePhaseDecl{
			{ID: "record", Handler: "evidence.record", ArtifactOutputs: []string{"evidence_ref"}},
			{ID: "publish", Action: "artifact.publish", ArtifactOutputs: []string{"artifact_ref"}},
		},
	}
	def := &app.AppDef{
		App: app.AppMeta{ID: "artifact-producer"},
		Application: &app.ApplicationContract{Actions: map[string]*app.ApplicationAction{
			"artifact.publish": {Handler: "artifact.publish.handler"},
		}},
		Exports: &app.ExportsBlock{
			Handlers: map[string]*app.ApplicationHandler{
				"evidence.record":          {Expose: []string{"jsonrpc"}, Effect: effect.Read},
				"artifact.publish.handler": {Expose: []string{"jsonrpc"}, Effect: effect.Read},
			},
			Application: &app.ApplicationExports{Actions: []string{"artifact.publish"}},
		},
	}
	if err := ValidateApplicationBinding(def, binding); err != nil {
		t.Fatal(err)
	}
	def.App.ID = "other"
	if err := ValidateApplicationBinding(def, binding); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched application id error = %v", err)
	}
}

func TestValidateApplicationBindingRejectsUnsafeActionsAndEffectfulIdempotency(t *testing.T) {
	base := func() (*app.AppDef, *Binding) {
		return &app.AppDef{
				App: app.AppMeta{ID: "artifact-producer"},
				Application: &app.ApplicationContract{Actions: map[string]*app.ApplicationAction{
					"artifact.publish": {Handler: "artifact.publish.handler"},
				}},
				Exports: &app.ExportsBlock{
					Handlers: map[string]*app.ApplicationHandler{
						"artifact.publish.handler": {
							Expose: []string{"jsonrpc"}, Effect: effect.Write,
							Idempotency: &app.HandlerIdempotencyPolicy{Key: "request_id", Scope: "application"},
						},
					},
					Application: &app.ApplicationExports{Actions: []string{"artifact.publish"}},
				},
			}, &Binding{
				ApplicationID: "artifact-producer",
				Phases: []graph.MaterializePhaseDecl{{
					ID: "publish", Action: "artifact.publish", ArtifactOutputs: []string{"artifact_ref"},
				}},
			}
	}
	cases := []struct {
		name   string
		mutate func(*app.AppDef)
		want   string
	}{
		{
			name: "intent action",
			mutate: func(def *app.AppDef) {
				def.Application.Actions["artifact.publish"] = &app.ApplicationAction{Intent: "publish"}
			},
			want: "must be handler-backed",
		},
		{
			name: "private action handler",
			mutate: func(def *app.AppDef) {
				delete(def.Exports.Handlers, "artifact.publish.handler")
			},
			want: "is not exported",
		},
		{
			name: "missing idempotency",
			mutate: func(def *app.AppDef) {
				def.Exports.Handlers["artifact.publish.handler"].Idempotency = nil
			},
			want: "requires idempotency with scope application",
		},
		{
			name: "optional idempotency",
			mutate: func(def *app.AppDef) {
				def.Exports.Handlers["artifact.publish.handler"].Idempotency = &app.HandlerIdempotencyPolicy{}
			},
			want: "requires idempotency with scope application",
		},
		{
			name: "session idempotency",
			mutate: func(def *app.AppDef) {
				def.Exports.Handlers["artifact.publish.handler"].Idempotency.Scope = "session"
			},
			want: "requires idempotency with scope application",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def, binding := base()
			tc.mutate(def)
			err := ValidateApplicationBinding(def, binding)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	def, binding := base()
	def.Exports.Handlers["artifact.publish.handler"].Effect = effect.Read
	def.Exports.Handlers["artifact.publish.handler"].Idempotency = nil
	if err := ValidateApplicationBinding(def, binding); err != nil {
		t.Fatalf("read-only handler should not require idempotency: %v", err)
	}
}

func TestApplicationPhaseOutputRejectsOversizedHandle(t *testing.T) {
	phase := graph.MaterializePhaseDecl{ID: "record", ArtifactOutputs: []string{"evidence_ref"}}
	output, _ := json.Marshal(map[string]any{
		"evidence_ref": "evidence:" + strings.Repeat("a", maxOpaqueArtifactHandleLength),
	})
	_, err := applicationPhaseOutput(appplatform.OutcomeEnvelope{
		Outcome: "ok",
		Output:  output,
		Receipt: appplatform.Receipt{ID: "ar_" + strings.Repeat("1", 32)},
	}, phase)
	if err == nil || !strings.Contains(err.Error(), "opaque artifact handle") {
		t.Fatalf("oversized handle error = %v", err)
	}
}

func TestTypedApplicationExecutionFailsWhenEvidenceIsNotDurable(t *testing.T) {
	prep := prepareTypedMaterialization(t)
	writeback := &recordingApplicationWriteback{appendErr: errors.New("evidence store unavailable")}
	prep.writeback = writeback
	job := runTypedMaterialization(t, prep, &recordingApplicationExecutor{})
	if job.Status != jobs.JobFailed || !strings.Contains(job.Error, "persist phase") {
		t.Fatalf("job = %+v, want failed durable evidence", job)
	}
	if len(writeback.records) != 1 || writeback.records[0].Status != "failed" {
		t.Fatalf("writeback records = %+v, want failed terminal record", writeback.records)
	}
}

func TestTypedApplicationExecutionFailsWhenCompletionIsNotDurable(t *testing.T) {
	prep := prepareTypedMaterialization(t)
	writeback := &recordingApplicationWriteback{completeErr: errors.New("materialization store unavailable")}
	prep.writeback = writeback
	job := runTypedMaterialization(t, prep, &recordingApplicationExecutor{})
	if job.Status != jobs.JobFailed || !strings.Contains(job.Error, "persist completed materialization") {
		t.Fatalf("job = %+v, want failed terminal writeback", job)
	}
	if len(writeback.records) != 2 ||
		writeback.records[0].Status != "complete" ||
		writeback.records[1].Status != "failed" {
		t.Fatalf("writeback records = %+v, want complete attempt then failed record", writeback.records)
	}
}

func prepareTypedMaterialization(t *testing.T) *Prepared {
	t.Helper()
	root, catalogPath := writeTypedMaterializeCatalog(t)
	prep, err := Prepare(Request{
		CatalogPath: catalogPath,
		CatalogRef:  "product",
		RepoRoot:    root,
		NodeID:      graph.NodeID("app-one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return prep
}

func runTypedMaterialization(t *testing.T, prep *Prepared, executor ApplicationPhaseExecutor) jobs.Job {
	t.Helper()
	sched := jobs.NewInMemoryScheduler()
	jobID, _, err := prep.Submit(context.Background(), sched, nil, "session-opaque", executor)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sched.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	job, ok := sched.Get(jobID)
	if !ok {
		t.Fatal("typed materialization job not found")
	}
	return job
}

func writeTypedMaterializeCatalog(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "catalog.yaml")
	const catalog = `schema: project-object-graph/seed-catalog/v0
catalog: {id: typed-materialize}
type_registry:
  - id: core-node
    schema: graph-type/v0
    required_fields: [id, schema, title, status, visibility]
  - id: changeset
    schema: graph-type/v0
    extends: core-node
  - id: app
    schema: graph-type/v0
    extends: core-node
    artifact: {schema: pog/artifact/application/v0, format: json, presentation: evidence}
    materialize:
      application_id: artifact-producer
      phases:
        - {id: record, handler: evidence.record, artifact_outputs: [evidence_ref]}
        - {id: publish, action: artifact.publish, artifact_outputs: [artifact_ref]}
nodes:
  - schema: graph/app/v0
    id: app-one
    title: Application
    status: active
    visibility: internal
`
	if err := os.WriteFile(path, []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, path
}
