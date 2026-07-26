package studio

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/host"
	rsserver "kitsoki/internal/runstatus/server"
)

type ApplicationHandleArgs struct {
	Handle string `json:"handle"`
	Page   string `json:"page,omitempty"`
}

type ApplicationDiscoverArgs struct {
	Handle    string `json:"handle"`
	Page      string `json:"page,omitempty"`
	Transport string `json:"transport,omitempty"`
}

type ApplicationInspectArgs struct {
	Handle string `json:"handle"`
	Page   string `json:"page,omitempty"`
	Ref    string `json:"ref"`
	Limit  int    `json:"relationship_limit,omitempty"`
}

type ApplicationCallArgs struct {
	Handle         string         `json:"handle"`
	Page           string         `json:"page,omitempty"`
	Handler        string         `json:"handler"`
	Input          map[string]any `json:"input,omitempty"`
	RoutingMode    string         `json:"routing_mode,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

type ApplicationActionArgs struct {
	Handle         string         `json:"handle"`
	Page           string         `json:"page,omitempty"`
	Action         string         `json:"action"`
	Input          map[string]any `json:"input,omitempty"`
	FrameRevision  uint64         `json:"frame_revision"`
	RoutingMode    string         `json:"routing_mode,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

type ApplicationEventArgs struct {
	Handle string         `json:"handle"`
	Page   string         `json:"page,omitempty"`
	Event  string         `json:"event"`
	Input  map[string]any `json:"input,omitempty"`
}

type ApplicationFeedbackArgs struct {
	Handle         string `json:"handle"`
	Page           string `json:"page,omitempty"`
	Ref            string `json:"ref"`
	Instruction    string `json:"instruction"`
	Kind           string `json:"kind,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func (srv *Server) registerApplicationTools() {
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "application.frame",
		Description: "Read the canonical application-frame/v1 for a driving session. {handle, page?}. Read-only and presentation-free.",
	}, srv.handleApplicationFrame)
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "application.discover",
		Description: "Discover exported story application handlers from the shared registry. {handle, transport?:mcp|jsonrpc|cli|web|vscode|tui}.",
	}, srv.handleApplicationDiscover)
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "application.inspect",
		Description: "Inspect one stable semantic application ref with provenance, runtime state, and bounded relationships. {handle, ref, page?, relationship_limit?}.",
	}, srv.handleApplicationInspect)
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name: "application.feedback",
		Description: "Build a reviewed, privacy-safe feedback attachment for one canonical semantic ref. " +
			"The returned kitsoki.feedback.report.v1 bundle is accepted by the existing /api/feedback/local intake.",
	}, srv.handleApplicationFeedback)
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "application.call",
		Description: "Call one exported application handler through the shared registry. {handle, handler, input?, routing_mode?, idempotency_key?}.",
	}, srv.handleApplicationCall)
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "application.action",
		Description: "Dispatch a transport-neutral application action with stale-frame enforcement. {handle, action, frame_revision, input?, page?}.",
	}, srv.handleApplicationAction)
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "application.event",
		Description: "Dispatch a declared application event through the shared handler registry. {handle, event, input?, page?}.",
	}, srv.handleApplicationEvent)
}

func (srv *Server) handleApplicationFeedback(ctx context.Context, _ *mcpsdk.CallToolRequest, args ApplicationFeedbackArgs) (*mcpsdk.CallToolResult, any, error) {
	if args.Ref == "" || args.Instruction == "" {
		return buildToolError(ErrBadRequest, "application.feedback: ref and instruction are required"), nil, nil
	}
	service, sh, failure := srv.applicationService(args.Handle, args.Page)
	if failure != nil {
		return failure, nil, nil
	}
	frame, err := service.Frames.CurrentFrame(ctx, string(sh.SID))
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	var policy *app.ApplicationFeedbackPolicy
	if sh.Runtime != nil {
		if def := sh.Runtime.AppDef(); def != nil && def.Application != nil {
			policy = def.Application.Feedback
		}
	}
	report, err := applicationfeedback.BuildReport(frame, policy, applicationfeedback.ReportRequest{
		Ref: args.Ref, Kind: args.Kind, Instruction: args.Instruction,
		IdempotencyKey: args.IdempotencyKey,
	})
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	return nil, report, nil
}

func (srv *Server) applicationService(handle, page string) (appplatform.Service, *SessionHandle, *mcpsdk.CallToolResult) {
	sh, err := srv.sess.ResolveSession(handle)
	if err != nil {
		return appplatform.Service{}, nil, buildToolError(ErrBadRequest, fmt.Sprintf("application: %v", err))
	}
	if sh.Runtime == nil || sh.Driver == nil {
		return appplatform.Service{}, nil, buildToolError(ErrBadRequest, "application: handle has no driving runtime")
	}
	baseEntry := rsserver.Entry{
		Source: sh.Runtime,
		Driver: sh.Driver,
	}
	created := map[string]rsserver.Entry{}
	createdHandles := map[string]*SessionHandle{}
	service, err := rsserver.NewSessionApplicationService(baseEntry, page, rsserver.ApplicationRuntime{
		ResolveEntry: func(sessionID string) (rsserver.Entry, error) {
			if sessionID == string(sh.SID) || sessionID == sh.Key {
				return baseEntry, nil
			}
			if entry, ok := created[sessionID]; ok {
				return entry, nil
			}
			resolved, resolveErr := srv.sess.ResolveSession(sessionID)
			if resolveErr != nil || resolved.Runtime == nil || resolved.Driver == nil {
				return rsserver.Entry{}, fmt.Errorf("application: resolve created session %q: %v", sessionID, resolveErr)
			}
			return rsserver.Entry{Source: resolved.Runtime, Driver: resolved.Driver}, nil
		},
		CreateSession: func(ctx context.Context, _ *app.AppDef) (string, error) {
			tracePath, traceErr := resolveTracePath("", sh.StoryPath, "")
			if traceErr != nil {
				return "", traceErr
			}
			createdHandle, createErr := srv.sess.OpenDrivingSession(ctx, OpenDrivingSessionParams{
				Mode: sh.Mode, RecordingPath: sh.RecordingPath, StoryPath: sh.StoryPath,
				TracePath: tracePath, ImportResolver: srv.importResolver,
			})
			if createErr != nil {
				return "", createErr
			}
			created[createdHandle.Key] = rsserver.Entry{Source: createdHandle.Runtime, Driver: createdHandle.Driver}
			createdHandles[createdHandle.Key] = createdHandle
			return createdHandle.Key, nil
		},
		Interrupt: func(sessionID string) {
			target := sh
			if createdHandle, ok := createdHandles[sessionID]; ok {
				target = createdHandle
			} else if sessionID != "" && sessionID != sh.Key && sessionID != string(sh.SID) {
				if resolved, resolveErr := srv.sess.ResolveSession(sessionID); resolveErr == nil {
					target = resolved
				}
			}
			if target != nil && target.Runtime != nil {
				target.Runtime.interruptActiveTurn()
			}
		},
		ResolveHostRegistry: func(sessionID string) *host.Registry {
			if createdHandle, ok := createdHandles[sessionID]; ok && createdHandle.Runtime != nil {
				return createdHandle.Runtime.hostRegistry
			}
			return sh.Runtime.hostRegistry
		},
		EventScheduler: sh.Runtime.scheduler,
		JournalPath:    sh.TracePath + ".application.jsonl",
	})
	if err != nil {
		return appplatform.Service{}, nil, buildToolError(ErrBadRequest, err.Error())
	}
	return service, sh, nil
}

func (srv *Server) handleApplicationFrame(ctx context.Context, _ *mcpsdk.CallToolRequest, args ApplicationHandleArgs) (*mcpsdk.CallToolResult, any, error) {
	service, sh, failure := srv.applicationService(args.Handle, args.Page)
	if failure != nil {
		return failure, nil, nil
	}
	frame, err := service.Frames.CurrentFrame(ctx, string(sh.SID))
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	return nil, frame, nil
}

func (srv *Server) handleApplicationDiscover(ctx context.Context, _ *mcpsdk.CallToolRequest, args ApplicationDiscoverArgs) (*mcpsdk.CallToolResult, any, error) {
	service, _, failure := srv.applicationService(args.Handle, args.Page)
	if failure != nil {
		return failure, nil, nil
	}
	transport := appplatform.Transport(args.Transport)
	if transport == "" {
		transport = appplatform.TransportMCP
	}
	handlers, err := service.Discover(ctx, transport)
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	return nil, handlers, nil
}

func (srv *Server) handleApplicationInspect(ctx context.Context, _ *mcpsdk.CallToolRequest, args ApplicationInspectArgs) (*mcpsdk.CallToolResult, any, error) {
	if args.Ref == "" {
		return buildToolError(ErrBadRequest, "application.inspect: ref is required"), nil, nil
	}
	service, sh, failure := srv.applicationService(args.Handle, args.Page)
	if failure != nil {
		return failure, nil, nil
	}
	inspection, ok, err := service.Inspect(ctx, string(sh.SID), args.Ref, args.Limit)
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	if !ok {
		return buildToolError(ErrBadRequest, "application.inspect: semantic ref not found"), nil, nil
	}
	return nil, inspection, nil
}

func (srv *Server) handleApplicationCall(ctx context.Context, _ *mcpsdk.CallToolRequest, args ApplicationCallArgs) (*mcpsdk.CallToolResult, any, error) {
	if args.Handler == "" {
		return buildToolError(ErrBadRequest, "application.call: handler is required"), nil, nil
	}
	service, sh, failure := srv.applicationService(args.Handle, args.Page)
	if failure != nil {
		return failure, nil, nil
	}
	input, err := json.Marshal(args.Input)
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	outcome, err := service.Call(ctx, appplatform.TransportMCP, appplatform.CallRequest{
		Handler: args.Handler, Input: input, SessionID: string(sh.SID),
		Actor:          "mcp:" + args.Handle,
		RoutingMode:    appplatform.RoutingMode(args.RoutingMode),
		IdempotencyKey: args.IdempotencyKey,
	})
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	return nil, outcome, nil
}

func (srv *Server) handleApplicationAction(ctx context.Context, _ *mcpsdk.CallToolRequest, args ApplicationActionArgs) (*mcpsdk.CallToolResult, any, error) {
	if args.Action == "" {
		return buildToolError(ErrBadRequest, "application.action: action is required"), nil, nil
	}
	service, sh, failure := srv.applicationService(args.Handle, args.Page)
	if failure != nil {
		return failure, nil, nil
	}
	input, err := json.Marshal(args.Input)
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	outcome, err := service.DispatchAction(ctx, appplatform.TransportMCP, appplatform.ActionEnvelope{
		Action: args.Action, Input: input, SessionID: string(sh.SID),
		Actor:         "mcp:" + args.Handle,
		FrameRevision: args.FrameRevision, RoutingMode: appplatform.RoutingMode(args.RoutingMode),
		IdempotencyKey: args.IdempotencyKey,
	})
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	return nil, outcome, nil
}

func (srv *Server) handleApplicationEvent(ctx context.Context, _ *mcpsdk.CallToolRequest, args ApplicationEventArgs) (*mcpsdk.CallToolResult, any, error) {
	if args.Event == "" {
		return buildToolError(ErrBadRequest, "application.event: event is required"), nil, nil
	}
	service, sh, failure := srv.applicationService(args.Handle, args.Page)
	if failure != nil {
		return failure, nil, nil
	}
	input, err := json.Marshal(args.Input)
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	outcome, err := service.DispatchEvent(ctx, appplatform.EventEnvelope{
		Event: args.Event, Input: input, SessionID: string(sh.SID),
	})
	if err != nil {
		return buildToolError(ErrBadRequest, err.Error()), nil, nil
	}
	return nil, outcome, nil
}
