package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationcapture"
	"kitsoki/internal/clock"
	"kitsoki/internal/runstatus"
)

func TestApplicationCaptureRPCStreamsDurableRequestAndAcknowledgesOpaqueArtifact(t *testing.T) {
	def, err := app.LoadBytes([]byte(`
app: {id: capture-app, version: 1.0.0}
root: ready
intents:
  advance: {title: Advance}
states:
  ready:
    on:
      advance: [{target: ready}]
application:
  schema: application/v1
  name: Capture
  description: Capture application.
  semantic_ref: capture-app.application
  shell: {entry: home}
  pages:
    home:
      name: Home
      description: Capture page.
      semantic_ref: capture-app.page
      regions:
        main:
          name: Main
          description: Capture actions.
          semantic_ref: capture-app.region
          items:
            - card:
                id: capture
                name: Capture
                description: Capture card.
                semantic_ref: capture-app.card
                actions: [capture-app.advance]
  actions:
    capture-app.advance:
      name: Advance
      description: Advance the scenario.
      semantic_ref: capture-app.action.advance
      intent: advance
      state: ready
      routing_mode: exact
`))
	if err != nil {
		t.Fatal(err)
	}
	source := applicationTestSource{def: def, header: runstatus.SessionHeader{
		SessionID: "engine-session", AppID: "capture-app", CurrentState: "ready", Turn: 5,
	}}
	provider := &applicationTestProvider{
		def: def,
		entries: map[string]Entry{
			"public-session": {Source: source, Driver: &captureDriver{}},
		},
	}
	broker := applicationcapture.NewFileBroker(t.TempDir(), clock.Real())
	srv := NewMulti(provider,
		WithApplicationCaptureBroker(broker),
		WithDefaultActor("alice"),
		WithPollInterval(5*time.Millisecond),
	)
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)

	var subscribed struct {
		SubscriptionID string `json:"subscription_id"`
		Revision       uint64 `json:"revision"`
	}
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.capture.subscribe",
		map[string]any{"session_id": "public-session"}, &subscribed); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if subscribed.Revision != 5 || subscribed.SubscriptionID == "" {
		t.Fatalf("subscribe = %#v", subscribed)
	}
	if len(subscribed.SubscriptionID) != 32 {
		t.Fatalf("subscription id is not a 128-bit opaque token: %q", subscribed.SubscriptionID)
	}
	guessed, err := http.Get(httpServer.URL +
		"/rpc/application-captures?subscription_id=00000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	defer guessed.Body.Close()
	if guessed.StatusCode != http.StatusNotFound {
		t.Fatalf("guessed subscription status = %d, want 404", guessed.StatusCode)
	}
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.capture.unsubscribe",
		map[string]any{"subscription_id": subscribed.SubscriptionID, "actor": "mallory"}, nil); rpcErr == nil || rpcErr.Code != codeNotFound {
		t.Fatalf("cross-actor unsubscribe error = %#v", rpcErr)
	}

	resultCh := make(chan applicationcapture.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := broker.Request(context.Background(), "capture-app", "origin-session", "alice",
			applicationcapture.Plan{
				ScenarioRef: "kitsoki://story-demo/scenario/sha256/one",
				ActionIDs:   []string{"capture-app.advance"},
			},
			"manifest-ref",
		)
		resultCh <- result
		errCh <- err
	}()

	request, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	streamRequest, err := http.NewRequestWithContext(request, http.MethodGet,
		httpServer.URL+"/rpc/application-captures?subscription_id="+subscribed.SubscriptionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, "engine_session_id") || strings.Contains(line, `"actor"`) ||
		strings.Contains(line, "selector") || strings.Contains(line, "command") || strings.Contains(line, "url") {
		t.Fatalf("capture request leaked private or executable fields: %s", line)
	}
	var notification struct {
		Method string                     `json:"method"`
		Params applicationcapture.Request `json:"params"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &notification); err != nil {
		t.Fatal(err)
	}
	if notification.Method != "runstatus.application.capture.request" ||
		notification.Params.Revision != 5 ||
		len(notification.Params.ActionIDs) != 1 {
		t.Fatalf("notification = %#v", notification)
	}
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.capture.fail", map[string]any{
		"session_id": "public-session", "request_id": notification.Params.ID,
		"app_id": "capture-app", "revision": 5, "actor": "mallory",
		"reason": "forged", "receipts": []any{},
	}, nil); rpcErr == nil || !strings.Contains(rpcErr.Message, "authorized") {
		t.Fatalf("cross-actor failure acknowledgement error = %#v", rpcErr)
	}
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.capture.ack", map[string]any{
		"session_id": "public-session", "request_id": notification.Params.ID,
		"app_id": "capture-app", "revision": 5,
		"receipts": []map[string]any{{
			"index": 0, "action_id": "capture-app.advance", "receipt_id": "ar_forged",
			"handler_id": "capture-app.advance", "semantic_ref": "capture-app.action.advance",
			"idempotency_key": "forged", "frame_revision": 5,
		}},
		"recording": map[string]any{
			"startTime": 1, "endTime": 2, "durationMs": 1,
			"events": []map[string]any{{"type": 4}, {"type": 2}},
		},
	}, nil); rpcErr == nil || !strings.Contains(rpcErr.Message, "absent from the server action ledger") {
		t.Fatalf("forged acknowledgement error = %#v", rpcErr)
	}
	var action appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.web_action", map[string]any{
		"session_id": "public-session", "action": "capture-app.advance",
		"frame_revision": 5, "input": map[string]any{},
		"idempotency_key": notification.Params.ID + ":0",
	}, &action); rpcErr != nil {
		t.Fatal(rpcErr)
	}

	var acked map[string]any
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.capture.ack", map[string]any{
		"session_id": "public-session",
		"request_id": notification.Params.ID,
		"app_id":     "capture-app",
		"revision":   5,
		"receipts": []map[string]any{{
			"index": 0, "action_id": "capture-app.advance",
			"receipt_id": action.Receipt.ID, "handler_id": action.Receipt.HandlerID,
			"semantic_ref":    action.Receipt.SemanticRef,
			"idempotency_key": action.Receipt.IdempotencyKey,
			"frame_revision":  action.Receipt.FrameRevision,
		}},
		"recording": map[string]any{
			"startTime": 1, "endTime": 2, "durationMs": 1,
			"events": []map[string]any{{"type": 4, "timestamp": 1}, {"type": 2, "timestamp": 2}},
		},
	}, &acked); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	ref, _ := acked["artifact_ref"].(string)
	if !strings.HasPrefix(ref, "kitsoki://application-capture/") {
		t.Fatalf("ack = %#v", acked)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
		if result := <-resultCh; result.ArtifactRef != ref {
			t.Fatalf("broker result = %#v, rpc ref = %q", result, ref)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("host-side capture waiter did not complete")
	}
}

func TestApplicationCaptureFailureAckInterruptsWaiterImmediately(t *testing.T) {
	def, err := app.LoadBytes([]byte(`
app: {id: fail-app, version: 1.0.0}
root: ready
intents:
  advance: {title: Advance}
states:
  ready:
    on:
      advance: [{target: ready}]
application:
  schema: application/v1
  name: Fail
  description: Failure capture.
  semantic_ref: fail-app.application
  shell: {entry: home}
  pages:
    home:
      name: Home
      description: Failure page.
      semantic_ref: fail-app.page
      regions:
        main:
          name: Main
          description: Failure region.
          semantic_ref: fail-app.region
          items:
            - card:
                id: fail
                name: Fail
                description: Failure card.
                semantic_ref: fail-app.card
                actions: [fail-app.advance]
  actions:
    fail-app.advance:
      name: Advance
      description: Advance.
      semantic_ref: fail-app.action.advance
      intent: advance
      state: ready
`))
	if err != nil {
		t.Fatal(err)
	}
	source := applicationTestSource{def: def, header: runstatus.SessionHeader{
		SessionID: "engine", AppID: "fail-app", CurrentState: "ready", Turn: 2,
	}}
	provider := &applicationTestProvider{def: def, entries: map[string]Entry{
		"public": {Source: source, Driver: &captureDriver{}},
	}}
	broker := applicationcapture.NewFileBroker(t.TempDir(), clock.Real())
	httpServer := httptest.NewServer(NewMulti(provider,
		WithApplicationCaptureBroker(broker), WithDefaultActor("alice"),
	).Handler())
	t.Cleanup(httpServer.Close)
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.capture.attach",
		map[string]any{"session_id": "public"}, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := broker.Request(context.Background(), "fail-app", "origin", "alice",
			applicationcapture.Plan{
				ScenarioRef: "kitsoki://scenario/fail",
				ActionIDs:   []string{"fail-app.advance"},
			}, "failure-manifest")
		errCh <- err
	}()
	var pending []applicationcapture.Request
	deadline := time.Now().Add(3 * time.Second)
	for len(pending) == 0 && time.Now().Before(deadline) {
		pending, _ = broker.Pending(context.Background(), applicationcapture.Surface{
			AppID: "fail-app", PublicSessionID: "public", EngineSessionID: "engine",
			Actor: "alice", Revision: 2, ActionIDs: []string{"fail-app.advance"},
		})
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatal("capture request did not become pending")
	}
	started := time.Now()
	if rpcErr := applicationRPCCall(t, httpServer, "runstatus.application.capture.fail", map[string]any{
		"session_id": "public", "request_id": pending[0].ID,
		"app_id": "fail-app", "revision": 2,
		"reason": "application_action_failed", "receipts": []any{},
	}, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, applicationcapture.ErrInterrupted) {
			t.Fatalf("waiter error = %v", err)
		}
		if time.Since(started) > time.Second {
			t.Fatalf("failure acknowledgement did not wake waiter promptly")
		}
	case <-time.After(time.Second):
		t.Fatal("failure acknowledgement left waiter blocked")
	}
}

func TestApplicationCaptureAuthorizesOnlyOfferedCardActions(t *testing.T) {
	hidden := false
	disabled := false
	frame := appplatform.Frame{
		Actions: []appplatform.Action{{ID: "catalog-only", Enabled: true}},
		Regions: []appplatform.Region{
			{State: appplatform.NodeState{Visible: &hidden}, Cards: []appplatform.Card{{
				Actions: []appplatform.Action{{ID: "hidden-region", Enabled: true}},
			}}},
			{Cards: []appplatform.Card{
				{State: appplatform.NodeState{Visible: &hidden}, Actions: []appplatform.Action{{ID: "hidden-card", Enabled: true}}},
				{Actions: []appplatform.Action{
					{ID: "disabled-state", Enabled: true, State: appplatform.NodeState{Enabled: &disabled}},
					{ID: "offered", Enabled: true},
				}, Body: []appplatform.Element{{
					State:   appplatform.NodeState{Visible: &hidden},
					Actions: []appplatform.Action{{ID: "hidden-element", Enabled: true}},
				}}},
			}},
		},
	}
	if got := applicationFrameActionIDs(frame); len(got) != 1 || got[0] != "offered" {
		t.Fatalf("capture actions = %#v", got)
	}
}

func TestApplicationCaptureSubscriptionExpiresWhileStreamIsOpen(t *testing.T) {
	registry := newApplicationCaptureSubscriptions()
	id, err := registry.subscribe("session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	sub := registry.lookup(id)
	if sub == nil {
		t.Fatal("subscription missing")
	}
	srv := &Server{
		poll:        time.Millisecond,
		captureSubs: registry,
	}
	httpServer := httptest.NewServer(http.HandlerFunc(srv.handleApplicationCaptures))
	t.Cleanup(httpServer.Close)
	response, err := http.Get(httpServer.URL + "?subscription_id=" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	registry.mu.Lock()
	sub.expiresAt = time.Now().Add(-time.Millisecond)
	registry.mu.Unlock()
	readDone := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(response.Body).ReadByte()
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("expired open subscription produced unexpected stream data")
		}
	case <-time.After(time.Second):
		t.Fatal("expired open subscription did not close")
	}
	if registry.active(sub) || registry.lookup(id) != nil {
		t.Fatal("expired subscription remained active")
	}
}
