package server

import (
	"context"
	"encoding/json"
	"fmt"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/materialize"
)

type registeredMaterializeApplicationExecutor struct {
	service   appplatform.Service
	sessionID string
	actor     string
}

func (e registeredMaterializeApplicationExecutor) ExecuteApplicationPhase(
	ctx context.Context,
	request materialize.ApplicationPhaseRequest,
) (appplatform.OutcomeEnvelope, error) {
	input, err := json.Marshal(request.Input)
	if err != nil {
		return appplatform.OutcomeEnvelope{}, fmt.Errorf("encode bounded phase input: %w", err)
	}
	if request.Phase.Handler != "" {
		return e.service.Call(ctx, appplatform.TransportJSONRPC, appplatform.CallRequest{
			Handler:        request.Phase.Handler,
			Input:          input,
			SessionID:      e.sessionID,
			Actor:          e.actor,
			IdempotencyKey: request.IdempotencyKey,
		})
	}
	frame, err := e.service.Frames.CurrentFrame(ctx, e.sessionID)
	if err != nil {
		return appplatform.OutcomeEnvelope{}, fmt.Errorf("load application frame: %w", err)
	}
	if frame.ApplicationID != request.ApplicationID {
		return appplatform.OutcomeEnvelope{}, fmt.Errorf(
			"application frame id %q does not match binding %q",
			frame.ApplicationID,
			request.ApplicationID,
		)
	}
	if _, ok := frame.FindAction(request.Phase.Action); !ok {
		return appplatform.OutcomeEnvelope{}, fmt.Errorf(
			"application action %q is not offered by the current frame",
			request.Phase.Action,
		)
	}
	return e.service.DispatchAction(ctx, appplatform.TransportJSONRPC, appplatform.ActionEnvelope{
		Action:         request.Phase.Action,
		Input:          input,
		SessionID:      e.sessionID,
		FrameRevision:  frame.Revision,
		Actor:          e.actor,
		IdempotencyKey: request.IdempotencyKey,
	})
}

func (s *Server) materializeRegisteredApplication(
	ctx context.Context,
	prep *materialize.Prepared,
) (string, materialize.ApplicationPhaseExecutor, error) {
	provider, ok := s.provider.(RegisteredApplicationProvider)
	if !ok {
		return "", nil, fmt.Errorf("provider does not support registered application execution")
	}
	sessionID, err := provider.NewRegisteredApplicationSession(ctx, prep.Binding.ApplicationID)
	if err != nil {
		return "", nil, err
	}
	if sessionID == "" {
		return "", nil, fmt.Errorf("provider returned an empty registered application session")
	}
	entry, ok := s.provider.Get(sessionID)
	if !ok || entry.Source == nil || entry.Source.AppDef() == nil {
		return "", nil, fmt.Errorf("registered application session %q is unavailable", sessionID)
	}
	if err := materialize.ValidateApplicationBinding(entry.Source.AppDef(), prep.Binding); err != nil {
		return "", nil, err
	}
	if err := s.validateMaterializeApplicationReplay(entry.Source.AppDef(), prep.Binding); err != nil {
		return "", nil, err
	}
	runtime := s.applicationRuntime()
	if s.materializeReplay != nil {
		runtime.Dependencies.Replay = s.materializeReplay
	}
	if s.materializeReceipts != nil {
		runtime.Dependencies.Receipts = s.materializeReceipts
	}
	service, err := NewSessionApplicationService(entry, "", runtime)
	if err != nil {
		return "", nil, err
	}
	return sessionID, registeredMaterializeApplicationExecutor{
		service:   service,
		sessionID: sessionID,
		actor:     "graph.materialize:" + string(prep.Req.NodeID),
	}, nil
}

func (s *Server) validateMaterializeApplicationReplay(def *app.AppDef, binding *materialize.Binding) error {
	if materialize.ApplicationBindingRequiresDurableReplay(def, binding) && s.materializeReplay == nil {
		if s.materializeReplayErr != nil {
			return fmt.Errorf("durable application replay is unavailable: %w", s.materializeReplayErr)
		}
		return fmt.Errorf("effectful typed materialization requires durable application replay")
	}
	return nil
}
