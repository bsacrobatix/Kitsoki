package server

import (
	"context"
	"encoding/json"
	"fmt"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/orchestrator"
)

type sessionApplicationFrameProvider struct {
	entry Entry
	page  string
}

func (p sessionApplicationFrameProvider) CurrentFrame(ctx context.Context, sessionID string) (appplatform.Frame, error) {
	snapshot, err := p.entry.Source.Snapshot()
	if err != nil {
		return appplatform.Frame{}, err
	}
	workflow := appplatform.Workflow{State: snapshot.Session.CurrentState}
	if p.entry.Driver != nil {
		view, viewErr := p.entry.Driver.View(ctx)
		if viewErr != nil {
			return appplatform.Frame{}, viewErr
		}
		workflow.State = string(view.NewState)
		if workflow.State == "" {
			workflow.State = snapshot.Session.CurrentState
		}
		workflow.AllowedIntents = append([]string(nil), view.AllowedIntents...)
	}
	return appplatform.CompileFrame(
		p.entry.Source.AppDef(),
		sessionID,
		uint64(snapshot.Session.Turn),
		p.page,
		workflow,
	)
}

type sessionApplicationIntentDispatcher struct {
	entry  Entry
	frames appplatform.FrameProvider
}

func (d sessionApplicationIntentDispatcher) DispatchIntent(
	ctx context.Context,
	transport appplatform.Transport,
	envelope appplatform.ActionEnvelope,
	action appplatform.Action,
) (appplatform.OutcomeEnvelope, error) {
	if d.entry.Driver == nil {
		return appplatform.OutcomeEnvelope{}, fmt.Errorf("application: session is read-only")
	}
	slots, err := applicationInputSlots(envelope.Input)
	if err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	slots = applicationActorSlots(slots, envelope.Actor)
	out, err := d.entry.Driver.SubmitDirect(ctx, action.Intent, slots)
	if err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	if err := applicationTurnFailure(action.ID, out); err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	output, err := applicationTurnOutput(out)
	if err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	receipt, err := applicationReceipt(
		action.ID, action.Semantic.Ref, envelope.SessionID, envelope.Actor,
		transport, action.RoutingMode, envelope.IdempotencyKey,
		envelope.FrameRevision, envelope.Input, output, "ok",
	)
	if err != nil {
		return appplatform.OutcomeEnvelope{}, err
	}
	result := appplatform.OutcomeEnvelope{
		Schema: appplatform.OutcomeSchema, Handler: action.ID, Outcome: "ok",
		Output: output, Receipt: receipt,
	}
	if d.frames != nil {
		frame, frameErr := d.frames.CurrentFrame(ctx, envelope.SessionID)
		if frameErr != nil {
			return appplatform.OutcomeEnvelope{}, frameErr
		}
		result.Frame = &frame
	}
	return result, nil
}

// NewSessionApplicationService adapts one runstatus entry to the shared
// application registry. Studio MCP and JSON-RPC both use this constructor.
func NewSessionApplicationService(entry Entry, page string) (appplatform.Service, error) {
	if entry.Source == nil || entry.Source.AppDef() == nil {
		return appplatform.Service{}, fmt.Errorf("application: session has no story definition")
	}
	def := entry.Source.AppDef()
	frames := sessionApplicationFrameProvider{entry: entry, page: page}
	registry := appplatform.NewRegistry(appplatform.Dependencies{})
	if def.Exports != nil {
		for id, declared := range def.Exports.Handlers {
			if declared == nil {
				continue
			}
			handlerID := id
			handlerDecl := declared
			runtimeDef := applicationHandlerDefinition(id, declared)
			err := registry.RegisterHandler(runtimeDef, appplatform.HandlerFunc(func(ctx context.Context, invocation appplatform.Invocation) (appplatform.HandlerResult, error) {
				if entry.Driver == nil {
					return appplatform.HandlerResult{}, fmt.Errorf("application: session is read-only")
				}
				if handlerDecl.Dispatch == nil || handlerDecl.Dispatch.Intent == "" {
					return appplatform.HandlerResult{}, fmt.Errorf(
						"application: handler %q is functional Starlark and is not session-dispatchable", handlerID,
					)
				}
				slots, err := applicationInputSlots(invocation.Input)
				if err != nil {
					return appplatform.HandlerResult{}, err
				}
				slots = applicationActorSlots(slots, invocation.Actor)
				out, err := entry.Driver.SubmitDirect(ctx, handlerDecl.Dispatch.Intent, slots)
				if err != nil {
					return appplatform.HandlerResult{}, err
				}
				if err := applicationTurnFailure(handlerID, out); err != nil {
					return appplatform.HandlerResult{}, err
				}
				output, err := applicationTurnOutput(out)
				if err != nil {
					return appplatform.HandlerResult{}, err
				}
				return appplatform.HandlerResult{
					Outcome:         applicationSuccessOutcome(handlerDecl.Outcomes),
					Output:          output,
					RoutingResolved: applicationRoutingMode(handlerDecl.RoutingMode),
				}, nil
			}))
			if err != nil {
				return appplatform.Service{}, err
			}
		}
	}
	for id, event := range def.Events {
		if event == nil || event.Dispatch == nil || event.Dispatch.Handler == "" {
			continue
		}
		if err := registry.RegisterEvent(appplatform.EventDefinition{
			ID: id, Source: event.Source, Session: appplatform.SessionPolicy(event.Session),
			Mode: appplatform.EventMode(event.Mode), Handler: event.Dispatch.Handler,
		}); err != nil {
			return appplatform.Service{}, err
		}
	}
	return appplatform.Service{
		Registry: registry,
		Frames:   frames,
		Intents:  sessionApplicationIntentDispatcher{entry: entry, frames: frames},
	}, nil
}

func applicationHandlerDefinition(id string, handler *app.ApplicationHandler) appplatform.HandlerDefinition {
	routing := applicationRoutingMode(handler.RoutingMode)
	expose := make([]appplatform.Transport, 0, len(handler.Expose))
	for _, transport := range handler.Expose {
		expose = append(expose, appplatform.Transport(transport))
	}
	idempotency := appplatform.IdempotencyPolicy("")
	if handler.Idempotency != nil {
		idempotency = appplatform.IdempotencyRequired
	}
	retryable := handler.Retry != nil && handler.Retry.MaxAttempts > 1
	return appplatform.HandlerDefinition{
		ID: id, Name: handler.Name, Description: handler.Description,
		SemanticRef: handler.SemanticRef, Session: appplatform.SessionPolicy(handler.Session),
		Effect: appplatform.EffectClass(handler.Effect), RoutingMode: routing,
		Outcomes: append([]string(nil), handler.Outcomes...), Expose: expose,
		Idempotency: idempotency, Retryable: retryable,
		CompensationHandler:  handler.Compensation,
		NoCompensationReason: handler.CompensationImpossible,
	}
}

func applicationRoutingMode(mode string) appplatform.RoutingMode {
	if mode == "" {
		return appplatform.RoutingExact
	}
	return appplatform.RoutingMode(mode)
}

func applicationSuccessOutcome(outcomes []string) string {
	for _, outcome := range outcomes {
		if outcome == "ok" {
			return outcome
		}
	}
	if len(outcomes) > 0 {
		return outcomes[0]
	}
	return "ok"
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
		"mode":            out.Mode,
		"state":           out.NewState,
		"view":            out.View,
		"allowed_intents": out.AllowedIntents,
		"error_code":      out.ErrorCode,
		"error_message":   out.ErrorMessage,
		"turn":            out.TurnNumber,
	})
}

func applicationReceipt(
	handlerID, semanticRef, sessionID, actor string,
	transport appplatform.Transport,
	routing appplatform.RoutingMode,
	idempotencyKey string,
	frameRevision uint64,
	input, output json.RawMessage,
	outcome string,
) (appplatform.Receipt, error) {
	inputDigest, err := appplatform.DigestJSON(input)
	if err != nil {
		return appplatform.Receipt{}, err
	}
	outputDigest, err := appplatform.DigestJSON(output)
	if err != nil {
		return appplatform.Receipt{}, err
	}
	return appplatform.FinalizeReceipt(appplatform.Receipt{
		HandlerID: handlerID, SemanticRef: semanticRef, SessionID: sessionID,
		Actor: actor, Routing: appplatform.RoutingReceipt{Requested: routing, Resolved: routing},
		Budget: appplatform.BudgetDecision{Allowed: true}, IdempotencyKey: idempotencyKey,
		InputDigest: inputDigest, OutputDigest: outputDigest, Transport: transport,
		FrameRevision: frameRevision, Outcome: outcome,
	})
}
