package storydemo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"kitsoki/internal/applicationcapture"
	"kitsoki/internal/clock"
	"kitsoki/internal/host"
)

func TestBrokerCaptureTargetsAttachedNonOriginApplication(t *testing.T) {
	broker := applicationcapture.NewFileBroker(t.TempDir(), clock.Real())
	surface := applicationcapture.Surface{
		AppID: "target-app", PublicSessionID: "target-public",
		EngineSessionID: "target-engine", Actor: "alice", Revision: 4,
		ActionIDs: []string{"target-app.advance"},
	}
	if err := broker.Attach(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	ctx := host.WithActor(
		host.WithKitsokiSessionID(context.Background(), "origin-engine"),
		"alice",
	)
	capture := BrokerCapture{Broker: broker}
	resultCh := make(chan ToolResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := capture.Record(ctx, t.TempDir(), Manifest{
			Path: "manifest-ref",
			Capture: &CapturePlan{
				ApplicationID: "target-app",
				ScenarioRef:   "kitsoki://scenario/one",
				ActionIDs:     []string{"target-app.advance"},
			},
		})
		resultCh <- result
		errCh <- err
	}()
	var pending []applicationcapture.Request
	deadline := time.Now().Add(3 * time.Second)
	for len(pending) == 0 && time.Now().Before(deadline) {
		pending, _ = broker.Pending(context.Background(), surface)
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending) != 1 || pending[0].SessionID != "target-public" {
		t.Fatalf("pending = %#v", pending)
	}
	if _, err := broker.Acknowledge(context.Background(), surface, applicationcapture.Ack{
		RequestID: pending[0].ID, AppID: "target-app",
		SessionID: "target-public", Revision: 4,
		Receipts: []applicationcapture.ActionReceipt{{
			Index: 0, ActionID: "target-app.advance", ReceiptID: "ar_one",
			HandlerID: "target-app.advance", SemanticRef: "target-app.action.advance",
			IdempotencyKey: "application-action/v1:frame:4", FrameRevision: 4,
		}},
		Recording: json.RawMessage(`{"startTime":1,"endTime":2,"durationMs":1,"events":[{"type":4},{"type":2}]}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
		result := <-resultCh
		if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "rrweb" {
			t.Fatalf("capture result = %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not complete")
	}
}

func TestBrokerCaptureFailsClosedWithoutDistinctCompatibleSurface(t *testing.T) {
	broker := applicationcapture.NewFileBroker(t.TempDir(), clock.Real())
	if err := broker.Attach(context.Background(), applicationcapture.Surface{
		AppID: "same-app", PublicSessionID: "origin-public",
		EngineSessionID: "origin-engine", Actor: "alice", Revision: 1,
		ActionIDs: []string{"same-app.advance"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx := host.WithActor(
		host.WithKitsokiSessionID(context.Background(), "origin-engine"),
		"alice",
	)
	_, err := (BrokerCapture{Broker: broker}).Record(ctx, "", Manifest{
		Path: "manifest-ref",
		Capture: &CapturePlan{
			ApplicationID: "same-app", ScenarioRef: "kitsoki://scenario",
			ActionIDs: []string{"same-app.advance"},
		},
	})
	if !errors.Is(err, applicationcapture.ErrNoSurface) {
		t.Fatalf("record error = %v", err)
	}
}
