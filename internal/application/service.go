package application

import (
	"context"
	"encoding/json"
	"fmt"
)

type FrameProvider interface {
	CurrentFrame(context.Context, string) (Frame, error)
}

type PageFrameProvider interface {
	CurrentFrameForPage(context.Context, string, string) (Frame, error)
}

// IntentDispatcher is the existing story state-machine boundary injected into
// the surface service. Intent business behavior remains owned by the runtime.
type IntentDispatcher interface {
	DispatchIntent(context.Context, Transport, ActionEnvelope, Action) (OutcomeEnvelope, error)
}

type Service struct {
	Registry *Registry
	Frames   FrameProvider
	Intents  IntentDispatcher
}

type CallRequest struct {
	Handler        string          `json:"handler"`
	Input          json.RawMessage `json:"input,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	Actor          string          `json:"actor,omitempty"`
	RoutingMode    RoutingMode     `json:"routing_mode,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type EventEnvelope struct {
	Event     string          `json:"event"`
	Input     json.RawMessage `json:"input,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Actor     string          `json:"actor,omitempty"`
}

func (s Service) Discover(_ context.Context, transport Transport) ([]HandlerDefinition, error) {
	if s.Registry == nil {
		return nil, fmt.Errorf("application: registry is required")
	}
	if !validTransport(transport) || transport == TransportEvent {
		return nil, fmt.Errorf("application: invalid discovery transport %q", transport)
	}
	return s.Registry.Discover(transport), nil
}

func (s Service) Call(ctx context.Context, transport Transport, request CallRequest) (OutcomeEnvelope, error) {
	if s.Registry == nil {
		return OutcomeEnvelope{}, fmt.Errorf("application: registry is required")
	}
	outcome, err := s.Registry.Invoke(ctx, Invocation{
		HandlerID: request.Handler, Input: request.Input, SessionID: request.SessionID,
		Actor: request.Actor, Transport: transport, RoutingMode: request.RoutingMode,
		IdempotencyKey: request.IdempotencyKey,
	})
	return s.attachCurrentFrame(ctx, outcome.Receipt.SessionID, outcome, err)
}

func (s Service) DispatchAction(ctx context.Context, transport Transport, envelope ActionEnvelope) (OutcomeEnvelope, error) {
	if s.Frames == nil {
		return OutcomeEnvelope{}, fmt.Errorf("application: frame provider is required")
	}
	frame, err := s.Frames.CurrentFrame(ctx, envelope.SessionID)
	if err != nil {
		return OutcomeEnvelope{}, fmt.Errorf("application: load current frame: %w", err)
	}
	action, err := ValidateActionEnvelope(frame, envelope)
	if err != nil {
		return OutcomeEnvelope{}, err
	}
	if len(action.InputSchema) > 0 {
		input, normalizeErr := NormalizeJSON(envelope.Input)
		if normalizeErr != nil {
			return OutcomeEnvelope{}, normalizeErr
		}
		if s.Registry == nil || s.Registry.deps.Schemas == nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: schema validator is required for action %q", action.ID)
		}
		if validateErr := validateSchema(
			ctx,
			s.Registry.deps.Schemas,
			action.SchemaReference,
			action.InputSchema,
			input,
		); validateErr != nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: validate action %q input: %w", action.ID, validateErr)
		}
		envelope.Input = input
	}
	if action.Handler == "" {
		if s.Intents == nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: intent dispatcher is required for action %q", action.ID)
		}
		return s.Intents.DispatchIntent(ctx, transport, envelope, action)
	}
	if s.Registry == nil {
		return OutcomeEnvelope{}, fmt.Errorf("application: registry is required for handler action %q", action.ID)
	}
	routingMode := envelope.RoutingMode
	if routingMode == "" {
		routingMode = action.RoutingMode
	}
	outcome, err := s.Registry.Invoke(ctx, Invocation{
		HandlerID: action.Handler, Input: envelope.Input, SessionID: envelope.SessionID,
		Actor: envelope.Actor, Transport: transport, RoutingMode: routingMode,
		IdempotencyKey: envelope.IdempotencyKey, FrameRevision: envelope.FrameRevision,
	})
	if outcome.Frame == nil {
		refreshed, frameErr := CurrentFrameForPage(
			ctx, s.Frames, outcome.Receipt.SessionID, action.TargetPage,
		)
		if frameErr != nil && err == nil {
			return OutcomeEnvelope{}, fmt.Errorf("application: refresh frame: %w", frameErr)
		}
		if frameErr == nil {
			outcome.Frame = &refreshed
		}
	}
	annotateBudgetFrame(&outcome)
	return outcome, err
}

func CurrentFrameForPage(
	ctx context.Context,
	frames FrameProvider,
	sessionID string,
	page string,
) (Frame, error) {
	if page != "" {
		if provider, ok := frames.(PageFrameProvider); ok {
			return provider.CurrentFrameForPage(ctx, sessionID, page)
		}
	}
	return frames.CurrentFrame(ctx, sessionID)
}

func (s Service) DispatchEvent(ctx context.Context, envelope EventEnvelope) (OutcomeEnvelope, error) {
	if s.Registry == nil {
		return OutcomeEnvelope{}, fmt.Errorf("application: registry is required")
	}
	outcome, err := s.Registry.DispatchEvent(ctx, envelope.Event, envelope.Input, envelope.SessionID, envelope.Actor)
	return s.attachCurrentFrame(ctx, outcome.Receipt.SessionID, outcome, err)
}

func (s Service) Inspect(ctx context.Context, sessionID, ref string, relationshipLimit int) (SemanticInspection, bool, error) {
	if s.Frames == nil {
		return SemanticInspection{}, false, fmt.Errorf("application: frame provider is required")
	}
	frame, err := s.Frames.CurrentFrame(ctx, sessionID)
	if err != nil {
		return SemanticInspection{}, false, fmt.Errorf("application: load current frame: %w", err)
	}
	return frame.Inspect(ref, relationshipLimit)
}

func (s Service) attachCurrentFrame(
	ctx context.Context,
	sessionID string,
	outcome OutcomeEnvelope,
	invocationErr error,
) (OutcomeEnvelope, error) {
	if outcome.Frame != nil || s.Frames == nil || sessionID == "" {
		annotateBudgetFrame(&outcome)
		return outcome, invocationErr
	}
	frame, err := s.Frames.CurrentFrame(ctx, sessionID)
	if err != nil {
		if invocationErr != nil {
			return outcome, invocationErr
		}
		return OutcomeEnvelope{}, fmt.Errorf("application: refresh frame: %w", err)
	}
	outcome.Frame = &frame
	annotateBudgetFrame(&outcome)
	return outcome, invocationErr
}

func annotateBudgetFrame(outcome *OutcomeEnvelope) {
	if outcome == nil || outcome.Frame == nil {
		return
	}
	outcome.Frame.Workflow.BudgetState = outcome.Receipt.Budget.Code
	if outcome.Receipt.Budget.Reason != "" && outcome.Receipt.Budget.Code != "not_applicable" {
		outcome.Frame.Workflow.Degradation = outcome.Receipt.Budget.Reason
	}
}
