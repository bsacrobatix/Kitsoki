package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"kitsoki/internal/app"
)

func semantic(ref string, kind SemanticKind) SemanticNode {
	return SemanticNode{
		Ref: ref, Kind: kind, Name: ref + " name", Description: ref + " purpose",
		Source: Provenance{Story: "test", Member: "application." + ref},
	}
}

func testFrame() Frame {
	app := semantic("test.application", SemanticApplication)
	region := semantic("test.region.main", SemanticRegion)
	card := semantic("test.card.items", SemanticCard)
	action := semantic("test.action.open", SemanticAction)
	card.Relationships = []Relationship{{Kind: "action", Ref: action.Ref}}
	return Frame{
		Schema: FrameSchema, ApplicationID: "test", SessionID: "session-1",
		Revision: 7, Page: "home", Semantic: app,
		PageSemantic: semantic("test.page.home", SemanticPage),
		Workflow:     Workflow{State: "ready", AllowedIntents: []string{"open"}},
		Regions: []Region{{
			ID: "main", Semantic: region, Cards: []Card{{
				ID: "items", Semantic: card,
				Actions: []Action{{
					ID: "test.open", Handler: "test.open", Enabled: true, Semantic: action,
					InputSchema: json.RawMessage(`{"type":"object"}`),
				}},
			}},
		}},
	}
}

func TestFrameValidateInspectAndActionRevision(t *testing.T) {
	frame := testFrame()
	if err := frame.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	inspection, ok, err := frame.Inspect("test.action.open", 10)
	if err != nil || !ok {
		t.Fatalf("Inspect() = (_, %v, %v)", ok, err)
	}
	if inspection.Current.FrameRevision != 7 {
		t.Fatalf("frame revision = %d, want 7", inspection.Current.FrameRevision)
	}
	if len(inspection.Incoming) != 1 || inspection.Incoming[0].Ref != "test.card.items" {
		t.Fatalf("incoming = %#v", inspection.Incoming)
	}

	envelope := ActionEnvelope{Action: "test.open", SessionID: "session-1", FrameRevision: 6}
	if _, err := ValidateActionEnvelope(frame, envelope); !errors.Is(err, ErrStaleFrame) {
		t.Fatalf("ValidateActionEnvelope() error = %v, want ErrStaleFrame", err)
	}
	var stale *StaleFrameError
	if _, err := ValidateActionEnvelope(frame, envelope); !errors.As(err, &stale) || stale.Current != 7 {
		t.Fatalf("stale error = %#v, %v", stale, err)
	}
	envelope.FrameRevision = 7
	action, err := ValidateActionEnvelope(frame, envelope)
	if err != nil || action.Handler != "test.open" {
		t.Fatalf("ValidateActionEnvelope() = %#v, %v", action, err)
	}

	frame.Regions[0].Cards[0].Actions = nil
	if _, err := ValidateActionEnvelope(frame, envelope); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("unoffered action error = %v, want ErrActionNotFound", err)
	}

	frame = testFrame()
	disabled := false
	frame.Regions[0].Cards[0].Actions[0].State.Enabled = &disabled
	if _, err := ValidateActionEnvelope(frame, envelope); !errors.Is(err, ErrActionDisabled) {
		t.Fatalf("state-disabled action error = %v, want ErrActionDisabled", err)
	}
}

func TestFrameRejectsDanglingAndDuplicateSemantics(t *testing.T) {
	frame := testFrame()
	frame.Regions[0].Semantic = frame.Semantic
	frame.Regions[0].Semantic.Name = "Conflicting placement"
	if err := frame.Validate(); err == nil || !strings.Contains(err.Error(), "conflicting semantic ref") {
		t.Fatalf("Validate() error = %v, want conflicting ref", err)
	}

	frame = testFrame()
	frame.Regions[0].Cards[0].Semantic.Relationships[0].Ref = "test.missing"
	if err := frame.Validate(); err == nil || !strings.Contains(err.Error(), "unknown ref") {
		t.Fatalf("Validate() error = %v, want dangling relationship", err)
	}
}

func TestCompileFrameProjectsValidatedAuthorContract(t *testing.T) {
	def := &app.AppDef{
		App: app.AppMeta{ID: "pog", Version: "1.0.0"},
		Application: &app.ApplicationContract{
			Schema: app.ApplicationSchemaV1, Name: "POG", Description: "Manage product work",
			SemanticRef: "pog.application", Shell: app.ApplicationShell{Entry: "dashboard"},
			Navigation: []app.ApplicationNavigation{{
				ID: "dashboard", Name: "Dashboard", Description: "Open the dashboard",
				SemanticRef: "pog.nav.dashboard", Page: "dashboard",
			}},
			Pages: map[string]*app.ApplicationPage{
				"dashboard": {
					Name: "Portfolio", Description: "Summarize portfolio work", SemanticRef: "pog.page.dashboard",
					Regions: map[string]*app.ApplicationRegion{
						"main": {
							Name: "Overview", Description: "Show active work", SemanticRef: "pog.region.main",
							Items: []app.ApplicationRegionItem{{Card: &app.ApplicationCard{
								ID: "changes", Name: "Changes", Description: "List active changes",
								SemanticRef: "pog.card.changes", Component: "pog.change-list",
								Props: map[string]any{"limit": 5}, Actions: []string{"pog.change.open"},
							}}},
						},
					},
				},
			},
			Components: map[string]*app.ApplicationComponent{
				"pog.change-list": {
					Name: "Change list", Description: "Present change summaries",
					SemanticRef: "pog.component.change-list",
					Web:         &app.ApplicationWebComponent{Module: "ui/ChangeList.vue"},
					Fallback:    &app.ApplicationComponentFallback{Element: "list"},
				},
			},
			Actions: map[string]*app.ApplicationAction{
				"pog.change.open": {
					Name: "Open change", Description: "Open a selected change",
					SemanticRef: "pog.action.change-open", Handler: "pog.change.open",
					InputSchema: "schemas/change-open.json",
				},
			},
		},
		Exports: &app.ExportsBlock{Handlers: map[string]*app.ApplicationHandler{
			"pog.change.open": {
				Name: "Open change", Description: "Open a change and return a frame",
				SemanticRef: "pog.handler.change-open",
			},
		}},
	}
	frame, err := CompileFrame(def, "session-1", 12, "", Workflow{State: "portfolio.ready"})
	if err != nil {
		t.Fatalf("CompileFrame() error = %v", err)
	}
	if frame.Page != "dashboard" || frame.Revision != 12 || len(frame.Regions) != 1 {
		t.Fatalf("frame identity = page %q revision %d regions %d", frame.Page, frame.Revision, len(frame.Regions))
	}
	card := frame.Regions[0].Cards[0]
	if string(card.Body[0].Props) != `{"limit":5}` {
		t.Fatalf("props = %s", card.Body[0].Props)
	}
	if card.Actions[0].InputSchemaRef != "schemas/change-open.json" {
		t.Fatalf("schema ref = %q", card.Actions[0].InputSchemaRef)
	}
	def.Application.Shell.Entry = ""
	defaulted, err := CompileFrame(def, "session-1", 13, "", Workflow{State: "portfolio.ready"})
	if err != nil || defaulted.Page != "dashboard" {
		t.Fatalf("CompileFrame() default page = %q, %v", defaulted.Page, err)
	}
	if got := frame.Capabilities.Presentation; len(got) != 2 || got[1] != "custom-components" {
		t.Fatalf("presentation capabilities = %#v", got)
	}
	inspection, ok, err := frame.Inspect("pog.handler.change-open", 10)
	if err != nil || !ok {
		t.Fatalf("Inspect(handler) = (_, %v, %v)", ok, err)
	}
	if len(inspection.Incoming) != 1 || inspection.Incoming[0].Ref != "pog.action.change-open" {
		t.Fatalf("handler incoming = %#v", inspection.Incoming)
	}
}

type fakeSchemaValidator struct {
	values []string
}

func (f *fakeSchemaValidator) Validate(_ context.Context, _, value json.RawMessage) error {
	f.values = append(f.values, string(value))
	return nil
}

type receiptCollector struct {
	receipts []Receipt
}

func (c *receiptCollector) Record(_ context.Context, receipt Receipt) error {
	c.receipts = append(c.receipts, receipt)
	return nil
}

func readDefinition(id string, expose ...Transport) HandlerDefinition {
	return HandlerDefinition{
		ID: id, Name: "Read", Description: "Read a test resource", SemanticRef: "test.handler." + id,
		InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
		Session: SessionRequired, Effect: EffectRead, RoutingMode: RoutingExact,
		Outcomes: []string{"ok", "not_found"}, Expose: expose,
	}
}

func TestRegistryDiscoveryInvokeAndDeterministicReceipt(t *testing.T) {
	schemas := &fakeSchemaValidator{}
	receipts := &receiptCollector{}
	registry := NewRegistry(Dependencies{Schemas: schemas, Receipts: receipts})
	invocations := 0
	handler := HandlerFunc(func(_ context.Context, invocation Invocation) (HandlerResult, error) {
		invocations++
		if string(invocation.Input) != `{"a":1,"b":2}` {
			t.Fatalf("normalized input = %s", invocation.Input)
		}
		return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{"z":3,"a":1}`)}, nil
	})
	if err := registry.RegisterHandler(readDefinition("test.open", TransportCLI, TransportMCP), handler); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterHandler(readDefinition("test.other", TransportMCP), handler); err != nil {
		t.Fatal(err)
	}
	discovered := registry.Discover(TransportCLI)
	if len(discovered) != 1 || discovered[0].ID != "test.open" {
		t.Fatalf("Discover(cli) = %#v", discovered)
	}

	invocation := Invocation{
		HandlerID: "test.open", Input: json.RawMessage(`{"b":2,"a":1}`),
		SessionID: "session-1", Actor: "operator", Transport: TransportCLI,
	}
	first, err := registry.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	second, err := registry.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Invoke() second error = %v", err)
	}
	if first.Schema != OutcomeSchema || first.Outcome != "ok" || string(first.Output) != `{"a":1,"z":3}` {
		t.Fatalf("outcome = %#v", first)
	}
	if first.Receipt.ID != second.Receipt.ID || first.Receipt.InputDigest != second.Receipt.InputDigest {
		t.Fatalf("receipt not deterministic: %#v vs %#v", first.Receipt, second.Receipt)
	}
	if invocations != 2 || len(receipts.receipts) != 2 || len(schemas.values) != 4 {
		t.Fatalf("invocations=%d receipts=%d schema validations=%d", invocations, len(receipts.receipts), len(schemas.values))
	}
}

func TestRegistryPolicyRoutingAndEventDispatch(t *testing.T) {
	registry := NewRegistry(Dependencies{})
	invalid := readDefinition("external", TransportCLI)
	invalid.Effect = EffectExternal
	if err := registry.RegisterHandler(invalid, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{}, nil
	})); err == nil || !strings.Contains(err.Error(), "idempotency") {
		t.Fatalf("RegisterHandler() error = %v, want idempotency policy", err)
	}

	called := Invocation{}
	handler := HandlerFunc(func(_ context.Context, invocation Invocation) (HandlerResult, error) {
		called = invocation
		return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{"handled":true}`)}, nil
	})
	if err := registry.RegisterHandler(readDefinition("event-target", TransportCLI), handler); err != nil {
		t.Fatal(err)
	}
	event := EventDefinition{
		ID: "changed", Source: "test.changed", InputSchema: json.RawMessage(`{"type":"object"}`),
		Session: SessionRequired, Mode: EventBackground, Handler: "event-target",
	}
	if err := registry.RegisterEvent(event); err != nil {
		t.Fatal(err)
	}
	outcome, err := registry.DispatchEvent(context.Background(), "changed", json.RawMessage(`{"id":1}`), "session-1", "system")
	if err != nil {
		t.Fatalf("DispatchEvent() error = %v", err)
	}
	if called.Transport != TransportEvent || called.EventID != "changed" || outcome.Receipt.EventID != "changed" {
		t.Fatalf("event invocation=%#v receipt=%#v", called, outcome.Receipt)
	}
	if _, err := registry.Invoke(context.Background(), Invocation{
		HandlerID: "event-target", Input: json.RawMessage(`{}`), SessionID: "session-1",
		Transport: TransportCLI, RoutingMode: RoutingLLM,
	}); err == nil || !strings.Contains(err.Error(), "weakens handler pin") {
		t.Fatalf("routing error = %v", err)
	}
}

type staticFrames struct {
	frame Frame
	calls int
}

func (f *staticFrames) CurrentFrame(context.Context, string) (Frame, error) {
	f.calls++
	return f.frame, nil
}

type intentDispatcherFunc func(context.Context, Transport, ActionEnvelope, Action) (OutcomeEnvelope, error)

func (f intentDispatcherFunc) DispatchIntent(ctx context.Context, transport Transport, envelope ActionEnvelope, action Action) (OutcomeEnvelope, error) {
	return f(ctx, transport, envelope, action)
}

func TestServiceDispatchActionIsMechanicalAndRejectsStaleFrame(t *testing.T) {
	schemas := &fakeSchemaValidator{}
	registry := NewRegistry(Dependencies{Schemas: schemas})
	calls := 0
	if err := registry.RegisterHandler(readDefinition("test.open", TransportWeb), HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		calls++
		return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	frames := &staticFrames{frame: testFrame()}
	service := Service{Registry: registry, Frames: frames}
	envelope := ActionEnvelope{
		Action: "test.open", Input: json.RawMessage(`{}`), SessionID: "session-1", FrameRevision: 6,
	}
	if _, err := service.DispatchAction(context.Background(), TransportWeb, envelope); !errors.Is(err, ErrStaleFrame) {
		t.Fatalf("DispatchAction() stale error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("handler called for stale frame")
	}
	envelope.FrameRevision = 7
	outcome, err := service.DispatchAction(context.Background(), TransportWeb, envelope)
	if err != nil {
		t.Fatalf("DispatchAction() error = %v", err)
	}
	if calls != 1 || outcome.Frame == nil || frames.calls != 3 {
		t.Fatalf("calls=%d frame calls=%d outcome frame=%v", calls, frames.calls, outcome.Frame != nil)
	}
	if len(schemas.values) != 3 {
		t.Fatalf("schema validations = %d, want action input plus handler input/output", len(schemas.values))
	}
}

func TestServiceDispatchesIntentActionThroughInjectedRuntime(t *testing.T) {
	frame := testFrame()
	frame.Regions[0].Cards[0].Actions[0].Handler = ""
	frame.Regions[0].Cards[0].Actions[0].Intent = "open"
	called := false
	service := Service{
		Frames: &staticFrames{frame: frame},
		Intents: intentDispatcherFunc(func(_ context.Context, transport Transport, envelope ActionEnvelope, action Action) (OutcomeEnvelope, error) {
			called = true
			if transport != TransportTUI || action.Intent != "open" || envelope.FrameRevision != 7 {
				t.Fatalf("intent dispatch = %q, %#v, %#v", transport, envelope, action)
			}
			return OutcomeEnvelope{Schema: OutcomeSchema, Handler: "intent:open", Outcome: "ok"}, nil
		}),
	}
	outcome, err := service.DispatchAction(context.Background(), TransportTUI, ActionEnvelope{
		Action: "test.open", Input: json.RawMessage(`{}`), SessionID: "session-1", FrameRevision: 7,
	})
	if err != nil {
		t.Fatalf("DispatchAction(intent) error = %v", err)
	}
	if !called || outcome.Outcome != "ok" {
		t.Fatalf("called=%v outcome=%#v", called, outcome)
	}
}

func TestServiceAttachesCurrentFrameToCallsAndEvents(t *testing.T) {
	registry := NewRegistry(Dependencies{})
	def := readDefinition("test.read", TransportCLI)
	if err := registry.RegisterHandler(def, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterEvent(EventDefinition{
		ID: "test.changed", Source: "test.changed", Session: SessionRequired,
		Mode: EventBackground, Handler: "test.read",
	}); err != nil {
		t.Fatal(err)
	}
	frames := &staticFrames{frame: testFrame()}
	service := Service{Registry: registry, Frames: frames}

	call, err := service.Call(context.Background(), TransportCLI, CallRequest{
		Handler: "test.read", SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	event, err := service.DispatchEvent(context.Background(), EventEnvelope{
		Event: "test.changed", SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("DispatchEvent() error = %v", err)
	}
	if call.Frame == nil || event.Frame == nil || frames.calls != 2 {
		t.Fatalf("call frame=%v event frame=%v frame calls=%d", call.Frame != nil, event.Frame != nil, frames.calls)
	}
}

func TestDigestJSONIgnoresObjectKeyOrder(t *testing.T) {
	first, err := DigestJSON(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := DigestJSON(json.RawMessage("{\n  \"a\": 1,\n  \"b\": 2\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digests differ: %q != %q", first, second)
	}
}
