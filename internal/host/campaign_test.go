package host

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestCampaignHandlerFailsClosedOutsideDaemon(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	result, err := registry.Invoke(context.Background(), "host.campaign.watch", map[string]any{
		"poll_seconds": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "unavailable outside daemon mode") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestCampaignHandlerFixesApplicationScope(t *testing.T) {
	controller := &campaignControllerCapture{}
	handler := NewCampaignHandler(controller, "runner")
	result, err := handler(context.Background(), map[string]any{
		"op": "watch", "poll_seconds": 5, "app_id": "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if controller.appID != "runner" {
		t.Fatalf("controller app id = %q", controller.appID)
	}
	if result.Data["job_id"] != "watch-1" {
		t.Fatalf("result = %#v", result.Data)
	}
}

func TestCampaignRegistrationSchemaAndEffect(t *testing.T) {
	class, deterministic := ClassifyDispatchedCall("host.campaign", map[string]any{"op": "watch"})
	if class != effect.External || deterministic {
		t.Fatalf("watch classification = (%q, %v)", class, deterministic)
	}
	class, deterministic = ClassifyDispatchedCall("host.campaign.snapshot", nil)
	if class != effect.Read || !deterministic {
		t.Fatalf("snapshot classification = (%q, %v)", class, deterministic)
	}
	watch, ok := opschema.Builtins().Lookup("host.campaign", "watch")
	if !ok || watch.Input["poll_seconds"].Type != "int" ||
		watch.Output["job_id"].Type != "string" {
		t.Fatalf("watch opschema = %#v, %v", watch, ok)
	}
	snapshot, ok := opschema.Builtins().Lookup("host.campaign", "snapshot")
	if !ok || snapshot.Input["max_campaigns"].Type != "int" ||
		snapshot.Output["snapshot"].Type != "object" {
		t.Fatalf("snapshot opschema = %#v, %v", snapshot, ok)
	}
}

type campaignControllerCapture struct {
	appID string
}

func (c *campaignControllerCapture) Watch(_ context.Context, appID string, _ int) (map[string]any, error) {
	c.appID = appID
	return map[string]any{"job_id": "watch-1"}, nil
}

func (c *campaignControllerCapture) Snapshot(_ context.Context, appID string, _, _ int) (map[string]any, error) {
	c.appID = appID
	return map[string]any{"app_id": appID}, nil
}
