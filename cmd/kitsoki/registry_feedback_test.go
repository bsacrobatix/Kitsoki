package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

type registryFeedbackBackend struct {
	request host.FeedbackListRequest
}

func (b *registryFeedbackBackend) ListReviewed(
	_ context.Context,
	request host.FeedbackListRequest,
) ([]host.ReviewedFeedbackReport, error) {
	b.request = request
	return []host.ReviewedFeedbackReport{{
		Ref: "feedback/ref", Kind: "bug", Title: "title", Summary: "summary",
		Producer: "intake", Reviewed: true, AppID: request.Scope.ApplicationID,
		Owner: request.Scope.Owner, Revision: request.Scope.Revision,
		ReceiptRef: "receipt/ref", ReviewedAt: time.Unix(1, 0).UTC(),
	}}, nil
}

func (*registryFeedbackBackend) Dispatch(
	context.Context,
	host.FeedbackDispatchRequest,
) (host.FeedbackDispatchReceipt, error) {
	return host.FeedbackDispatchReceipt{JobID: "job"}, nil
}

func TestRegisterAndWireFeedbackBackendUsesLoadedApplicationScope(t *testing.T) {
	registry := NewRegistry(webconfig.WebConfig{}, nil, runtimeBase{})
	backend := &registryFeedbackBackend{}
	if err := registry.RegisterFeedbackBackend("app-a", backend); err != nil {
		t.Fatalf("register backend: %v", err)
	}
	if err := registry.RegisterFeedbackBackend("app-a", backend); err == nil ||
		!strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate registration error = %v", err)
	}

	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireFeedback(
		&sessionRuntime{HostRegistry: hostRegistry},
		"app-a",
		"server-owner",
		"2.1.0",
	)
	result, err := hostRegistry.Invoke(
		host.WithActor(context.Background(), "actor"),
		"host.feedback.list_reviewed",
		map[string]any{"scope": "current", "limit": 1},
	)
	if err != nil {
		t.Fatalf("invoke list reviewed: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("result error = %q", result.Error)
	}
	wantScope := (host.FeedbackScope{
		ApplicationID: "app-a",
		Owner:         "server-owner",
		Revision:      "2.1.0",
	})
	if backend.request.Scope != wantScope {
		t.Fatalf("backend scope = %#v, want %#v", backend.request.Scope, wantScope)
	}
}

func TestWireFeedbackLeavesUnregisteredApplicationUnavailable(t *testing.T) {
	registry := &SessionRegistry{}
	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireFeedback(
		&sessionRuntime{HostRegistry: hostRegistry},
		"unregistered",
		"owner",
		"1",
	)
	result, err := hostRegistry.Invoke(
		host.WithActor(context.Background(), "actor"),
		"host.feedback.list_reviewed",
		map[string]any{"scope": "current", "limit": 1},
	)
	if err != nil {
		t.Fatalf("invoke sentinel: %v", err)
	}
	if !strings.Contains(result.Error, "unavailable") {
		t.Fatalf("sentinel error = %q", result.Error)
	}
	if err := registry.RegisterFeedbackBackend("app", nil); err == nil {
		t.Fatal("nil backend registration succeeded")
	}
}
