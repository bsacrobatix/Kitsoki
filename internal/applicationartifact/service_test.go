package applicationartifact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/effect"
)

func TestServiceExecutesBoundPhasesAndReplaysApplicationScope(t *testing.T) {
	def := artifactApplicationDef()
	replay := appplatform.NewMemoryReplayStore()
	registry := appplatform.NewRegistry(appplatform.Dependencies{Replay: replay})
	calls := 0
	if err := registry.RegisterHandler(appplatform.HandlerDefinition{
		ID: "artifact.produce", Name: "Produce", Description: "Produce artifact",
		SemanticRef: "producer.handler.produce", Session: appplatform.SessionRequired,
		Effect: appplatform.EffectWrite, RoutingMode: appplatform.RoutingExact,
		Outcomes: []string{"ok"}, Expose: []appplatform.Transport{appplatform.TransportJSONRPC},
		Idempotency: appplatform.IdempotencyRequired, IdempotencyKeyField: "request_id",
		IdempotencyScope: "application",
	}, appplatform.HandlerFunc(func(_ context.Context, invocation appplatform.Invocation) (appplatform.HandlerResult, error) {
		calls++
		if invocation.Actor != "application-artifact:caller" {
			t.Fatalf("actor = %q", invocation.Actor)
		}
		return appplatform.HandlerResult{
			Outcome: "ok",
			Output:  json.RawMessage(`{"artifact_ref":"demo-artifact:abc123","ignored":"private"}`),
		}, nil
	})); err != nil {
		t.Fatal(err)
	}
	service := Service{
		Definition:  def,
		Application: appplatform.Service{Registry: registry},
		SessionID:   "persisted-session",
		Actor:       "application-artifact:caller",
		Binding: Binding{
			ApplicationID: "producer",
			Phases: []Phase{{
				ID: "produce", Action: "artifact.publish", ArtifactOutputs: []string{"artifact_ref"},
			}},
			PrimaryOutput: "artifact_ref",
		},
	}
	request := Request{
		CallerApplicationID: "caller",
		Operation:           "create_mockup",
		Input:               json.RawMessage(`{"request_id":"opaque","node_id":"n1"}`),
	}
	first, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if first.Primary != "demo-artifact:abc123" ||
		first.Artifacts["artifact_ref"] != first.Primary ||
		len(first.ReceiptIDs) != 1 ||
		len(second.ReceiptIDs) != 1 ||
		second.ReceiptIDs[0] == first.ReceiptIDs[0] {
		t.Fatalf("first = %#v second = %#v", first, second)
	}
}

func TestValidateBindingRejectsUnexportedUnsafeAndNonApplicationIdempotency(t *testing.T) {
	def := artifactApplicationDef()
	cases := []struct {
		name    string
		binding Binding
		want    string
	}{
		{
			name: "wrong app",
			binding: Binding{
				ApplicationID: "other",
				Phases:        []Phase{{ID: "p", Handler: "artifact.produce", ArtifactOutputs: []string{"artifact_ref"}}},
			},
			want: "does not match",
		},
		{
			name: "unexported action",
			binding: Binding{
				ApplicationID: "producer",
				Phases:        []Phase{{ID: "p", Action: "artifact.private", ArtifactOutputs: []string{"artifact_ref"}}},
			},
			want: "not exported",
		},
		{
			name: "authority shaped handler",
			binding: Binding{
				ApplicationID: "producer",
				Phases:        []Phase{{ID: "p", Handler: "../run.sh", ArtifactOutputs: []string{"artifact_ref"}}},
			},
			want: "malformed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBinding(def, tc.binding); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	def.Exports.Handlers["artifact.produce"].Idempotency.Scope = "session"
	err := ValidateBinding(def, Binding{
		ApplicationID: "producer",
		Phases:        []Phase{{ID: "p", Handler: "artifact.produce", ArtifactOutputs: []string{"artifact_ref"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "scope application") {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceRejectsRawOrPathLikeArtifactOutputs(t *testing.T) {
	def := artifactApplicationDef()
	registry := appplatform.NewRegistry(appplatform.Dependencies{})
	if err := registry.RegisterHandler(appplatform.HandlerDefinition{
		ID: "artifact.produce", Name: "Produce", Description: "Produce artifact",
		SemanticRef: "producer.handler.produce", Session: appplatform.SessionRequired,
		Effect: appplatform.EffectWrite, RoutingMode: appplatform.RoutingExact,
		Outcomes: []string{"ok"}, Expose: []appplatform.Transport{appplatform.TransportJSONRPC},
		Idempotency: appplatform.IdempotencyRequired, IdempotencyKeyField: "request_id",
		IdempotencyScope: "application",
	}, appplatform.HandlerFunc(func(context.Context, appplatform.Invocation) (appplatform.HandlerResult, error) {
		return appplatform.HandlerResult{
			Outcome: "ok", Output: json.RawMessage(`{"artifact_ref":"file:///tmp/secret"}`),
		}, nil
	})); err != nil {
		t.Fatal(err)
	}
	_, err := (Service{
		Definition: def, Application: appplatform.Service{Registry: registry},
		SessionID: "session", Actor: "server",
		Binding: Binding{
			ApplicationID: "producer",
			Phases:        []Phase{{ID: "p", Handler: "artifact.produce", ArtifactOutputs: []string{"artifact_ref"}}},
		},
	}).Execute(context.Background(), Request{
		CallerApplicationID: "caller", Operation: "materialize.subject",
		Input: json.RawMessage(`{"request_id":"opaque"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "opaque artifact handle") {
		t.Fatalf("error = %v", err)
	}
}

func artifactApplicationDef() *app.AppDef {
	return &app.AppDef{
		App: app.AppMeta{ID: "producer"},
		Application: &app.ApplicationContract{
			Actions: map[string]*app.ApplicationAction{
				"artifact.publish": {Handler: "artifact.produce"},
			},
		},
		Exports: &app.ExportsBlock{
			Application: &app.ApplicationExports{Actions: []string{"artifact.publish"}},
			Handlers: map[string]*app.ApplicationHandler{
				"artifact.produce": {
					Effect: effect.Write, Expose: []string{string(appplatform.TransportJSONRPC)},
					Idempotency: &app.HandlerIdempotencyPolicy{Key: "request_id", Scope: "application"},
				},
			},
		},
	}
}
