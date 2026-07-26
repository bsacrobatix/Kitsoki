package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationjob"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/runstatus/server"
)

func (r *SessionRegistry) wireApplicationJob(rt *sessionRuntime, callerApplicationID string) {
	if r.applicationJobs == nil || rt == nil || rt.HostRegistry == nil {
		return
	}
	if _, configured := r.cfg.StoryApplicationJobs[callerApplicationID]; !configured {
		return
	}
	rt.HostRegistry.Replace(
		"host.application_job",
		applicationjob.NewHandler(r.applicationJobs, callerApplicationID),
	)
}

type applicationJobRegistryBackend struct {
	registry *SessionRegistry
}

func (b applicationJobRegistryBackend) Dispatch(
	ctx context.Context,
	request applicationjob.DispatchRequest,
) (applicationjob.DispatchResult, error) {
	if b.registry == nil {
		return applicationjob.DispatchResult{}, fmt.Errorf("application job registry is unavailable")
	}
	input, err := applicationjob.ValidateEventInput(
		request.Input,
		request.Template.Bounds.MaxInputBytes,
	)
	if err != nil {
		return applicationjob.DispatchResult{}, err
	}
	routeID, err := b.registry.NewRegisteredApplicationSession(
		ctx,
		request.Template.ApplicationID,
	)
	if err != nil {
		return applicationjob.DispatchResult{}, fmt.Errorf("open configured application")
	}
	entry, ok := b.registry.Get(routeID)
	if !ok || entry.Source == nil || entry.Source.AppDef() == nil {
		return applicationjob.DispatchResult{}, fmt.Errorf("configured application session is unavailable")
	}
	fallbackScheduler, ok := b.registry.ApplicationEventScheduler(routeID)
	if !ok {
		return applicationjob.DispatchResult{}, fmt.Errorf("configured application scheduler is unavailable")
	}
	runtime := server.ApplicationRuntime{
		ResolveEntry: func(sessionID string) (server.Entry, error) {
			resolved, found := b.registry.Get(sessionID)
			if !found {
				return server.Entry{}, fmt.Errorf("application job session is unavailable")
			}
			return resolved, nil
		},
		CreateSession: func(createCtx context.Context, def *app.AppDef) (string, error) {
			if def == nil {
				return "", fmt.Errorf("application job definition is unavailable")
			}
			return b.registry.NewRegisteredApplicationSession(createCtx, def.App.ID)
		},
		EventScheduler: fallbackScheduler,
		ResolveEventScheduler: func(sessionID string) jobs.Scheduler {
			if scheduler, found := b.registry.ApplicationEventScheduler(sessionID); found {
				return scheduler
			}
			return fallbackScheduler
		},
		ResolveHostRegistry: func(sessionID string) *host.Registry {
			registry, _ := b.registry.ApplicationHostRegistry(sessionID)
			return registry
		},
	}
	service, err := server.NewSessionApplicationService(entry, "", runtime)
	if err != nil {
		return applicationjob.DispatchResult{}, fmt.Errorf("construct configured application service")
	}
	event, ok := service.Registry.Event(request.Template.Event)
	if !ok || event.ID != request.Template.Event ||
		event.Mode != appplatform.EventBackground {
		return applicationjob.DispatchResult{}, fmt.Errorf("configured application event is not an exact background event")
	}
	sessionID := ""
	if event.Session == appplatform.SessionRequired {
		sessionID = routeID
	}
	outcome, err := service.DispatchEvent(ctx, appplatform.EventEnvelope{
		Event: request.Template.Event, Input: input, SessionID: sessionID,
		Actor: "application-job:" + request.CallerApplicationID,
	})
	if err != nil {
		return applicationjob.DispatchResult{}, fmt.Errorf("dispatch configured application event")
	}
	if outcome.Outcome != "accepted" || outcome.Receipt.EventID != request.Template.Event ||
		outcome.Receipt.EventMode != appplatform.EventBackground ||
		len(outcome.Children) != 1 || outcome.Children[0].ID == "" {
		return applicationjob.DispatchResult{}, fmt.Errorf("configured application event returned an invalid background acknowledgement")
	}
	schedulerRouteID := outcome.Receipt.SessionID
	if schedulerRouteID == "" {
		schedulerRouteID = routeID
	}
	return applicationjob.DispatchResult{
		RouteID: schedulerRouteID, SessionID: outcome.Receipt.SessionID,
		ChildID: outcome.Children[0].ID,
	}, nil
}

func (b applicationJobRegistryBackend) Status(
	_ context.Context,
	record applicationjob.Record,
) (applicationjob.ChildSnapshot, bool, error) {
	scheduler, ok := b.registry.ApplicationEventScheduler(record.TargetRouteID)
	if !ok {
		return applicationjob.ChildSnapshot{}, false, nil
	}
	child, ok := scheduler.Get(record.ChildJobID)
	if !ok {
		return applicationjob.ChildSnapshot{}, false, nil
	}
	snapshot := applicationjob.ChildSnapshot{Status: string(child.Status)}
	if child.Result == nil || child.Result.Data == nil {
		return snapshot, true, nil
	}
	output, err := applicationJobOutput(child.Result.Data)
	if err != nil {
		return applicationjob.ChildSnapshot{}, false, err
	}
	snapshot.Output = output
	return snapshot, true, nil
}

func (b applicationJobRegistryBackend) Cancel(
	ctx context.Context,
	record applicationjob.Record,
) error {
	scheduler, ok := b.registry.ApplicationEventScheduler(record.TargetRouteID)
	if !ok {
		return nil
	}
	err := scheduler.Cancel(ctx, record.ChildJobID)
	if errors.Is(err, jobs.ErrJobNotFound) {
		return nil
	}
	return err
}

func applicationJobOutput(data map[string]any) (map[string]any, error) {
	value, ok := data["output"]
	if !ok || value == nil {
		return map[string]any{}, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, nil
	case json.RawMessage:
		var output map[string]any
		if err := json.Unmarshal(typed, &output); err != nil {
			return nil, err
		}
		return output, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, err
		}
		var output map[string]any
		if err := json.Unmarshal(raw, &output); err != nil {
			return nil, err
		}
		return output, nil
	}
}

var _ applicationjob.Backend = applicationJobRegistryBackend{}
