package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/effect"
	"kitsoki/internal/graph"
	"kitsoki/internal/materialize"
)

type fixedMaterializeFrame struct {
	frame appplatform.Frame
}

func (f fixedMaterializeFrame) CurrentFrame(context.Context, string) (appplatform.Frame, error) {
	return f.frame, nil
}

func TestRegisteredMaterializeApplicationExecutorUsesJSONRPCRegistryAuthority(t *testing.T) {
	registry := appplatform.NewRegistry(appplatform.Dependencies{
		Schemas: &appplatform.JSONSchemaValidator{},
	})
	var invocations []appplatform.Invocation
	register := func(id, output string) {
		t.Helper()
		err := registry.RegisterHandler(appplatform.HandlerDefinition{
			ID: id, Name: id, Description: id, SemanticRef: "test." + id,
			InputSchema: json.RawMessage(`{
				"type":"object",
				"required":["catalog_ref","node_id","context_digest"],
				"properties":{
					"catalog_ref":{"type":"string"},
					"node_id":{"type":"string"},
					"context_digest":{"type":"string"}
				},
				"additionalProperties":false
			}`),
			OutputSchema: json.RawMessage(`{"type":"object"}`),
			Session:      appplatform.SessionRequired, Effect: appplatform.EffectRead,
			RoutingMode: appplatform.RoutingExact, Outcomes: []string{"ok"},
			Expose: []appplatform.Transport{appplatform.TransportJSONRPC},
		}, appplatform.HandlerFunc(func(_ context.Context, invocation appplatform.Invocation) (appplatform.HandlerResult, error) {
			invocations = append(invocations, invocation)
			return appplatform.HandlerResult{Outcome: "ok", Output: json.RawMessage(output)}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
	}
	register("evidence.record", `{"evidence_ref":"flow-evidence:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	register("artifact.publish.handler", `{"artifact_ref":"artifact:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)

	frame := appplatform.Frame{
		ApplicationID: "artifact-producer",
		SessionID:     "session-opaque",
		Revision:      7,
		Regions: []appplatform.Region{{Cards: []appplatform.Card{{Actions: []appplatform.Action{{
			ID: "artifact.publish", Handler: "artifact.publish.handler", Enabled: true,
		}}}}}},
	}
	executor := registeredMaterializeApplicationExecutor{
		service: appplatform.Service{
			Registry: registry,
			Frames:   fixedMaterializeFrame{frame: frame},
		},
		sessionID: "session-opaque",
		actor:     "graph.materialize:app-one",
	}
	input := materialize.ApplicationPhaseInput{
		CatalogRef: "product", NodeID: "app-one", ContextDigest: "sha256:context",
	}
	cases := []materialize.ApplicationPhaseRequest{
		{
			ApplicationID: "artifact-producer",
			Phase: graph.MaterializePhaseDecl{
				ID: "record", Handler: "evidence.record", ArtifactOutputs: []string{"evidence_ref"},
			},
			Input: input, IdempotencyKey: "graph-materialize:record",
		},
		{
			ApplicationID: "artifact-producer",
			Phase: graph.MaterializePhaseDecl{
				ID: "publish", Action: "artifact.publish", ArtifactOutputs: []string{"artifact_ref"},
			},
			Input: input, IdempotencyKey: "graph-materialize:publish",
		},
	}
	for _, request := range cases {
		outcome, err := executor.ExecuteApplicationPhase(context.Background(), request)
		if err != nil {
			t.Fatalf("%s: %v", request.Phase.ID, err)
		}
		if outcome.Receipt.Transport != appplatform.TransportJSONRPC || outcome.Receipt.ID == "" {
			t.Fatalf("%s receipt = %+v", request.Phase.ID, outcome.Receipt)
		}
	}
	if len(invocations) != 2 {
		t.Fatalf("invocations = %d, want 2", len(invocations))
	}
	for i, invocation := range invocations {
		if invocation.SessionID != "session-opaque" ||
			invocation.Actor != "graph.materialize:app-one" ||
			invocation.Transport != appplatform.TransportJSONRPC ||
			invocation.IdempotencyKey != cases[i].IdempotencyKey {
			t.Fatalf("invocation %d authority = %+v", i, invocation)
		}
		var inputKeys map[string]any
		if err := json.Unmarshal(invocation.Input, &inputKeys); err != nil {
			t.Fatal(err)
		}
		if got, want := sortedMapKeys(inputKeys), []string{"catalog_ref", "context_digest", "node_id"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("input keys = %v, want %v", got, want)
		}
	}
}

func TestRegisteredMaterializeApplicationExecutorReplaysEffectAfterServerRestart(t *testing.T) {
	root := t.TempDir()
	calls := 0
	request := materialize.ApplicationPhaseRequest{
		ApplicationID: "artifact-producer",
		Phase: graph.MaterializePhaseDecl{
			ID: "publish", Handler: "artifact.publish", ArtifactOutputs: []string{"artifact_ref"},
		},
		Input: materialize.ApplicationPhaseInput{
			CatalogRef: "product", NodeID: "app-one", ContextDigest: "sha256:context",
		},
		IdempotencyKey: "graph-materialize:stable",
	}
	firstServer := newServer(nil, serverConfig{materializeRoot: root})
	firstExecutor := registeredMaterializeApplicationExecutor{
		service:   appplatform.Service{Registry: materializeReplayRegistry(t, firstServer, &calls)},
		sessionID: "session-one",
		actor:     "graph.materialize:app-one",
	}
	first, err := firstExecutor.ExecuteApplicationPhase(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, ".artifacts", "kitsoki", "graph-materialize.application.jsonl")
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("durable materialization journal: %v", err)
	}

	// A new server owns a new journal instance seeded from disk. This models
	// daemon restart rather than sharing a process-memory replay map.
	secondServer := newServer(nil, serverConfig{materializeRoot: root})
	secondExecutor := registeredMaterializeApplicationExecutor{
		service:   appplatform.Service{Registry: materializeReplayRegistry(t, secondServer, &calls)},
		sessionID: "session-two",
		actor:     "graph.materialize:app-one",
	}
	replay, err := secondExecutor.ExecuteApplicationPhase(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !replay.Receipt.Replayed || replay.Receipt.ReplayOf != first.Receipt.ID {
		t.Fatalf("calls=%d first=%+v replay=%+v", calls, first.Receipt, replay.Receipt)
	}
	if replay.Receipt.SessionID != "session-one" {
		t.Fatalf("replayed effect unexpectedly re-executed in second session: %+v", replay.Receipt)
	}
}

func materializeReplayRegistry(t *testing.T, server *Server, calls *int) *appplatform.Registry {
	t.Helper()
	if server.materializeReplay == nil || server.materializeReplayErr != nil {
		t.Fatalf("materialize replay unavailable: %v", server.materializeReplayErr)
	}
	registry := appplatform.NewRegistry(appplatform.Dependencies{
		Schemas:  &appplatform.JSONSchemaValidator{},
		Replay:   server.materializeReplay,
		Receipts: server.materializeReceipts,
	})
	err := registry.RegisterHandler(appplatform.HandlerDefinition{
		ID: "artifact.publish", Name: "Publish", Description: "Publish artifact",
		SemanticRef:  "test.artifact.publish",
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Session:      appplatform.SessionRequired, Effect: appplatform.EffectWrite,
		RoutingMode: appplatform.RoutingExact, Outcomes: []string{"ok"},
		Expose:      []appplatform.Transport{appplatform.TransportJSONRPC},
		Idempotency: appplatform.IdempotencyRequired, IdempotencyScope: "application",
	}, appplatform.HandlerFunc(func(context.Context, appplatform.Invocation) (appplatform.HandlerResult, error) {
		(*calls)++
		return appplatform.HandlerResult{
			Outcome: "ok",
			Output:  json.RawMessage(`{"artifact_ref":"artifact:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestEffectfulTypedMaterializeFailsClosedWithoutDurableReplay(t *testing.T) {
	def := &app.AppDef{
		App: app.AppMeta{ID: "artifact-producer"},
		Exports: &app.ExportsBlock{Handlers: map[string]*app.ApplicationHandler{
			"artifact.publish": {
				Effect: effect.Write,
				Idempotency: &app.HandlerIdempotencyPolicy{
					Key: "request_id", Scope: "application",
				},
			},
		}},
	}
	binding := &materialize.Binding{
		ApplicationID: "artifact-producer",
		Phases: []graph.MaterializePhaseDecl{{
			ID: "publish", Handler: "artifact.publish", ArtifactOutputs: []string{"artifact_ref"},
		}},
	}
	server := newServer(nil, serverConfig{})
	err := server.validateMaterializeApplicationReplay(def, binding)
	if err == nil || err.Error() != "effectful typed materialization requires durable application replay" {
		t.Fatalf("replay error = %v", err)
	}

	def.Exports.Handlers["artifact.publish"].Effect = effect.Read
	if err := server.validateMaterializeApplicationReplay(def, binding); err != nil {
		t.Fatalf("read-only phase should not require durable replay: %v", err)
	}
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
