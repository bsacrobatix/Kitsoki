package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "schemas", "change-open.json"), []byte(`{
		"type": "object",
		"required": ["change_id"],
		"properties": {"change_id": {"type": "string"}},
		"additionalProperties": false
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	def := &app.AppDef{
		App: app.AppMeta{ID: "pog", Version: "1.0.0"}, BaseDir: baseDir,
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
	if !strings.Contains(string(card.Actions[0].InputSchema), `"change_id"`) {
		t.Fatalf("materialized schema = %s", card.Actions[0].InputSchema)
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

func TestCompileFrameWithDataProjectsOnlyAllowlistedWorldValues(t *testing.T) {
	def := &app.AppDef{
		App: app.AppMeta{ID: "demo", Version: "1.0.0"},
		Application: &app.ApplicationContract{
			Schema: app.ApplicationSchemaV1, Name: "Demo", Description: "Show live data",
			SemanticRef: "demo.application", Shell: app.ApplicationShell{Entry: "home"},
			Pages: map[string]*app.ApplicationPage{
				"home": {Name: "Home", Description: "Show live data", SemanticRef: "demo.page.home"},
			},
			Data: map[string]*app.ApplicationData{
				"catalog": {
					Source: "world.catalog", Sensitivity: "internal", Policy: "include",
				},
				"token": {
					Source: "world.token", Sensitivity: "secret", Policy: "redact",
				},
				"fingerprint": {
					Source: "world.token", Sensitivity: "secret", Policy: "hash",
				},
				"omitted": {
					Source: "world.token", Sensitivity: "secret", Policy: "exclude",
				},
			},
		},
	}
	world := map[string]any{
		"catalog": map[string]any{"nodes": []any{"a", "b"}},
		"token":   "credential",
		"ambient": "/private/path",
	}
	frame, err := CompileFrameWithData(
		def, "session-1", 1, "", Workflow{State: "ready"}, world,
	)
	if err != nil {
		t.Fatalf("CompileFrameWithData() error = %v", err)
	}
	if len(frame.Data) != 3 {
		t.Fatalf("data = %#v, want three projected entries", frame.Data)
	}
	if got := string(frame.Data["catalog"].Value); got != `{"nodes":["a","b"]}` {
		t.Fatalf("catalog = %s", got)
	}
	if got := string(frame.Data["token"].Value); got != `"[redacted]"` {
		t.Fatalf("redacted token = %s", got)
	}
	if got := string(frame.Data["fingerprint"].Value); !strings.Contains(got, `"sha256:`) {
		t.Fatalf("fingerprint = %s", got)
	}
	if _, ok := frame.Data["omitted"]; ok {
		t.Fatal("excluded data was projected")
	}
	wire, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "ambient") || strings.Contains(string(wire), "/private/path") ||
		strings.Contains(string(wire), "credential") {
		t.Fatalf("frame leaked ambient or secret world data: %s", wire)
	}

	legacy, err := CompileFrame(def, "session-1", 2, "", Workflow{State: "ready"})
	if err != nil {
		t.Fatalf("CompileFrame() error = %v", err)
	}
	if legacy.Data != nil {
		t.Fatalf("legacy compile data = %#v, want nil", legacy.Data)
	}
}

func TestCompileFramePreservesComposedMemberProvenance(t *testing.T) {
	def := &app.AppDef{
		App: app.AppMeta{ID: "parent", Version: "1.0.0"},
		Application: &app.ApplicationContract{
			Schema: app.ApplicationSchemaV1, Name: "Parent", Description: "Compose child applications",
			SemanticRef: "parent.application", Shell: app.ApplicationShell{Entry: "child__home"},
			Pages: map[string]*app.ApplicationPage{
				"child__home": {
					Name: "Child home", Description: "Render the imported child page",
					SemanticRef: "child.page.home",
					Origin: app.ApplicationMemberOrigin{
						Story: "child", Member: "application.pages.home",
					},
				},
				"child__settings": {
					Name: "Parent settings", Description: "Render the parent override",
					SemanticRef: "parent.page.settings", SemanticAliases: []string{"child.page.settings"},
					Origin: app.ApplicationMemberOrigin{
						Story: "parent", Member: "overrides.application.pages.settings",
					},
				},
			},
		},
	}

	frame, err := CompileFrame(def, "session-1", 1, "", Workflow{State: "ready"})
	if err != nil {
		t.Fatalf("CompileFrame() error = %v", err)
	}
	if got := frame.PageSemantic.Source; got.Story != "child" || got.Member != "application.pages.home" {
		t.Fatalf("imported page source = %#v", got)
	}
	for _, page := range frame.Pages {
		if page.ID != "child__settings" {
			continue
		}
		if got := page.Semantic.Source; got.Story != "parent" || got.Member != "overrides.application.pages.settings" {
			t.Fatalf("override page source = %#v", got)
		}
		return
	}
	t.Fatal("override page descriptor was not compiled")
}

func TestCompileFrameAutomaticallyProjectsLegacyTypedView(t *testing.T) {
	def := &app.AppDef{
		App:  app.AppMeta{ID: "legacy", Title: "Legacy flow"},
		Root: "ready",
		Intents: map[string]app.Intent{
			"submit": {Title: "Submit", Description: "Submit the value."},
		},
		States: map[string]*app.State{
			"ready": {
				Description: "Ready",
				View: app.View{Elements: []app.ViewElement{{
					Kind: "choice", ChoiceMode: "single", ChoicePrompt: "Choose",
					ChoiceItems: []app.ChoiceItem{{Label: "Submit", Intent: "submit"}},
				}}},
				On: map[string][]app.Transition{"submit": {{Target: "done"}}},
			},
			"done": {Description: "Done", Terminal: true},
		},
	}
	frame, err := CompileFrame(def, "session-legacy", 1, "ready", Workflow{State: "ready"})
	if err != nil {
		t.Fatalf("CompileFrame: %v", err)
	}
	if def.Application != nil {
		t.Fatal("legacy projection mutated AppDef and would change old surfaces")
	}
	if len(frame.Regions) != 1 || len(frame.Regions[0].Cards) != 1 ||
		len(frame.Regions[0].Cards[0].Body) != 1 {
		t.Fatalf("frame = %#v", frame)
	}
	projected, err := ProjectTUI(frame)
	if err != nil {
		t.Fatalf("ProjectTUI: %v", err)
	}
	var found bool
	for _, element := range projected.View.Elements {
		if element.Kind == "choice" && element.ChoicePrompt == "Choose" {
			found = true
		}
	}
	if !found {
		t.Fatalf("typed legacy element was not preserved: %#v", projected.View.Elements)
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

type sessionManagerFunc func(context.Context, HandlerDefinition, Invocation) (string, error)

func (f sessionManagerFunc) CreateSession(ctx context.Context, def HandlerDefinition, invocation Invocation) (string, error) {
	return f(ctx, def, invocation)
}

type effectPolicyFunc func(context.Context, HandlerDefinition, Invocation) error

func (f effectPolicyFunc) AuthorizeEffect(ctx context.Context, def HandlerDefinition, invocation Invocation) error {
	return f(ctx, def, invocation)
}

type budgetGovernorFunc func(context.Context, HandlerDefinition, Invocation) (BudgetDecision, error)

func (f budgetGovernorFunc) Decide(ctx context.Context, def HandlerDefinition, invocation Invocation) (BudgetDecision, error) {
	return f(ctx, def, invocation)
}

type immediateEventRuntime struct {
	interrupted bool
}

func (r *immediateEventRuntime) Enqueue(_ context.Context, _ EventDefinition, _ HandlerDefinition, _ Invocation, run EventRun) (OutcomeEnvelope, error) {
	return run(context.Background())
}

func (r *immediateEventRuntime) Interrupt(_ context.Context, _ EventDefinition, _ Invocation) error {
	r.interrupted = true
	return nil
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
	registry := NewRegistry(Dependencies{Events: &immediateEventRuntime{}})
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

func TestRegistryValidatesCompensationReferencesAndEffects(t *testing.T) {
	registry := NewRegistry(Dependencies{})
	primary := readDefinition("publish", TransportCLI)
	primary.Effect = EffectExternal
	primary.Idempotency = IdempotencyRequired
	primary.IdempotencyScope = "application"
	primary.Retryable = true
	primary.CompensationHandler = "rollback"
	if err := registry.RegisterHandler(primary, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{Outcome: "ok"}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err == nil || !strings.Contains(err.Error(), "unknown compensation") {
		t.Fatalf("missing compensation error = %v", err)
	}
	rollback := readDefinition("rollback", TransportCLI)
	if err := registry.RegisterHandler(rollback, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{Outcome: "ok"}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err == nil || !strings.Contains(err.Error(), "write or external effect") {
		t.Fatalf("weak compensation error = %v", err)
	}
}

func TestInterruptEventCancelsBeforeHandlerDispatch(t *testing.T) {
	events := &immediateEventRuntime{}
	registry := NewRegistry(Dependencies{Events: events})
	def := readDefinition("event-target", TransportCLI)
	if err := registry.RegisterHandler(def, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		if !events.interrupted {
			t.Fatal("interrupt handler ran before active work was cancelled")
		}
		return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterEvent(EventDefinition{
		ID: "changed", Source: "test.changed", Session: SessionRequired,
		Mode: EventInterrupt, Handler: "event-target",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.DispatchEvent(context.Background(), "changed", nil, "session-1", "system"); err != nil {
		t.Fatal(err)
	}
}

func TestEventAndHandlerSessionPolicyMatrix(t *testing.T) {
	tests := []struct {
		name           string
		event          SessionPolicy
		handler        SessionPolicy
		valid          bool
		wantHandlerSID string
		wantReceiptSID string
		wantCreates    int
	}{
		{name: "none to none", event: SessionNone, handler: SessionNone, valid: true},
		{name: "none to required", event: SessionNone, handler: SessionRequired},
		{name: "none to create", event: SessionNone, handler: SessionCreate, valid: true, wantHandlerSID: "created-1", wantReceiptSID: "created-1", wantCreates: 1},
		{name: "required to none", event: SessionRequired, handler: SessionNone, valid: true, wantReceiptSID: "bound-session"},
		{name: "required to required", event: SessionRequired, handler: SessionRequired, valid: true, wantHandlerSID: "bound-session", wantReceiptSID: "bound-session"},
		{name: "required to create", event: SessionRequired, handler: SessionCreate},
		{name: "create to none", event: SessionCreate, handler: SessionNone, valid: true, wantReceiptSID: "created-1", wantCreates: 1},
		{name: "create to required", event: SessionCreate, handler: SessionRequired, valid: true, wantHandlerSID: "created-1", wantReceiptSID: "created-1", wantCreates: 1},
		{name: "create to create once", event: SessionCreate, handler: SessionCreate, valid: true, wantHandlerSID: "created-1", wantReceiptSID: "created-1", wantCreates: 1},
	}
	for _, mode := range []EventMode{EventBackground, EventInterrupt} {
		for _, tt := range tests {
			t.Run(string(mode)+"/"+tt.name, func(t *testing.T) {
				events := &immediateEventRuntime{}
				creates := 0
				registry := NewRegistry(Dependencies{
					Events: events,
					Sessions: sessionManagerFunc(func(context.Context, HandlerDefinition, Invocation) (string, error) {
						creates++
						return fmt.Sprintf("created-%d", creates), nil
					}),
				})
				var handlerSID string
				handlerDef := readDefinition("target", TransportCLI)
				handlerDef.Session = tt.handler
				if err := registry.RegisterHandler(handlerDef, HandlerFunc(func(_ context.Context, invocation Invocation) (HandlerResult, error) {
					handlerSID = invocation.SessionID
					return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{}`)}, nil
				})); err != nil {
					t.Fatal(err)
				}
				err := registry.RegisterEvent(EventDefinition{
					ID: "changed", Source: "test.changed", Session: tt.event,
					Mode: mode, Handler: "target",
				})
				if !tt.valid {
					if err == nil {
						t.Fatal("incompatible policy pair registered")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				outcome, err := registry.DispatchEvent(
					context.Background(), "changed", json.RawMessage(`{}`), "bound-session", "system",
				)
				if err != nil {
					t.Fatal(err)
				}
				if handlerSID != tt.wantHandlerSID || outcome.Receipt.SessionID != tt.wantReceiptSID || creates != tt.wantCreates {
					t.Fatalf("handler session=%q receipt session=%q creates=%d outcome=%#v", handlerSID, outcome.Receipt.SessionID, creates, outcome)
				}
				if mode == EventInterrupt && !events.interrupted {
					t.Fatal("interrupt mode did not cancel before dispatch")
				}
			})
		}
	}
}

func TestRequiredEventRejectsMissingSessionBeforeRuntime(t *testing.T) {
	events := &immediateEventRuntime{}
	registry := NewRegistry(Dependencies{Events: events})
	def := readDefinition("target", TransportCLI)
	if err := registry.RegisterHandler(def, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		t.Fatal("handler called without required event session")
		return HandlerResult{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterEvent(EventDefinition{
		ID: "changed", Source: "test.changed", Session: SessionRequired,
		Mode: EventInterrupt, Handler: "target",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.DispatchEvent(context.Background(), "changed", nil, "", "system"); err == nil {
		t.Fatal("missing required event session was accepted")
	}
	if events.interrupted {
		t.Fatal("event runtime was called before required-session validation")
	}
}

func TestRegistryHonorsNoneAndCreateSessionPolicies(t *testing.T) {
	created := 0
	registry := NewRegistry(Dependencies{
		Sessions: sessionManagerFunc(func(_ context.Context, _ HandlerDefinition, invocation Invocation) (string, error) {
			created++
			if invocation.SessionID != "definition-context" {
				t.Fatalf("create context session = %q", invocation.SessionID)
			}
			return "created-session", nil
		}),
	})
	seen := map[string]string{}
	register := func(id string, policy SessionPolicy) {
		def := readDefinition(id, TransportCLI)
		def.Session = policy
		if err := registry.RegisterHandler(def, HandlerFunc(func(_ context.Context, invocation Invocation) (HandlerResult, error) {
			seen[id] = invocation.SessionID
			return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{}`)}, nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	register("none", SessionNone)
	register("create", SessionCreate)
	none, err := registry.Invoke(context.Background(), Invocation{
		HandlerID: "none", SessionID: "definition-context", Transport: TransportCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdOutcome, err := registry.Invoke(context.Background(), Invocation{
		HandlerID: "create", SessionID: "definition-context", Transport: TransportCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen["none"] != "" || none.Receipt.SessionID != "" {
		t.Fatalf("session:none leaked session: invocation=%q receipt=%q", seen["none"], none.Receipt.SessionID)
	}
	if created != 1 || seen["create"] != "created-session" || createdOutcome.Receipt.SessionID != "created-session" {
		t.Fatalf("session:create created=%d invocation=%q receipt=%q", created, seen["create"], createdOutcome.Receipt.SessionID)
	}
}

func TestRegistryIdempotencyReplaysAcrossTransportsWithNewReceipt(t *testing.T) {
	receipts := &receiptCollector{}
	registry := NewRegistry(Dependencies{
		Receipts: receipts, Replay: NewMemoryReplayStore(),
	})
	def := readDefinition("write", TransportCLI, TransportMCP)
	def.Effect = EffectWrite
	def.Idempotency = IdempotencyRequired
	def.IdempotencyScope = "application"
	calls := 0
	if err := registry.RegisterHandler(def, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		calls++
		return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{"saved":true}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	first, err := registry.Invoke(context.Background(), Invocation{
		HandlerID: "write", Input: json.RawMessage(`{"id":1}`), SessionID: "one",
		Transport: TransportCLI, IdempotencyKey: "save-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := registry.Invoke(context.Background(), Invocation{
		HandlerID: "write", Input: json.RawMessage(`{"id":1}`), SessionID: "two",
		Transport: TransportMCP, IdempotencyKey: "save-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !replay.Receipt.Replayed || replay.Receipt.ReplayOf != first.Receipt.ID {
		t.Fatalf("calls=%d first=%#v replay=%#v", calls, first.Receipt, replay.Receipt)
	}
	if replay.Receipt.Transport != TransportMCP || replay.Receipt.ID == first.Receipt.ID || len(receipts.receipts) != 2 {
		t.Fatalf("transport replay receipts = %#v", receipts.receipts)
	}
	_, err = registry.Invoke(context.Background(), Invocation{
		HandlerID: "write", Input: json.RawMessage(`{"id":2}`), SessionID: "three",
		Transport: TransportMCP, IdempotencyKey: "save-1",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestIdempotencyScopesHaveDistinctReplayIdentities(t *testing.T) {
	invocation := Invocation{SessionID: "session-1", Actor: "operator-1", Transport: TransportCLI}
	request := readDefinition("request")
	request.IdempotencyScope = "request"
	session := readDefinition("session")
	session.IdempotencyScope = "session"
	application := readDefinition("application")
	application.IdempotencyScope = "application"
	requestID := replaySessionID(request, invocation)
	sessionID := replaySessionID(session, invocation)
	applicationID := replaySessionID(application, invocation)
	if requestID == sessionID || requestID == applicationID || sessionID == applicationID {
		t.Fatalf("scope identities alias: request=%q session=%q application=%q", requestID, sessionID, applicationID)
	}
	if replaySessionID(request, Invocation{
		SessionID: "session-2", Actor: "operator-1", Transport: TransportCLI,
	}) != requestID {
		t.Fatal("request scope unexpectedly depended on session")
	}
	if replaySessionID(request, Invocation{
		SessionID: "session-1", Actor: "operator-1", Transport: TransportMCP,
	}) == requestID {
		t.Fatal("request scope aliased transports")
	}
}

func TestRegistryRunsEffectPolicyBeforeHandler(t *testing.T) {
	called := false
	registry := NewRegistry(Dependencies{
		Effects: effectPolicyFunc(func(context.Context, HandlerDefinition, Invocation) error {
			return errors.New("effect denied")
		}),
	})
	def := readDefinition("read", TransportCLI)
	if err := registry.RegisterHandler(def, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		called = true
		return HandlerResult{Outcome: "ok"}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Invoke(context.Background(), Invocation{
		HandlerID: "read", SessionID: "session-1", Transport: TransportCLI,
	}); err == nil || !strings.Contains(err.Error(), "effect policy denied") {
		t.Fatalf("effect policy error = %v", err)
	}
	if called {
		t.Fatal("handler ran after effect policy denial")
	}
}

func TestRegistryBudgetDenialStopsHandler(t *testing.T) {
	called := false
	registry := NewRegistry(Dependencies{
		Budget: budgetGovernorFunc(func(context.Context, HandlerDefinition, Invocation) (BudgetDecision, error) {
			return BudgetDecision{Allowed: false, Code: "limit_exhausted", Reason: "run budget exhausted"}, nil
		}),
	})
	def := readDefinition("read", TransportCLI)
	if err := registry.RegisterHandler(def, HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		called = true
		return HandlerResult{Outcome: "ok"}, nil
	})); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Invoke(context.Background(), Invocation{
		HandlerID: "read", SessionID: "session-1", Transport: TransportCLI,
	})
	if !errors.Is(err, ErrBudgetDenied) || !strings.Contains(err.Error(), "run budget exhausted") {
		t.Fatalf("budget denial error = %v", err)
	}
	if called {
		t.Fatal("handler ran after budget denial")
	}
}

func TestServiceSurfacesBudgetDegradationInReceiptAndFrame(t *testing.T) {
	registry := NewRegistry(Dependencies{
		Budget: budgetGovernorFunc(func(context.Context, HandlerDefinition, Invocation) (BudgetDecision, error) {
			return BudgetDecision{Allowed: true, Code: "degraded", Reason: "using deterministic routing"}, nil
		}),
	})
	if err := registry.RegisterHandler(readDefinition("read", TransportCLI), HandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{Outcome: "ok", Output: json.RawMessage(`{}`)}, nil
	})); err != nil {
		t.Fatal(err)
	}
	service := Service{Registry: registry, Frames: &staticFrames{frame: testFrame()}}
	outcome, err := service.Call(context.Background(), TransportCLI, CallRequest{
		Handler: "read", SessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Receipt.Budget.Code != "degraded" || outcome.Frame == nil ||
		outcome.Frame.Workflow.BudgetState != "degraded" ||
		outcome.Frame.Workflow.Degradation != "using deterministic routing" {
		t.Fatalf("budget projection = receipt %#v frame %#v", outcome.Receipt.Budget, outcome.Frame)
	}
}

func TestJSONSchemaValidatorRejectsInvalidValue(t *testing.T) {
	validator := &JSONSchemaValidator{}
	schema := json.RawMessage(`{
		"type":"object",
		"required":["name"],
		"properties":{"name":{"type":"string"}},
		"additionalProperties":false
	}`)
	if err := validator.Validate(context.Background(), schema, json.RawMessage(`{"name":"ok"}`)); err != nil {
		t.Fatalf("Validate(valid) error = %v", err)
	}
	err := validator.Validate(context.Background(), schema, json.RawMessage(`{"name":3}`))
	if err == nil || !strings.Contains(err.Error(), "schema validation failed") {
		t.Fatalf("Validate(invalid) error = %v", err)
	}
}

func TestJSONSchemaValidatorResolvesRootedRefsAndCachesCompilation(t *testing.T) {
	root := t.TempDir()
	defs := filepath.Join(root, "defs.json")
	if err := os.WriteFile(defs, []byte(`{
		"$defs":{"name":{"type":"string","minLength":2}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rootURI := (&url.URL{Scheme: "file", Path: filepath.Join(root, "root.json")}).String()
	schema := json.RawMessage(fmt.Sprintf(`{
		"$id":%q,
		"type":"object",
		"required":["name"],
		"properties":{"name":{"$ref":"defs.json#/$defs/name"}}
	}`, rootURI))
	validator := &JSONSchemaValidator{Root: root}
	if err := validator.Validate(context.Background(), schema, json.RawMessage(`{"name":"ok"}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(defs); err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(context.Background(), schema, json.RawMessage(`{"name":"cached"}`)); err != nil {
		t.Fatalf("cached validation reloaded removed ref: %v", err)
	}
}

func TestJSONSchemaValidatorDeniesEscapeAndNetworkRefs(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "story")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.json"), []byte(`{"type":"string"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outside.json"), filepath.Join(root, "linked.json")); err != nil {
		t.Fatal(err)
	}
	rootURI := (&url.URL{Scheme: "file", Path: filepath.Join(root, "root.json")}).String()
	validator := &JSONSchemaValidator{Root: root}
	for name, ref := range map[string]string{
		"escape":  "../outside.json",
		"symlink": "linked.json",
		"network": "https://example.com/schema.json",
	} {
		t.Run(name, func(t *testing.T) {
			schema := json.RawMessage(fmt.Sprintf(`{"$id":%q,"$ref":%q}`, rootURI, ref))
			err := validator.Validate(context.Background(), schema, json.RawMessage(`"value"`))
			if err == nil || (!strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "escapes story root")) {
				t.Fatalf("reference error = %v", err)
			}
		})
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
		Registry: NewRegistry(Dependencies{Schemas: &JSONSchemaValidator{}}),
		Frames:   &staticFrames{frame: frame},
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

func TestServiceRejectsInvalidIntentActionInputBeforeDispatch(t *testing.T) {
	frame := testFrame()
	action := &frame.Regions[0].Cards[0].Actions[0]
	action.Handler = ""
	action.Intent = "open"
	action.InputSchema = json.RawMessage(`{
		"type": "object",
		"required": ["change_id"],
		"properties": {"change_id": {"type": "string"}},
		"additionalProperties": false
	}`)
	called := false
	service := Service{
		Registry: NewRegistry(Dependencies{Schemas: &JSONSchemaValidator{}}),
		Frames:   &staticFrames{frame: frame},
		Intents: intentDispatcherFunc(func(context.Context, Transport, ActionEnvelope, Action) (OutcomeEnvelope, error) {
			called = true
			return OutcomeEnvelope{}, nil
		}),
	}

	_, err := service.DispatchAction(context.Background(), TransportTUI, ActionEnvelope{
		Action: "test.open", Input: json.RawMessage(`{"unknown":true}`),
		SessionID: "session-1", FrameRevision: 7,
	})
	if err == nil || !strings.Contains(err.Error(), `validate action "test.open" input`) {
		t.Fatalf("DispatchAction() error = %v", err)
	}
	if called {
		t.Fatal("intent dispatcher was called with schema-invalid input")
	}
}

func TestServiceAttachesCurrentFrameToCallsAndEvents(t *testing.T) {
	registry := NewRegistry(Dependencies{Events: &immediateEventRuntime{}})
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
