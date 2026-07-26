package studio

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	appplatform "kitsoki/internal/application"
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

func (srv *Server) applicationService(handle, page string) (appplatform.Service, *SessionHandle, *mcpsdk.CallToolResult) {
	sh, err := srv.sess.ResolveSession(handle)
	if err != nil {
		return appplatform.Service{}, nil, buildToolError(ErrBadRequest, fmt.Sprintf("application: %v", err))
	}
	if sh.Runtime == nil || sh.Driver == nil {
		return appplatform.Service{}, nil, buildToolError(ErrBadRequest, "application: handle has no driving runtime")
	}
	service, err := rsserver.NewSessionApplicationService(rsserver.Entry{
		Source: sh.Runtime,
		Driver: sh.Driver,
	}, page)
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
