package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationconformance"
	"kitsoki/internal/host"
)

// conformance-consumer:jsonrpc
// conformance-consumer:web-go
const serverConformanceFixture = "application-conformance-v1.json"

func TestApplicationBoundRPCConformanceFixture(t *testing.T) {
	fixture, err := applicationconformance.Load()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	server, driver, live := applicationRPCFixture(t, WithMaterializeRoot(root))

	var frame appplatform.Frame
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.frame", map[string]any{
		"session_id": fixture.SessionID, "page": fixture.Page,
	}, &frame); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if frame.Schema != fixture.Expected.FrameSchema ||
		frame.Semantic.Ref != fixture.Expected.ApplicationSemanticRef ||
		frame.PageSemantic.Ref != fixture.Expected.PageSemanticRef {
		t.Fatalf("canonical frame identity = %#v", frame)
	}

	for _, bound := range []struct {
		method    string
		transport appplatform.Transport
	}{
		{"runstatus.application.action", appplatform.TransportJSONRPC},
		{"runstatus.application.web_action", appplatform.TransportWeb},
	} {
		var outcome appplatform.OutcomeEnvelope
		if rpcErr := applicationRPCCall(t, server, bound.method, map[string]any{
			"session_id": frame.SessionID, "page": frame.Page,
			"action": fixture.Action.ID, "frame_revision": frame.Revision,
			"input": fixture.Action.Input,
		}, &outcome); rpcErr != nil {
			t.Fatal(rpcErr)
		}
		got := applicationconformance.Normalize(outcome)
		if got.Schema != fixture.Expected.OutcomeSchema ||
			got.ReceiptSchema != fixture.Expected.ReceiptSchema ||
			got.Outcome != fixture.Expected.Outcome ||
			got.SemanticRef != fixture.Action.HandlerSemanticRef ||
			got.Transport != bound.transport ||
			got.ApplicationID != fixture.ApplicationID ||
			got.Page != fixture.Page {
			t.Fatalf("%s normalized outcome = %#v", bound.method, got)
		}
	}

	var event appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.event", map[string]any{
		"session_id": fixture.SessionID, "event": fixture.Event.ID, "input": fixture.Event.Input,
	}, &event); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	eventResult := applicationconformance.Normalize(event)
	if eventResult.EventID != fixture.Event.ID ||
		eventResult.EventMode != appplatform.EventMode(fixture.Event.Mode) ||
		eventResult.Transport != appplatform.TransportEvent {
		t.Fatalf("background event = %#v", eventResult)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := live.applicationEventSched.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	if driver.lastSlots["item_id"] != fixture.Event.Input["item_id"] {
		t.Fatalf("background event slots = %#v", driver.lastSlots)
	}

	var feedback struct {
		Report struct {
			Schema string                `json:"schema"`
			Anchor host.AnnotationAnchor `json:"anchor"`
		} `json:"report"`
	}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.feedback", map[string]any{
		"session_id": fixture.SessionID, "page": fixture.Page,
		"ref": fixture.Feedback.Ref, "instruction": fixture.Feedback.Instruction,
		"kind": fixture.Feedback.Kind, "idempotency_key": fixture.Feedback.IdempotencyKey,
	}, &feedback); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".artifacts", "feedback", "feedback.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(feedback)
	for _, excluded := range fixture.Feedback.Excluded {
		if strings.Contains(string(raw), excluded) || strings.Contains(string(encoded), excluded) {
			t.Fatalf("feedback leaked %q", excluded)
		}
	}
	if feedback.Report.Schema != fixture.Expected.FeedbackSchema ||
		feedback.Report.Anchor.SemanticElement == nil ||
		feedback.Report.Anchor.SemanticElement.Ref != fixture.Feedback.Ref {
		t.Fatalf("feedback = %#v", feedback)
	}
}
