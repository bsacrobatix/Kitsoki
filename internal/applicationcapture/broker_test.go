package applicationcapture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/clock"
)

func TestFileBrokerRevisionEvolutionIdempotencyAndPrivacy(t *testing.T) {
	root := t.TempDir()
	broker := NewFileBroker(root, clock.Real())
	initial := Surface{
		AppID: "app", PublicSessionID: "public", EngineSessionID: "engine",
		Actor: "alice", Revision: 7, ActionIDs: []string{"open"},
	}
	if err := broker.Attach(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	plan := Plan{ScenarioRef: "kitsoki://scenario/one", ActionIDs: []string{"open", "confirm"}}
	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := broker.Request(context.Background(), "app", "origin", "alice", plan, "record-1")
		resultCh <- result
		errCh <- err
	}()
	waitPending(t, broker, initial, 1)

	advanced := initial
	advanced.Revision = 9
	advanced.ActionIDs = []string{"confirm"}
	if err := broker.Attach(context.Background(), advanced); err != nil {
		t.Fatal(err)
	}
	pending := waitPending(t, broker, advanced, 1)
	if pending[0].Revision != 7 {
		t.Fatalf("request revision = %d, want pinned revision 7", pending[0].Revision)
	}
	recording := json.RawMessage(`{"startTime":100,"endTime":130,"durationMs":30,"events":[{"type":4,"timestamp":100,"data":{"href":"token=supersecretvalue"}},{"type":2,"timestamp":130}]}`)
	ackResult, err := broker.Acknowledge(context.Background(), advanced, Ack{
		RequestID: pending[0].ID, AppID: "app", SessionID: "public", Revision: 7,
		Receipts: []ActionReceipt{
			canonicalReceipt(pending[0].ID, 0, "open", 7),
			canonicalReceipt(pending[0].ID, 1, "confirm", 8),
		},
		Recording: recording,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ackResult.ArtifactRef, "kitsoki://application-capture/") {
		t.Fatalf("artifact ref = %q", ackResult.ArtifactRef)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not observe acknowledgement")
	}
	got := <-resultCh
	if got.ArtifactRef != ackResult.ArtifactRef {
		t.Fatalf("request result = %#v, ack = %#v", got, ackResult)
	}
	raw, err := os.ReadFile(got.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supersecretvalue") || !strings.Contains(string(raw), harscrubRedactedForTest) {
		t.Fatalf("recording was not scrubbed: %s", raw)
	}

	replayed, err := broker.Request(context.Background(), "app", "origin", "alice", plan, "record-1")
	if err != nil || replayed.ArtifactRef != got.ArtifactRef {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	_, err = broker.Request(context.Background(), "app", "origin", "alice",
		Plan{ScenarioRef: plan.ScenarioRef, ActionIDs: []string{"different"}}, "record-1")
	if err == nil || !strings.Contains(err.Error(), "idempotency conflict") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestFileBrokerRestartReemitsPendingAndRejectsOtherActor(t *testing.T) {
	root := t.TempDir()
	first := NewFileBroker(root, clock.Real())
	surface := Surface{
		AppID: "app", PublicSessionID: "public", EngineSessionID: "engine",
		Actor: "alice", Revision: 3, ActionIDs: []string{"save"},
	}
	if err := first.Attach(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan error, 1)
	go func() {
		_, err := first.Request(context.Background(), "app", "origin", "alice",
			Plan{ScenarioRef: "kitsoki://scenario/restart", ActionIDs: []string{"save"}}, "restart-key")
		resultCh <- err
	}()
	pending := waitPending(t, first, surface, 1)

	restarted := NewFileBroker(root, clock.Real())
	if pendingBeforeAttach, err := restarted.Pending(context.Background(), surface); !errors.Is(err, ErrNoSurface) ||
		len(pendingBeforeAttach) != 0 {
		t.Fatalf("restart without attachment = %#v, %v", pendingBeforeAttach, err)
	}
	if err := restarted.Attach(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	reemitted := waitPending(t, restarted, surface, 1)
	if reemitted[0].ID != pending[0].ID {
		t.Fatalf("restart request = %q, want %q", reemitted[0].ID, pending[0].ID)
	}
	other := surface
	other.Actor = "mallory"
	if err := restarted.Attach(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	_, err := restarted.Acknowledge(context.Background(), other, Ack{
		RequestID: pending[0].ID, AppID: "app", SessionID: "public", Revision: 3,
		Receipts:  []ActionReceipt{canonicalReceipt(pending[0].ID, 0, "save", 3)},
		Recording: validRecording(),
	})
	if err == nil || !strings.Contains(err.Error(), "authorized") {
		t.Fatalf("cross-actor ack error = %v", err)
	}
	if _, err := restarted.Acknowledge(context.Background(), surface, Ack{
		RequestID: pending[0].ID, AppID: "app", SessionID: "public", Revision: 3,
		Receipts:  []ActionReceipt{canonicalReceipt(pending[0].ID, 0, "save", 3)},
		Recording: validRecording(),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("original waiter did not observe restarted broker acknowledgement")
	}
}

func TestFileBrokerInterruptedTruthAndRequiredIdempotency(t *testing.T) {
	broker := NewFileBroker(t.TempDir(), clock.Real())
	surface := Surface{
		AppID: "app", PublicSessionID: "public", EngineSessionID: "engine",
		Actor: "alice", Revision: 1, ActionIDs: []string{"save"},
	}
	if err := broker.Attach(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	plan := Plan{ScenarioRef: "kitsoki://scenario/one", ActionIDs: []string{"save"}}
	if _, err := broker.Request(context.Background(), "app", "origin", "alice", plan, ""); err == nil {
		t.Fatal("expected missing idempotency key to fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := broker.Request(ctx, "app", "origin", "alice", plan, "cancel-key")
		done <- err
	}()
	waitPending(t, broker, surface, 1)
	cancel()
	if err := <-done; !errors.Is(err, ErrInterrupted) {
		t.Fatalf("cancel error = %v", err)
	}
	waitPending(t, broker, surface, 0)
}

func TestFileBrokerAllowsEmptyAttachmentButRejectsUnofferedFirstAction(t *testing.T) {
	broker := NewFileBroker(t.TempDir(), clock.Real())
	surface := Surface{
		AppID: "app", PublicSessionID: "public", EngineSessionID: "engine",
		Actor: "alice", Revision: 1,
	}
	if err := broker.Attach(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	_, err := broker.Request(context.Background(), "app", "origin", "alice",
		Plan{ScenarioRef: "kitsoki://scenario", ActionIDs: []string{"not-offered"}}, "key")
	if !errors.Is(err, ErrNoSurface) || !strings.Contains(err.Error(), "first action") {
		t.Fatalf("request error = %v", err)
	}
}

func TestFileBrokerFailsClosedOnAmbiguousNonOriginSurfaces(t *testing.T) {
	broker := NewFileBroker(t.TempDir(), clock.Real())
	for _, engine := range []string{"target-a", "target-b"} {
		if err := broker.Attach(context.Background(), Surface{
			AppID: "app", PublicSessionID: "public-" + engine,
			EngineSessionID: engine, Actor: "alice", Revision: 1,
			ActionIDs: []string{"open"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := broker.Request(context.Background(), "app", "origin", "alice",
		Plan{ScenarioRef: "kitsoki://scenario", ActionIDs: []string{"open"}}, "key")
	if !errors.Is(err, ErrNoSurface) || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("ambiguous request error = %v", err)
	}
}

func TestFileBrokerPrunesStaleSurfaceAndSelectsOnlyLiveTarget(t *testing.T) {
	clk := clock.NewFake(time.Unix(100, 0))
	broker := NewFileBroker(t.TempDir(), clk)
	stale := Surface{
		AppID: "app", PublicSessionID: "stale-public", EngineSessionID: "stale-target",
		Actor: "alice", Revision: 1, ActionIDs: []string{"open"},
	}
	if err := broker.Attach(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	clk.Advance(surfaceLease + time.Millisecond)
	live := Surface{
		AppID: "app", PublicSessionID: "live-public", EngineSessionID: "live-target",
		Actor: "alice", Revision: 3, ActionIDs: []string{"open"},
	}
	if err := broker.Attach(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if len(broker.surfaces) != 1 {
		t.Fatalf("attached surface count = %d, want stale surface pruned", len(broker.surfaces))
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := broker.Request(context.Background(), "app", "origin", "alice",
			Plan{ScenarioRef: "kitsoki://scenario", ActionIDs: []string{"open"}}, "key")
		errCh <- err
	}()
	pending := waitPending(t, broker, live, 1)
	if pending[0].SessionID != live.PublicSessionID {
		t.Fatalf("selected session = %q, want live target %q", pending[0].SessionID, live.PublicSessionID)
	}
	if err := broker.Fail(context.Background(), live, FailureAck{
		RequestID: pending[0].ID, AppID: live.AppID, SessionID: live.PublicSessionID,
		Revision: live.Revision, Reason: "test complete",
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; !errors.Is(err, ErrInterrupted) {
		t.Fatalf("request completion error = %v", err)
	}
}

func TestFileBrokerPersistsFailureReasonAndPartialCanonicalReceipts(t *testing.T) {
	broker := NewFileBroker(t.TempDir(), clock.Real())
	surface := Surface{
		AppID: "app", PublicSessionID: "public", EngineSessionID: "target",
		Actor: "alice", Revision: 6, ActionIDs: []string{"open"},
	}
	if err := broker.Attach(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := broker.Request(context.Background(), "app", "origin", "alice",
			Plan{ScenarioRef: "kitsoki://scenario", ActionIDs: []string{"open", "confirm"}},
			"failure-key")
		errCh <- err
	}()
	pending := waitPending(t, broker, surface, 1)
	partial := []ActionReceipt{canonicalReceipt(pending[0].ID, 0, "open", 6)}
	if err := broker.Fail(context.Background(), surface, FailureAck{
		RequestID: pending[0].ID, AppID: "app", SessionID: "public",
		Revision: 6, Reason: "application_action_failed", Receipts: partial,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; !errors.Is(err, ErrInterrupted) {
		t.Fatalf("waiter error = %v", err)
	}
	stored, err := broker.Lookup(context.Background(), surface, pending[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "interrupted" || stored.InterruptedReason != "application_action_failed" ||
		len(stored.Receipts) != 1 || stored.Receipts[0].ReceiptID != partial[0].ReceiptID {
		t.Fatalf("stored failure = %#v", stored)
	}
}

const harscrubRedactedForTest = "[REDACTED]"

func validRecording() json.RawMessage {
	return json.RawMessage(`{"startTime":1,"endTime":2,"durationMs":1,"events":[{"type":4,"timestamp":1},{"type":2,"timestamp":2}]}`)
}

func canonicalReceipt(requestID string, index int, actionID string, revision uint64) ActionReceipt {
	return ActionReceipt{
		Index: index, ActionID: actionID, ReceiptID: "ar_receipt",
		HandlerID: "handler", SemanticRef: "semantic",
		IdempotencyKey: requestID + ":" + fmt.Sprint(index),
		FrameRevision:  revision,
	}
}

func waitPending(t *testing.T, broker Broker, surface Surface, count int) []Request {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		pending, err := broker.Pending(context.Background(), surface)
		if err == nil && len(pending) == count {
			return pending
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending count never reached %d: %#v, %v", count, pending, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
