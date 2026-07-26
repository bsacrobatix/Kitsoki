package application

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// TestApplicationCrossSurfaceConformance is the no-LLM scenario inventory for
// the transport-neutral application boundary. Individual adapter packages test
// their protocol encoding; this test proves they all consume one discovery,
// action, routing, frame, and receipt contract.
func TestApplicationCrossSurfaceConformance(t *testing.T) {
	transports := []Transport{
		TransportWeb,
		TransportVSCode,
		TransportTUI,
		TransportCLI,
		TransportMCP,
		TransportJSONRPC,
	}
	frame := conformanceFrame()
	registry := NewRegistry(Dependencies{
		Schemas: &JSONSchemaValidator{},
		Events:  conformanceEventRuntime{},
	})
	def := HandlerDefinition{
		ID: "demo.advance", Name: "Advance", Description: "Advance the deterministic wizard.",
		SemanticRef: "demo.handler.advance",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"required":["step"],
			"properties":{"step":{"type":"integer"}},
			"additionalProperties":false
		}`),
		OutputSchema: json.RawMessage(`{
			"type":"object",
			"required":["accepted"],
			"properties":{"accepted":{"type":"boolean"}},
			"additionalProperties":false
		}`),
		Session: SessionRequired, Effect: EffectRead, RoutingMode: RoutingExact,
		Outcomes: []string{"ok"}, Expose: transports,
	}
	if err := registry.RegisterHandler(def, HandlerFunc(func(_ context.Context, invocation Invocation) (HandlerResult, error) {
		return HandlerResult{
			Outcome: "ok", Output: json.RawMessage(`{"accepted":true}`),
			RoutingResolved: invocation.RoutingMode,
		}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterEvent(EventDefinition{
		ID: "demo.updated", Source: "demo.updated", Session: SessionRequired,
		Mode: EventBackground, RoutingMode: RoutingExact, Handler: def.ID,
		InputSchema: def.InputSchema,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	service := Service{Registry: registry, Frames: conformanceFrames{frame: frame}}

	var wantDiscovery []HandlerDefinition
	var wantCall normalizedConformanceOutcome
	var wantAction normalizedConformanceOutcome
	for i, transport := range transports {
		discovered, err := service.Discover(context.Background(), transport)
		if err != nil {
			t.Fatalf("%s discovery: %v", transport, err)
		}
		if i == 0 {
			wantDiscovery = discovered
		} else if !reflect.DeepEqual(discovered, wantDiscovery) {
			t.Fatalf("%s discovery differs:\n got %#v\nwant %#v", transport, discovered, wantDiscovery)
		}

		call, err := service.Call(context.Background(), transport, CallRequest{
			Handler: def.ID, Input: json.RawMessage(`{"step":1}`),
			SessionID: "session-1", Actor: "operator", RoutingMode: RoutingExact,
		})
		if err != nil {
			t.Fatalf("%s call: %v", transport, err)
		}
		if call.Receipt.Transport != transport {
			t.Fatalf("%s receipt transport = %q", transport, call.Receipt.Transport)
		}
		normalizedCall := normalizeConformanceOutcome(call)
		if i == 0 {
			wantCall = normalizedCall
		} else if !reflect.DeepEqual(normalizedCall, wantCall) {
			t.Fatalf("%s normalized call differs:\n got %#v\nwant %#v", transport, normalizedCall, wantCall)
		}

		action, err := service.DispatchAction(context.Background(), transport, ActionEnvelope{
			Action: "demo.advance", Input: json.RawMessage(`{"step":1}`),
			SessionID: frame.SessionID, FrameRevision: frame.Revision,
			Actor: "operator", RoutingMode: RoutingExact,
		})
		if err != nil {
			t.Fatalf("%s action: %v", transport, err)
		}
		if action.Receipt.Transport != transport || action.Receipt.SemanticRef != def.SemanticRef {
			t.Fatalf("%s action receipt = %#v", transport, action.Receipt)
		}
		normalizedAction := normalizeConformanceOutcome(action)
		if i == 0 {
			wantAction = normalizedAction
		} else if !reflect.DeepEqual(normalizedAction, wantAction) {
			t.Fatalf("%s normalized action differs:\n got %#v\nwant %#v", transport, normalizedAction, wantAction)
		}
	}

	if _, err := service.Call(context.Background(), TransportCLI, CallRequest{
		Handler: def.ID, Input: json.RawMessage(`{"step":1}`),
		SessionID: "session-1", RoutingMode: RoutingLLM,
	}); err == nil {
		t.Fatal("LLM routing weakened an exact conformance pin")
	}

	event, err := service.DispatchEvent(context.Background(), EventEnvelope{
		Event: "demo.updated", Input: json.RawMessage(`{"step":2}`),
		SessionID: "session-1", Actor: "system",
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Receipt.Transport != TransportEvent ||
		event.Receipt.EventID != "demo.updated" ||
		event.Receipt.EventMode != EventBackground ||
		event.Frame == nil ||
		event.Frame.Revision != frame.Revision {
		t.Fatalf("background event outcome = %#v", event)
	}
}

type normalizedConformanceOutcome struct {
	Schema           string
	Handler          string
	Outcome          string
	Output           string
	SemanticRef      string
	SessionID        string
	Effect           EffectClass
	Routing          RoutingReceipt
	Budget           BudgetDecision
	InputDigest      string
	OutputDigest     string
	FrameRevision    uint64
	FrameApplication string
	FramePage        string
}

func normalizeConformanceOutcome(outcome OutcomeEnvelope) normalizedConformanceOutcome {
	normalized := normalizedConformanceOutcome{
		Schema: outcome.Schema, Handler: outcome.Handler, Outcome: outcome.Outcome,
		Output: string(outcome.Output), SemanticRef: outcome.Receipt.SemanticRef,
		SessionID: outcome.Receipt.SessionID, Effect: outcome.Receipt.Effect,
		Routing: outcome.Receipt.Routing, Budget: outcome.Receipt.Budget,
		InputDigest: outcome.Receipt.InputDigest, OutputDigest: outcome.Receipt.OutputDigest,
		FrameRevision: outcome.Receipt.FrameRevision,
	}
	if outcome.Frame != nil {
		normalized.FrameApplication = outcome.Frame.ApplicationID
		normalized.FramePage = outcome.Frame.Page
	}
	return normalized
}

type conformanceFrames struct {
	frame Frame
}

func (f conformanceFrames) CurrentFrame(_ context.Context, sessionID string) (Frame, error) {
	frame := f.frame
	frame.SessionID = sessionID
	return frame, nil
}

type conformanceEventRuntime struct{}

func (conformanceEventRuntime) Enqueue(
	ctx context.Context,
	_ EventDefinition,
	_ HandlerDefinition,
	_ Invocation,
	run EventRun,
) (OutcomeEnvelope, error) {
	return run(ctx)
}

func (conformanceEventRuntime) Interrupt(context.Context, EventDefinition, Invocation) error {
	return nil
}

func conformanceFrame() Frame {
	semantic := func(kind SemanticKind, ref, name string) SemanticNode {
		return SemanticNode{
			Ref: ref, Kind: kind, Name: name, Description: name + " description",
			Source: Provenance{
				Story: "application-conformance", Member: ref, ProgramNode: "program." + ref,
			},
		}
	}
	handlerNode := semantic(SemanticHandler, "demo.handler.advance", "Advance handler")
	actionNode := semantic(SemanticAction, "demo.action.advance", "Advance")
	actionNode.Relationships = []Relationship{{Kind: "handler", Ref: handlerNode.Ref}}
	action := Action{
		ID: "demo.advance", Handler: "demo.advance", Enabled: true,
		RoutingMode: RoutingExact, Semantic: actionNode,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"required":["step"],
			"properties":{"step":{"type":"integer"}},
			"additionalProperties":false
		}`),
	}
	return Frame{
		Schema: FrameSchema, ApplicationID: "demo", SessionID: "session-1",
		Revision: 8, Page: "review",
		Semantic:     semantic(SemanticApplication, "demo.application", "Demo"),
		PageSemantic: semantic(SemanticPage, "demo.page.review", "Review"),
		Workflow: Workflow{
			State: "review.ready", AllowedIntents: []string{"advance"},
		},
		Handlers: []HandlerDescriptor{{ID: "demo.advance", Semantic: handlerNode}},
		Actions:  []Action{action},
		Regions: []Region{{
			ID: "main", Semantic: semantic(SemanticRegion, "demo.region.main", "Main"),
			Cards: []Card{{
				ID: "review", Semantic: semantic(SemanticCard, "demo.card.review", "Review card"),
				Body: []Element{{
					ID: "details", Kind: "form",
					Value: json.RawMessage(`{"step":1}`),
				}},
				Actions: []Action{action},
			}},
		}},
	}
}
