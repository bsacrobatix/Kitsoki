package host_test

import (
	"context"
	"errors"
	"testing"

	"kitsoki/internal/host"
)

func TestInboxAdd_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	if _, ok := r.Get("host.inbox.add"); !ok {
		t.Fatal("host.inbox.add missing")
	}
}

func TestInboxAdd_Happy_WithAdapter(t *testing.T) {
	mem := host.NewMemInboxAdder(nil)
	ctx := host.WithInboxAdder(context.Background(), mem)

	res, err := host.InboxAddHandler(ctx, map[string]any{
		"title":  "Reproduction artifact: BUG-1",
		"body":   "## Reproduction\n\nstep 1\nstep 2",
		"kind":   "checkpoint",
		"thread": "issues/bugs/2026-05-14T10-tui-hangs.md",
		"state":  "reproducing_awaiting_reply",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["persisted"] != true {
		t.Fatalf("persisted: %v", res.Data["persisted"])
	}
	if res.Data["id"] != "mem-1" {
		t.Fatalf("id: %v", res.Data["id"])
	}
	items := mem.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Notification.Title != "Reproduction artifact: BUG-1" {
		t.Fatalf("title: %v", items[0].Notification.Title)
	}
	if items[0].Notification.Kind != "checkpoint" {
		t.Fatalf("kind: %v", items[0].Notification.Kind)
	}
	if items[0].Notification.State != "reproducing_awaiting_reply" {
		t.Fatalf("state: %v", items[0].Notification.State)
	}
}

func TestInboxAdd_AlwaysOn_NoAdapter(t *testing.T) {
	res, err := host.InboxAddHandler(context.Background(), map[string]any{
		"title": "no adapter",
		"body":  "still works",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["ok"] != true {
		t.Fatalf("ok: %v", res.Data["ok"])
	}
	if res.Data["persisted"] != false {
		t.Fatalf("persisted should be false when no adapter: %v", res.Data["persisted"])
	}
}

func TestInboxAdd_RequiresTitle(t *testing.T) {
	res, err := host.InboxAddHandler(context.Background(), map[string]any{
		"body": "x",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for empty title")
	}
}

func TestInboxAdd_RequiresBody(t *testing.T) {
	res, err := host.InboxAddHandler(context.Background(), map[string]any{
		"title": "x",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for empty body")
	}
}

type failingAdder struct{}

func (failingAdder) AddInbox(_ context.Context, _ host.InboxNotification) (string, error) {
	return "", errors.New("synthetic adapter failure")
}

func TestInboxAdd_AdapterFailureSurfaces(t *testing.T) {
	ctx := host.WithInboxAdder(context.Background(), failingAdder{})
	res, err := host.InboxAddHandler(ctx, map[string]any{
		"title": "x",
		"body":  "y",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected adapter failure to surface as Result.Error")
	}
}

func TestInboxAdd_DefaultsKindToInfo(t *testing.T) {
	mem := host.NewMemInboxAdder(nil)
	ctx := host.WithInboxAdder(context.Background(), mem)

	if _, err := host.InboxAddHandler(ctx, map[string]any{
		"title": "x",
		"body":  "y",
	}); err != nil {
		t.Fatalf("infra: %v", err)
	}
	items := mem.Items()
	if len(items) != 1 || items[0].Notification.Kind != "info" {
		t.Fatalf("default kind: %v", items)
	}
}
