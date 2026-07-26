package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/effect"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/orchestrator"
)

// ApplicationRuntime injects the live-session capabilities that are not owned
// by a single Entry. The zero value keeps the fixed-entry test/read posture.
type ApplicationRuntime struct {
	ResolveEntry          func(string) (Entry, error)
	CreateSession         func(context.Context, *app.AppDef) (string, error)
	Interrupt             func(string)
	EventScheduler        jobs.Scheduler
	ResolveEventScheduler func(string) jobs.Scheduler
	ResolveHostRegistry   func(string) *host.Registry
	JournalPath           string
	Dependencies          appplatform.Dependencies
	RunFunctional         func(context.Context, Entry, *app.AppDef, string, *app.ApplicationHandler, appplatform.Invocation) (appplatform.HandlerResult, error)
}

func (s *Server) applicationRuntime() ApplicationRuntime {
	return ApplicationRuntime{
		ResolveEntry: func(sessionID string) (Entry, error) {
			entry, ok := s.provider.Get(sessionID)
			if !ok {
				return Entry{}, fmt.Errorf("application: created session %q is unavailable", sessionID)
			}
			return entry, nil
		},
		CreateSession: func(ctx context.Context, def *app.AppDef) (string, error) {
			if def == nil || def.BaseDir == "" {
				return "", fmt.Errorf("application: story path is unavailable for session:create")
			}
			return s.provider.NewSession(ctx, filepath.Join(def.BaseDir, "app.yaml"))
		},
		Interrupt: func(sessionID string) {
			s.cancelActiveTurn(sessionID)
		},
		EventScheduler: s.applicationEventSched,
		ResolveEventScheduler: func(sessionID string) jobs.Scheduler {
			if provider, ok := s.provider.(ApplicationEventSchedulerProvider); ok {
				if scheduler, found := provider.ApplicationEventScheduler(sessionID); found {
					return scheduler
				}
			}
			return s.applicationEventSched
		},
		ResolveHostRegistry: func(sessionID string) *host.Registry {
			if provider, ok := s.provider.(ApplicationHostRegistryProvider); ok {
				registry, _ := provider.ApplicationHostRegistry(sessionID)
				return registry
			}
			return nil
		},
		Dependencies: s.applicationDeps,
	}
}

type sessionApplicationFrameProvider struct {
	entry   Entry
	request ApplicationFrameRequest
	runtime ApplicationRuntime
}

func (p sessionApplicationFrameProvider) CurrentFrameForPage(
	ctx context.Context,
	sessionID string,
	page string,
) (appplatform.Frame, error) {
	p.request.Page = page
	p.request.RoutePath = ""
	p.request.SynchronizeState = false
	p.request.RequirePage = true
	return p.CurrentFrame(ctx, sessionID)
}

func (p sessionApplicationFrameProvider) CurrentFrame(ctx context.Context, sessionID string) (appplatform.Frame, error) {
	entry, err := resolveApplicationEntry(p.entry, p.runtime, sessionID)
	if err != nil {
		return appplatform.Frame{}, err
	}
	def := entry.Source.AppDef()
	contract, _ := app.EffectiveApplication(def)
	page := p.request.Page
	routeParams := cloneApplicationParams(p.request.RouteParams)
	if p.request.RoutePath != "" {
		match, routeErr := app.ResolveApplicationRoute(contract, p.request.RoutePath)
		if routeErr != nil {
			// The application host root is the neutral entry URL. Contracts
			// without a canonical "/" route resolve their entry page and let
			// the shared adapter replace it with that page's canonical path.
			if p.request.RoutePath != "/" {
				return appplatform.Frame{}, routeErr
			}
		} else {
			if page != "" && page != match.Page {
				return appplatform.Frame{}, fmt.Errorf(
					"application: route %q resolves page %q, not requested page %q",
					p.request.RoutePath, match.Page, page,
				)
			}
			page = match.Page
			routeParams = match.Params
		}
	}
	if page != "" && p.request.SynchronizeState && p.request.RoutePath == "" {
		if binding, ok := app.ApplicationPageBindingFor(contract, page); ok && binding.Route != "" {
			if routeErr := validateApplicationPageRouteParams(binding.Route, routeParams); routeErr != nil {
				return appplatform.Frame{}, fmt.Errorf(
					"application: navigate page %q: %w", page, routeErr,
				)
			}
		}
	}
	snapshot, err := entry.Source.Snapshot()
	if err != nil {
		return appplatform.Frame{}, err
	}
	currentState := snapshot.Session.CurrentState
	pageBinding, pageBound := app.ApplicationPageBindingFor(contract, page)
	routeWorld := applicationRouteWorld(contract, page, routeParams)
	nextState := ""
	if pageBound && pageBinding.State != "" && pageBinding.State != currentState {
		nextState = pageBinding.State
	}
	if len(routeWorld) > 0 && nextState == "" {
		nextState = currentState
	}
	if p.request.SynchronizeState && (nextState != "" || len(routeWorld) > 0) {
		navigator, ok := entry.Driver.(ApplicationPageNavigator)
		if !ok {
			return appplatform.Frame{}, fmt.Errorf(
				"application: page %q requires story navigation but the session cannot navigate applications",
				page,
			)
		}
		if _, err := navigator.NavigateApplication(ctx, nextState, routeWorld); err != nil {
			return appplatform.Frame{}, fmt.Errorf(
				"application: navigate page %q: %w", page, err,
			)
		}
		snapshot, err = entry.Source.Snapshot()
		if err != nil {
			return appplatform.Frame{}, err
		}
	}
	workflow := appplatform.Workflow{State: snapshot.Session.CurrentState}
	if entry.Driver != nil {
		view, viewErr := entry.Driver.View(ctx)
		if viewErr != nil {
			return appplatform.Frame{}, viewErr
		}
		workflow.State = string(view.NewState)
		if workflow.State == "" {
			workflow.State = snapshot.Session.CurrentState
		}
		workflow.AllowedIntents = append([]string(nil), view.AllowedIntents...)
	}
	if p.request.RequirePage {
		binding := app.ApplicationPageBindings(contract)[page]
		if binding.State != "" && binding.State != workflow.State {
			return appplatform.Frame{}, fmt.Errorf(
				"application: target page %q binds state %q but action reached state %q",
				page, binding.State, workflow.State,
			)
		}
	} else if !p.request.SynchronizeState {
		statePages := app.ApplicationPagesForState(contract, workflow.State)
		bindings := app.ApplicationPageBindings(contract)
		requestMatchesState := page != "" && bindings[page].State == workflow.State
		switch {
		case requestMatchesState:
			// A requested alias remains canonical while its bound state is current.
		case len(statePages) == 1:
			page = statePages[0]
		case len(statePages) > 1 && page == "":
			if entry := contract.Shell.Entry; bindings[entry].State == workflow.State {
				page = entry
			} else {
				return appplatform.Frame{}, fmt.Errorf(
					"application: state %q is bound to pages %s; an explicit target page is required",
					workflow.State, strings.Join(statePages, ", "),
				)
			}
		case len(statePages) > 1:
			return appplatform.Frame{}, fmt.Errorf(
				"application: state %q is bound to pages %s; action target_page is required",
				workflow.State, strings.Join(statePages, ", "),
			)
		}
		if binding := bindings[page]; binding.Route != "" {
			route, routeErr := app.ParseApplicationRouteTemplate(binding.Route)
			if routeErr != nil {
				return appplatform.Frame{}, routeErr
			}
			routeParams = retainApplicationRouteParams(route.Params, routeParams)
		}
	}
	var world map[string]any
	if worldReader, ok := entry.Driver.(WorldReader); ok {
		world, err = worldReader.CurrentWorld(ctx)
		if err != nil {
			return appplatform.Frame{}, fmt.Errorf("application: read frame data world: %w", err)
		}
	}
	return appplatform.CompileFrameWithContext(
		def, sessionID, uint64(snapshot.Session.Turn), page, workflow,
		appplatform.CompileContext{World: world, RouteParams: routeParams},
	)
}

func validateApplicationPageRouteParams(template string, params map[string]any) error {
	route, err := app.ParseApplicationRouteTemplate(template)
	if err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(route.Params))
	for _, name := range route.Params {
		allowed[name] = struct{}{}
	}
	for name := range params {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("route parameter %q is not declared by %q", name, template)
		}
	}
	_, err = app.FormatApplicationRoute(template, params)
	return err
}

func applicationRouteWorld(
	contract *app.ApplicationContract,
	page string,
	params map[string]any,
) map[string]any {
	if contract == nil || contract.Pages[page] == nil || len(params) == 0 {
		return nil
	}
	out := map[string]any{}
	for param, worldKey := range contract.Pages[page].RouteBindings {
		if value, ok := params[param]; ok {
			out[worldKey] = value
		}
	}
	return out
}

// ApplicationFrameRequest carries presentation-neutral page addressing into
// the shared frame compiler. RoutePath is resolved by the application contract;
// transports that already resolved a route may supply RouteParams with Page.
type ApplicationFrameRequest struct {
	Page             string
	RoutePath        string
	RouteParams      map[string]any
	SynchronizeState bool
	RequirePage      bool
}

func cloneApplicationParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]any, len(params))
	for name, value := range params {
		out[name] = value
	}
	return out
}

func retainApplicationRouteParams(names []string, params map[string]any) map[string]any {
	if len(names) == 0 || len(params) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, name := range names {
		if value, ok := params[name]; ok {
			out[name] = value
		}
	}
	return out
}

type sessionApplicationIntentDispatcher struct {
	frames   appplatform.FrameProvider
	registry *appplatform.Registry
}

func (d sessionApplicationIntentDispatcher) DispatchIntent(
	ctx context.Context,
	transport appplatform.Transport,
	envelope appplatform.ActionEnvelope,
	action appplatform.Action,
) (appplatform.OutcomeEnvelope, error) {
	if d.registry == nil {
		return appplatform.OutcomeEnvelope{}, fmt.Errorf("application: intent action registry is unavailable")
	}
	outcome, err := d.registry.Invoke(ctx, appplatform.Invocation{
		HandlerID: action.ID, Input: envelope.Input, SessionID: envelope.SessionID,
		Actor: envelope.Actor, Transport: transport, RoutingMode: action.RoutingMode,
		IdempotencyKey: applicationIntentActionKey(envelope.FrameRevision),
		FrameRevision:  envelope.FrameRevision,
	})
	if err != nil {
		return outcome, err
	}
	if outcome.Frame == nil && d.frames != nil {
		frame, frameErr := appplatform.CurrentFrameForPage(
			ctx, d.frames, envelope.SessionID, action.TargetPage,
		)
		if frameErr != nil {
			return appplatform.OutcomeEnvelope{}, frameErr
		}
		outcome.Frame = &frame
		outcome.Frame.Workflow.BudgetState = outcome.Receipt.Budget.Code
		if outcome.Receipt.Budget.Code != "not_applicable" {
			outcome.Frame.Workflow.Degradation = outcome.Receipt.Budget.Reason
		}
	}
	return outcome, nil
}

func applicationIntentActionKey(frameRevision uint64) string {
	// Handler ID and session are separate ReplayKey dimensions. The offered
	// frame revision therefore identifies one action opportunity without
	// trusting a transport-generated key or actor label.
	return fmt.Sprintf("application-action/v1:frame:%d", frameRevision)
}

// NewSessionApplicationService adapts one runstatus entry to the shared
// application registry. Studio MCP and JSON-RPC both use this constructor.
func NewSessionApplicationService(entry Entry, page string, runtimes ...ApplicationRuntime) (appplatform.Service, error) {
	return NewSessionApplicationServiceWithRequest(
		entry, ApplicationFrameRequest{Page: page}, runtimes...,
	)
}

// NewSessionApplicationServiceWithRequest preserves route and state-navigation
// context for every frame refresh performed by the shared service.
func NewSessionApplicationServiceWithRequest(
	entry Entry,
	request ApplicationFrameRequest,
	runtimes ...ApplicationRuntime,
) (appplatform.Service, error) {
	if entry.Source == nil || entry.Source.AppDef() == nil {
		return appplatform.Service{}, fmt.Errorf("application: session has no story definition")
	}
	var runtime ApplicationRuntime
	if len(runtimes) > 0 {
		runtime = runtimes[0]
	}
	def := entry.Source.AppDef()
	frames := sessionApplicationFrameProvider{entry: entry, request: request, runtime: runtime}
	deps, err := applicationDependencies(entry, def, runtime)
	if err != nil {
		return appplatform.Service{}, err
	}
	registry := appplatform.NewRegistry(deps)
	intentRegistry := appplatform.NewRegistry(deps)

	contract, _ := app.EffectiveApplication(def)
	if contract != nil {
		for _, id := range sortedApplicationKeys(contract.Actions) {
			action := contract.Actions[id]
			if action == nil || action.Intent == "" {
				continue
			}
			var inputSchema json.RawMessage
			if strings.TrimSpace(action.InputSchema) != "" {
				var err error
				inputSchema, err = loadApplicationSchema(def, action.InputSchema)
				if err != nil {
					return appplatform.Service{}, fmt.Errorf("application: action %q input schema: %w", id, err)
				}
			}
			actionID := id
			actionDecl := action
			if err := intentRegistry.RegisterHandler(appplatform.HandlerDefinition{
				ID: actionID, Name: actionDecl.Name, Description: actionDecl.Description,
				SemanticRef: actionDecl.SemanticRef, InputSchema: inputSchema,
				Session: appplatform.SessionRequired, Effect: appplatform.EffectWrite,
				RoutingMode: applicationRoutingMode(actionDecl.RoutingMode), Outcomes: []string{"ok"},
				Expose: []appplatform.Transport{
					appplatform.TransportJSONRPC, appplatform.TransportMCP, appplatform.TransportCLI,
					appplatform.TransportWeb, appplatform.TransportVSCode, appplatform.TransportTUI,
				},
				Idempotency:      appplatform.IdempotencyRequired,
				IdempotencyScope: "session",
			}, appplatform.HandlerFunc(func(ctx context.Context, invocation appplatform.Invocation) (appplatform.HandlerResult, error) {
				targetEntry, err := resolveApplicationEntry(entry, runtime, invocation.SessionID)
				if err != nil {
					return appplatform.HandlerResult{}, err
				}
				return runApplicationIntent(
					ctx, targetEntry, actionID, actionDecl.Intent,
					actionDecl.State, actionDecl.RoomInterface,
					[]string{"ok"}, actionDecl.RoutingMode, invocation,
				)
			})); err != nil {
				return appplatform.Service{}, err
			}
		}
	}

	if def.Exports != nil {
		for _, id := range sortedApplicationKeys(def.Exports.Handlers) {
			declared := def.Exports.Handlers[id]
			if declared == nil {
				continue
			}
			if declared.Dispatch != nil && declared.Session == string(appplatform.SessionNone) {
				return appplatform.Service{}, fmt.Errorf("application: stateful handler %q cannot use session:none", id)
			}
			runtimeDef, err := applicationHandlerDefinition(def, id, declared)
			if err != nil {
				return appplatform.Service{}, err
			}
			handlerID := id
			handlerDecl := declared
			if handlerDecl.Starlark != nil && applicationStarlarkNeedsHost(handlerDecl.Starlark.Capabilities) &&
				runtime.RunFunctional == nil && runtime.ResolveHostRegistry == nil {
				return appplatform.Service{}, fmt.Errorf(
					"application: handler %q declares host capabilities but no session host registry is configured",
					handlerID,
				)
			}
			if err := registry.RegisterHandler(runtimeDef, appplatform.HandlerFunc(func(ctx context.Context, invocation appplatform.Invocation) (appplatform.HandlerResult, error) {
				ctx = host.WithActor(ctx, invocation.Actor)
				targetEntry, err := resolveApplicationEntry(entry, runtime, invocation.SessionID)
				if err != nil {
					return appplatform.HandlerResult{}, err
				}
				if handlerDecl.Starlark != nil {
					executor := runtime.RunFunctional
					if executor == nil {
						return runApplicationStarlark(
							ctx, targetEntry, def, handlerID, handlerDecl, invocation,
							resolveApplicationHostRegistry(runtime, invocation.SessionID, entry),
						)
					}
					return executor(ctx, targetEntry, def, handlerID, handlerDecl, invocation)
				}
				if handlerDecl.Dispatch == nil || handlerDecl.Dispatch.Intent == "" {
					return appplatform.HandlerResult{}, fmt.Errorf("application: handler %q has no dispatch", handlerID)
				}
				return runApplicationIntent(
					ctx, targetEntry, handlerID, handlerDecl.Dispatch.Intent,
					handlerDecl.Dispatch.State, handlerDecl.Dispatch.RoomInterface,
					handlerDecl.Outcomes, handlerDecl.RoutingMode, invocation,
				)
			})); err != nil {
				return appplatform.Service{}, err
			}
		}
	}

	for _, id := range sortedApplicationKeys(def.Events) {
		event := def.Events[id]
		if event == nil || event.Dispatch == nil {
			continue
		}
		target := event.Dispatch.Handler
		if event.Dispatch.Intent != "" {
			if event.Session == string(appplatform.SessionNone) {
				return appplatform.Service{}, fmt.Errorf("application: stateful event %q cannot use session:none", id)
			}
			target = "event-intent:" + id
			inputSchema, err := loadApplicationSchema(def, event.InputSchema)
			if err != nil {
				return appplatform.Service{}, fmt.Errorf("application: event %q input schema: %w", id, err)
			}
			eventID := id
			eventDecl := event
			if err := registry.RegisterHandler(appplatform.HandlerDefinition{
				ID: target, Name: id, Description: "Dispatch the " + id + " event intent.",
				SemanticRef: event.Source, InputSchema: inputSchema,
				Session: appplatform.SessionPolicy(event.Session), Effect: appplatform.EffectWrite,
				RoutingMode: applicationRoutingMode(event.RoutingMode), Outcomes: []string{"ok"},
				Idempotency: appplatform.IdempotencyOptional,
			}, appplatform.HandlerFunc(func(ctx context.Context, invocation appplatform.Invocation) (appplatform.HandlerResult, error) {
				targetEntry, err := resolveApplicationEntry(entry, runtime, invocation.SessionID)
				if err != nil {
					return appplatform.HandlerResult{}, err
				}
				return runApplicationIntent(
					ctx, targetEntry, eventID, eventDecl.Dispatch.Intent,
					eventDecl.Dispatch.State, eventDecl.Dispatch.RoomInterface,
					[]string{"ok"}, eventDecl.RoutingMode, invocation,
				)
			})); err != nil {
				return appplatform.Service{}, err
			}
		}
		inputSchema, err := loadApplicationSchema(def, event.InputSchema)
		if err != nil {
			return appplatform.Service{}, fmt.Errorf("application: event %q input schema: %w", id, err)
		}
		if err := registry.RegisterEvent(appplatform.EventDefinition{
			ID: id, Source: event.Source, InputSchema: inputSchema,
			Session: appplatform.SessionPolicy(event.Session), Mode: appplatform.EventMode(event.Mode),
			RoutingMode: applicationRoutingMode(event.RoutingMode), Handler: target,
		}); err != nil {
			return appplatform.Service{}, err
		}
	}
	if err := registry.Validate(); err != nil {
		return appplatform.Service{}, err
	}
	if err := intentRegistry.Validate(); err != nil {
		return appplatform.Service{}, err
	}
	return appplatform.Service{
		Registry: registry,
		Frames:   frames,
		Intents: sessionApplicationIntentDispatcher{
			frames: frames, registry: intentRegistry,
		},
	}, nil
}

func applicationDependencies(entry Entry, def *app.AppDef, runtime ApplicationRuntime) (appplatform.Dependencies, error) {
	deps := runtime.Dependencies
	if deps.Schemas == nil {
		deps.Schemas = &appplatform.JSONSchemaValidator{Root: def.BaseDir}
	}
	if deps.Auth == nil {
		deps.Auth = applicationAuthorizer{}
	}
	if deps.Effects == nil {
		deps.Effects = applicationEffectPolicy{}
	}
	if deps.Budget == nil {
		deps.Budget = applicationBudgetGovernor{}
	}
	if runtime.CreateSession != nil && deps.Sessions == nil {
		deps.Sessions = applicationSessionManager{runtime: runtime, def: def}
	}
	journalPath := runtime.JournalPath
	if journalPath == "" {
		if source, ok := entry.Source.(AnnotationSource); ok {
			journalPath = strings.TrimSuffix(source.AnnotationPath(), ".annotations.jsonl") + ".application.jsonl"
		}
	}
	var journal *applicationJournal
	if journalPath != "" && (deps.Receipts == nil || deps.Replay == nil || deps.Events == nil) {
		var err error
		journal, err = sharedApplicationJournal(journalPath)
		if err != nil {
			return appplatform.Dependencies{}, err
		}
		if deps.Receipts == nil {
			deps.Receipts = journal
		}
		if deps.Replay == nil {
			deps.Replay = journal
		}
	}
	if deps.Replay == nil {
		deps.Replay = appplatform.NewMemoryReplayStore()
	}
	if deps.Events == nil && runtime.EventScheduler != nil {
		deps.Events = applicationEventRuntime{
			scheduler:        runtime.EventScheduler,
			resolveScheduler: runtime.ResolveEventScheduler,
			interrupt:        runtime.Interrupt,
			journal:          journal,
		}
	}
	return deps, nil
}

type applicationAuthorizer struct{}

func (applicationAuthorizer) Authorize(_ context.Context, def appplatform.HandlerDefinition, invocation appplatform.Invocation) error {
	if (def.Effect == appplatform.EffectWrite || def.Effect == appplatform.EffectExternal) && invocation.Actor == "" {
		return fmt.Errorf("authenticated actor is required for %s effect", def.Effect)
	}
	return nil
}

type applicationEffectPolicy struct{}

func (applicationEffectPolicy) AuthorizeEffect(_ context.Context, def appplatform.HandlerDefinition, invocation appplatform.Invocation) error {
	if def.Effect == appplatform.EffectExternal && def.Idempotency == appplatform.IdempotencyRequired && invocation.IdempotencyKey == "" {
		return fmt.Errorf("external effect requires an idempotency key")
	}
	return nil
}

type applicationBudgetGovernor struct{}

func (applicationBudgetGovernor) Decide(_ context.Context, def appplatform.HandlerDefinition, _ appplatform.Invocation) (appplatform.BudgetDecision, error) {
	return appplatform.BudgetDecision{
		Allowed: true,
		Code:    "not_applicable",
		Reason:  fmt.Sprintf("application handler %q does not allocate a model budget", def.ID),
	}, nil
}

type applicationEventRuntime struct {
	scheduler        jobs.Scheduler
	resolveScheduler func(string) jobs.Scheduler
	interrupt        func(string)
	journal          *applicationJournal
}

func (r applicationEventRuntime) Interrupt(_ context.Context, _ appplatform.EventDefinition, invocation appplatform.Invocation) error {
	if r.interrupt == nil {
		return fmt.Errorf("active-turn interrupt is unavailable")
	}
	r.interrupt(invocation.SessionID)
	return nil
}

func (r applicationEventRuntime) Enqueue(
	ctx context.Context,
	event appplatform.EventDefinition,
	handler appplatform.HandlerDefinition,
	invocation appplatform.Invocation,
	run appplatform.EventRun,
) (appplatform.OutcomeEnvelope, error) {
	scheduler := r.scheduler
	if r.resolveScheduler != nil {
		scheduler = r.resolveScheduler(invocation.SessionID)
	}
	if scheduler == nil {
		return appplatform.OutcomeEnvelope{}, fmt.Errorf("background scheduler is unavailable")
	}
	ready := make(chan string, 1)
	jobID, err := scheduler.Submit(ctx, jobs.JobSpec{
		SessionID: app.SessionID(invocation.SessionID),
		Kind:      "application.event." + event.ID,
		Payload:   map[string]any{"event": event.ID, "handler": handler.ID},
		Handler: func(runCtx context.Context, _ map[string]any) (host.Result, error) {
			id := <-ready
			outcome, runErr := run(runCtx)
			child := appplatform.ChildRun{ID: id, Status: "failed"}
			join := appplatform.JoinState{Status: "failed", Completed: []string{id}}
			if runErr == nil {
				child.Status = "done"
				child.Outcome = outcome.Outcome
				join.Status = "done"
			}
			if journalErr := r.journal.recordEventState(child, join, &outcome, runErr); journalErr != nil && runErr == nil {
				runErr = journalErr
			}
			if runErr != nil {
				return host.Result{}, runErr
			}
			var data map[string]any
			raw, marshalErr := json.Marshal(outcome)
			if marshalErr != nil {
				return host.Result{}, marshalErr
			}
			if err := json.Unmarshal(raw, &data); err != nil {
				return host.Result{}, err
			}
			return host.Result{Data: data}, nil
		},
	})
	if err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	ready <- jobID

	child := appplatform.ChildRun{ID: jobID, Status: "running"}
	join := appplatform.JoinState{Status: "pending", Pending: []string{jobID}}
	output, _ := json.Marshal(map[string]any{"job_id": jobID, "status": "running"})
	inputDigest, _ := appplatform.DigestJSON(invocation.Input)
	outputDigest, _ := appplatform.DigestJSON(output)
	receipt, err := appplatform.FinalizeReceipt(appplatform.Receipt{
		HandlerID: handler.ID, SemanticRef: handler.SemanticRef,
		SessionID: invocation.SessionID, Actor: invocation.Actor,
		Effect: handler.Effect,
		Routing: appplatform.RoutingReceipt{
			Requested: invocation.RoutingMode, Resolved: invocation.RoutingMode,
		},
		Budget: appplatform.BudgetDecision{
			Allowed: true, Code: "not_applicable",
			Reason: "background event scheduling does not allocate a model budget",
		},
		InputDigest: inputDigest, OutputDigest: outputDigest,
		Transport: appplatform.TransportEvent, EventID: event.ID,
		EventMode: event.Mode, Outcome: "accepted",
	})
	if err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	outcome := appplatform.OutcomeEnvelope{
		Schema: appplatform.OutcomeSchema, Handler: handler.ID, Outcome: "accepted",
		Output: output, Children: []appplatform.ChildRun{child}, Join: &join, Receipt: receipt,
	}
	if err := r.journal.recordEventState(child, join, &outcome, nil); err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	return outcome, nil
}

type applicationSessionManager struct {
	runtime ApplicationRuntime
	def     *app.AppDef
}

func (m applicationSessionManager) CreateSession(ctx context.Context, _ appplatform.HandlerDefinition, _ appplatform.Invocation) (string, error) {
	return m.runtime.CreateSession(ctx, m.def)
}

func applicationHandlerDefinition(def *app.AppDef, id string, handler *app.ApplicationHandler) (appplatform.HandlerDefinition, error) {
	expose := make([]appplatform.Transport, 0, len(handler.Expose))
	for _, transport := range handler.Expose {
		expose = append(expose, appplatform.Transport(transport))
	}
	inputSchema, err := loadApplicationSchema(def, handler.InputSchema)
	if err != nil {
		return appplatform.HandlerDefinition{}, fmt.Errorf("application: handler %q input schema: %w", id, err)
	}
	outputSchema, err := loadApplicationSchema(def, handler.OutputSchema)
	if err != nil {
		return appplatform.HandlerDefinition{}, fmt.Errorf("application: handler %q output schema: %w", id, err)
	}
	idempotency := appplatform.IdempotencyPolicy("")
	if handler.Effect == effect.Write || handler.Effect == effect.External {
		if handler.Idempotency == nil || strings.TrimSpace(handler.Idempotency.Key) == "" ||
			strings.TrimSpace(handler.Idempotency.Scope) == "" {
			return appplatform.HandlerDefinition{}, fmt.Errorf(
				"application: %s handler %q requires an explicit idempotency input key and scope",
				handler.Effect, id,
			)
		}
	}
	if handler.Idempotency != nil {
		idempotency = appplatform.IdempotencyRequired
	}
	runtimeDef := appplatform.HandlerDefinition{
		ID: id, Name: handler.Name, Description: handler.Description,
		SemanticRef: handler.SemanticRef, InputSchema: inputSchema, OutputSchema: outputSchema,
		Session: appplatform.SessionPolicy(handler.Session), Effect: appplatform.EffectClass(handler.Effect),
		RoutingMode: applicationRoutingMode(handler.RoutingMode),
		Outcomes:    append([]string(nil), handler.Outcomes...), Expose: expose,
		Idempotency: idempotency, Retryable: handler.Retry != nil && handler.Retry.MaxAttempts > 1,
		CompensationHandler: handler.Compensation, NoCompensationReason: handler.CompensationImpossible,
	}
	if handler.Idempotency != nil {
		runtimeDef.IdempotencyKeyField = handler.Idempotency.Key
		runtimeDef.IdempotencyScope = handler.Idempotency.Scope
	}
	return runtimeDef, nil
}

func runApplicationIntent(
	ctx context.Context,
	entry Entry,
	member, intent string,
	targetState, roomInterface string,
	outcomes []string,
	routingMode string,
	invocation appplatform.Invocation,
) (appplatform.HandlerResult, error) {
	if entry.Driver == nil {
		return appplatform.HandlerResult{}, fmt.Errorf("application: session is read-only")
	}
	if roomInterface != "" && targetState == "" {
		return appplatform.HandlerResult{}, fmt.Errorf("application: %q room interface %q has no resolved implementor", member, roomInterface)
	}
	if targetState != "" {
		snapshot, err := entry.Source.Snapshot()
		if err != nil {
			return appplatform.HandlerResult{}, fmt.Errorf("application: inspect target state for %q: %w", member, err)
		}
		current := snapshot.Session.CurrentState
		if current != targetState {
			return appplatform.HandlerResult{}, fmt.Errorf(
				"application: %q targets state %q but session is in %q; refusing same-name intent dispatch",
				member, targetState, current,
			)
		}
	}
	slots, err := applicationInputSlots(invocation.Input)
	if err != nil {
		return appplatform.HandlerResult{}, err
	}
	slots = applicationActorSlots(slots, invocation.Actor)
	ctx = host.WithActor(ctx, invocation.Actor)
	out, err := entry.Driver.SubmitDirect(ctx, intent, slots)
	if err != nil {
		return appplatform.HandlerResult{}, err
	}
	if err := applicationTurnFailure(member, out); err != nil {
		return appplatform.HandlerResult{}, err
	}
	output, err := applicationTurnOutput(out)
	if err != nil {
		return appplatform.HandlerResult{}, err
	}
	outcome, err := applicationExactOutcome(outcomes, output)
	if err != nil {
		return appplatform.HandlerResult{}, fmt.Errorf("application: handler %q: %w", member, err)
	}
	return appplatform.HandlerResult{
		Outcome: outcome, Output: output, RoutingResolved: applicationRoutingMode(routingMode),
		SelectedImplementor: targetState,
	}, nil
}

func runApplicationStarlark(
	ctx context.Context,
	entry Entry,
	def *app.AppDef,
	handlerID string,
	handlerDecl *app.ApplicationHandler,
	invocation appplatform.Invocation,
	hostRegistry *host.Registry,
) (appplatform.HandlerResult, error) {
	if handlerDecl.Starlark == nil {
		return appplatform.HandlerResult{}, fmt.Errorf("application: handler %q has no Starlark declaration", handlerID)
	}
	ctx = host.WithActor(ctx, invocation.Actor)
	script, err := resolveApplicationPath(def, handlerDecl.Starlark.Script)
	if err != nil {
		return appplatform.HandlerResult{}, err
	}
	inputs, err := applicationInputSlots(invocation.Input)
	if err != nil {
		return appplatform.HandlerResult{}, err
	}
	if invocation.SessionID != "" {
		if worldReader, ok := entry.Driver.(WorldReader); ok {
			world, readErr := worldReader.CurrentWorld(ctx)
			if readErr != nil {
				return appplatform.HandlerResult{}, fmt.Errorf("application: read world for %q: %w", handlerID, readErr)
			}
			ctx = host.WithWorldSnapshot(ctx, world)
		}
	}
	if hostRegistry == nil {
		if applicationStarlarkNeedsHost(handlerDecl.Starlark.Capabilities) {
			return appplatform.HandlerResult{}, fmt.Errorf("application: handler %q declares host capabilities but the session host registry is unavailable", handlerID)
		}
		hostRegistry = host.NewRegistry()
	}
	if _, ok := hostRegistry.Get("host.starlark.run"); !ok {
		hostRegistry.Register("host.starlark.run", host.NewStarlarkRunHandler(hostRegistry))
	}
	starlarkRun, _ := hostRegistry.Get("host.starlark.run")
	result, err := starlarkRun(ctx, map[string]any{
		"script": script, "inputs": inputs, "capabilities": handlerDecl.Starlark.Capabilities,
	})
	if err != nil {
		return appplatform.HandlerResult{}, err
	}
	if result.Error != "" {
		return appplatform.HandlerResult{}, fmt.Errorf("%s", result.Error)
	}
	output, err := json.Marshal(result.Data)
	if err != nil {
		return appplatform.HandlerResult{}, fmt.Errorf("application: encode Starlark output: %w", err)
	}
	outcome, err := applicationExactOutcome(handlerDecl.Outcomes, output)
	if err != nil {
		return appplatform.HandlerResult{}, fmt.Errorf("application: handler %q: %w", handlerID, err)
	}
	return appplatform.HandlerResult{
		Outcome: outcome, Output: output,
		RoutingResolved: applicationRoutingMode(handlerDecl.RoutingMode),
	}, nil
}

func resolveApplicationHostRegistry(runtime ApplicationRuntime, sessionID string, fallback Entry) *host.Registry {
	if runtime.ResolveHostRegistry == nil {
		return nil
	}
	if sessionID == "" && fallback.Source != nil {
		if snapshot, err := fallback.Source.Snapshot(); err == nil {
			sessionID = snapshot.Session.SessionID
		}
	}
	return runtime.ResolveHostRegistry(sessionID)
}

func applicationStarlarkNeedsHost(capabilities map[string]any) bool {
	_, ok := capabilities["host"]
	return ok
}

func applicationExactOutcome(outcomes []string, output json.RawMessage) (string, error) {
	var object map[string]any
	if json.Unmarshal(output, &object) == nil {
		if explicit, ok := object["outcome"].(string); ok && explicit != "" {
			for _, declared := range outcomes {
				if explicit == declared {
					return explicit, nil
				}
			}
			return "", fmt.Errorf("returned undeclared outcome %q", explicit)
		}
	}
	for _, outcome := range outcomes {
		if outcome == "ok" {
			return "ok", nil
		}
	}
	if len(outcomes) == 1 {
		return outcomes[0], nil
	}
	return "", fmt.Errorf("result did not identify one of the declared outcomes %v", outcomes)
}

func loadApplicationSchema(def *app.AppDef, reference string) (json.RawMessage, error) {
	path, err := resolveApplicationPath(def, reference)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", reference, err)
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("parse %q: %w", reference, err)
	}
	if object, ok := schema.(map[string]any); ok {
		schemaID, err := applicationSchemaID(def.BaseDir, path, object["$id"])
		if err != nil {
			return nil, fmt.Errorf("schema %q $id: %w", reference, err)
		}
		object["$id"] = schemaID
	}
	normalized, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("normalize %q: %w", reference, err)
	}
	return normalized, nil
}

func applicationSchemaID(root, path string, declared any) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	base := (&url.URL{Scheme: "file", Path: absolute}).String()
	if declared == nil || declared == "" {
		return base, nil
	}
	id, ok := declared.(string)
	if !ok {
		return "", fmt.Errorf("must be a string")
	}
	reference, err := url.Parse(id)
	if err != nil {
		return "", err
	}
	if reference.IsAbs() {
		if reference.Scheme != "file" {
			return "", fmt.Errorf("network and non-file identifiers are denied")
		}
		return applicationRootedFileURL(root, reference)
	}
	resolved, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return applicationRootedFileURL(root, resolved.ResolveReference(reference))
}

func applicationRootedFileURL(root string, reference *url.URL) (string, error) {
	if reference.Host != "" && reference.Host != "localhost" {
		return "", fmt.Errorf("remote file hosts are denied")
	}
	path, err := url.PathUnescape(reference.Path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file identifier escapes story root")
	}
	return reference.String(), nil
}

func resolveApplicationPath(def *app.AppDef, reference string) (string, error) {
	if def == nil || def.BaseDir == "" {
		return "", fmt.Errorf("story base directory is unavailable")
	}
	if strings.TrimSpace(reference) == "" {
		return "", fmt.Errorf("path reference is required")
	}
	path := reference
	if !filepath.IsAbs(path) {
		path = filepath.Join(def.BaseDir, path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(def.BaseDir, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the story root", reference)
	}
	return path, nil
}

func resolveApplicationEntry(fallback Entry, runtime ApplicationRuntime, sessionID string) (Entry, error) {
	if runtime.ResolveEntry == nil || sessionID == "" {
		return fallback, nil
	}
	return runtime.ResolveEntry(sessionID)
}

func applicationRoutingMode(mode string) appplatform.RoutingMode {
	if mode == "" {
		return appplatform.RoutingExact
	}
	return appplatform.RoutingMode(mode)
}

func applicationInputSlots(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var slots map[string]any
	if err := json.Unmarshal(raw, &slots); err != nil {
		return nil, fmt.Errorf("application: input must be a JSON object: %w", err)
	}
	if slots == nil {
		slots = map[string]any{}
	}
	return slots, nil
}

func applicationActorSlots(slots map[string]any, actor string) map[string]any {
	if actor == "" {
		return slots
	}
	if existing, ok := slots[authorSlot]; !ok || existing == nil || existing == "" {
		slots[authorSlot] = actor
	}
	return slots
}

func applicationTurnFailure(member string, out *orchestrator.TurnOutcome) error {
	if out == nil {
		return fmt.Errorf("application: %q returned no turn outcome", member)
	}
	if out.HarnessError != "" {
		return fmt.Errorf("application: %q harness failure: %s", member, out.HarnessError)
	}
	switch out.Mode {
	case orchestrator.ModeTransitioned, orchestrator.ModeCompleted:
		return nil
	default:
		detail := out.ErrorMessage
		if detail == "" {
			detail = out.Mode.String()
		}
		if out.ErrorCode != "" {
			return fmt.Errorf("application: %q %s (%s): %s", member, out.Mode.String(), out.ErrorCode, detail)
		}
		return fmt.Errorf("application: %q %s: %s", member, out.Mode.String(), detail)
	}
}

func applicationTurnOutput(out *orchestrator.TurnOutcome) (json.RawMessage, error) {
	if out == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(map[string]any{
		"mode": out.Mode, "state": out.NewState, "view": out.View,
		"allowed_intents": out.AllowedIntents, "error_code": out.ErrorCode,
		"error_message": out.ErrorMessage, "turn": out.TurnNumber,
	})
}

func sortedApplicationKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
