package host_test

// oracle_dispatch_test.go — tests for the B-2 oracle dispatcher.
//
// Coverage:
//   - Dispatcher calls oracle.Ask and writes OracleCalled + OracleReturned events.
//   - Schema validation failure writes OracleError, not OracleReturned.
//   - errNoRegistry fallthrough when no registry is wired.
//   - Default plugin resolution (oracle.claude) when PluginName is empty.
//   - SubEvents appended between OracleCalled and OracleReturned.
//
// All tests use oracle.New(AskFunc) (builtin.inprocess) with a stub AskFunc.
// No real LLM calls; no real subprocesses.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/oracle"
	"kitsoki/internal/store"
	"kitsoki/internal/world"
)

// captureSink is a simple store.EventSink that records appended events.
type captureSink struct {
	events []store.Event
}

func (s *captureSink) Append(e store.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (s *captureSink) History() store.History { return store.History(s.events) }

func (s *captureSink) kindsInOrder() []store.EventKind {
	var kinds []store.EventKind
	for _, e := range s.events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// buildDispatchCtx injects an oracle.Registry with the given oracle under
// "oracle.claude" and an event sink for capturing events.
func buildDispatchCtx(t *testing.T, o oracle.Oracle) (context.Context, *captureSink) {
	t.Helper()
	reg := oracle.NewRegistry()
	reg.Register("oracle.claude", o)

	sink := &captureSink{}
	ctx := host.WithOracleRegistry(context.Background(), reg)
	ctx = host.WithOracleEventSink(ctx, sink)
	ctx = host.WithOracleCallCtx(ctx, host.OracleCallCtx{
		SessionID: app.SessionID("sess-dispatch-test"),
		Turn:      app.TurnNumber(1),
		StatePath: app.StatePath("room.state"),
	})
	return ctx, sink
}

// sampleDispatchRequest builds a minimal OracleDispatchRequest.
func sampleDispatchRequest() host.OracleDispatchRequest {
	return host.OracleDispatchRequest{
		Req: oracle.AskRequest{
			SessionID:  app.SessionID("sess-dispatch-test"),
			TurnNumber: app.TurnNumber(1),
			StatePath:  app.StatePath("room.state"),
			Verb:       "ask",
			PromptText: "what should I do?",
			WithArgs:   map[string]any{"repo": "test/repo"},
			World:      world.World{Vars: map[string]any{}},
			Deadline:   time.Now().Add(30 * time.Second),
			CallID:     "call-dispatch-001",
		},
		PluginName:   "oracle.claude",
		Verb:         "ask",
		Agent:        "test-agent",
		Model:        "haiku",
		PromptText:   "what should I do?",
		SystemPrompt: "you are a helpful assistant",
		InputDesc:    map[string]any{},
	}
}

// TestDispatch_HappyPath verifies that Dispatch writes OracleCalled +
// OracleReturned events when the oracle succeeds.
func TestDispatch_HappyPath(t *testing.T) {
	t.Parallel()
	want := json.RawMessage(`{"choice":"a","score":0.9}`)
	o := oracle.New(oracle.AskFunc(func(_ context.Context, _ oracle.AskRequest) (oracle.AskResponse, error) {
		return oracle.AskResponse{Submission: want}, nil
	}))

	ctx, sink := buildDispatchCtx(t, o)
	dr := sampleDispatchRequest()

	result, err := host.Dispatch(ctx, dr)
	if err != nil {
		t.Fatalf("Dispatch: unexpected error: %v", err)
	}
	if string(result.Submission) != string(want) {
		t.Errorf("Submission: got %s, want %s", result.Submission, want)
	}

	kinds := sink.kindsInOrder()
	if len(kinds) < 2 {
		t.Fatalf("expected at least 2 events (OracleCalled, OracleReturned), got %d: %v", len(kinds), kinds)
	}
	if kinds[0] != store.OracleCalled {
		t.Errorf("events[0]: got %q, want OracleCalled", kinds[0])
	}
	if kinds[len(kinds)-1] != store.OracleReturned {
		t.Errorf("events[last]: got %q, want OracleReturned", kinds[len(kinds)-1])
	}
}

// TestDispatch_NoRegistry verifies that Dispatch returns the errNoRegistry
// sentinel when no registry is wired.
func TestDispatch_NoRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background() // no registry injected
	dr := sampleDispatchRequest()

	_, err := host.Dispatch(ctx, dr)
	if err == nil {
		t.Fatal("expected errNoRegistry, got nil")
	}
	if !host.IsNoRegistryError(err) {
		t.Errorf("expected IsNoRegistryError(err)==true, got false; err=%v", err)
	}
}

// TestDispatch_OracleError verifies that an oracle.Ask error writes OracleError
// and returns the error.
func TestDispatch_OracleError(t *testing.T) {
	t.Parallel()
	askErr := &oracle.AskError{Kind: "plugin_crash", Detail: "intentional test error"}
	o := oracle.New(oracle.AskFunc(func(_ context.Context, _ oracle.AskRequest) (oracle.AskResponse, error) {
		return oracle.AskResponse{}, askErr
	}))

	ctx, sink := buildDispatchCtx(t, o)
	dr := sampleDispatchRequest()

	_, err := host.Dispatch(ctx, dr)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ae *oracle.AskError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *oracle.AskError, got %T: %v", err, err)
	}
	if ae.Kind != "plugin_crash" {
		t.Errorf("AskError.Kind: got %q, want plugin_crash", ae.Kind)
	}

	kinds := sink.kindsInOrder()
	if len(kinds) < 2 {
		t.Fatalf("expected at least 2 events (OracleCalled, OracleError), got %d: %v", len(kinds), kinds)
	}
	if kinds[0] != store.OracleCalled {
		t.Errorf("events[0]: got %q, want OracleCalled", kinds[0])
	}
	if kinds[len(kinds)-1] != store.OracleError {
		t.Errorf("events[last]: got %q, want OracleError", kinds[len(kinds)-1])
	}
}

// TestDispatch_SchemaValidationFailure verifies that when the oracle returns a
// submission that fails schema validation, OracleError is written and no
// OracleReturned is written.
func TestDispatch_SchemaValidationFailure(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"choice": {"type": "string"}},
		"required": ["choice"],
		"additionalProperties": false
	}`)

	// Oracle returns a submission that fails the schema (extra field).
	badSubmission := json.RawMessage(`{"choice":"a","extra":"not allowed"}`)
	o := oracle.New(oracle.AskFunc(func(_ context.Context, _ oracle.AskRequest) (oracle.AskResponse, error) {
		return oracle.AskResponse{Submission: badSubmission}, nil
	}))

	ctx, sink := buildDispatchCtx(t, o)
	dr := sampleDispatchRequest()
	dr.Req.SchemaJSON = schema

	_, err := host.Dispatch(ctx, dr)
	if err == nil {
		t.Fatal("expected schema validation error, got nil")
	}
	var ae *oracle.AskError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *oracle.AskError, got %T: %v", err, err)
	}
	if ae.Kind != "schema_invalid" {
		t.Errorf("AskError.Kind: got %q, want schema_invalid", ae.Kind)
	}

	// OracleCalled should be first, OracleError should be last.
	kinds := sink.kindsInOrder()
	if len(kinds) < 2 {
		t.Fatalf("expected at least 2 events, got %d: %v", len(kinds), kinds)
	}
	if kinds[0] != store.OracleCalled {
		t.Errorf("events[0]: got %q, want OracleCalled", kinds[0])
	}
	if kinds[len(kinds)-1] != store.OracleError {
		t.Errorf("events[last]: got %q, want OracleError", kinds[len(kinds)-1])
	}
	// OracleReturned must NOT appear.
	for _, k := range kinds {
		if k == store.OracleReturned {
			t.Error("OracleReturned should not be written on schema validation failure")
		}
	}
}

// TestDispatch_SchemaValid verifies that a valid schema submission writes
// OracleReturned (not OracleError).
func TestDispatch_SchemaValid(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"choice":{"type":"string"}},"required":["choice"],"additionalProperties":false}`)
	goodSubmission := json.RawMessage(`{"choice":"a"}`)
	o := oracle.New(oracle.AskFunc(func(_ context.Context, _ oracle.AskRequest) (oracle.AskResponse, error) {
		return oracle.AskResponse{Submission: goodSubmission}, nil
	}))

	ctx, sink := buildDispatchCtx(t, o)
	dr := sampleDispatchRequest()
	dr.Req.SchemaJSON = schema

	result, err := host.Dispatch(ctx, dr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Submission) != string(goodSubmission) {
		t.Errorf("Submission: got %s, want %s", result.Submission, goodSubmission)
	}

	kinds := sink.kindsInOrder()
	last := kinds[len(kinds)-1]
	if last != store.OracleReturned {
		t.Errorf("events[last]: got %q, want OracleReturned", last)
	}
}

// TestDispatch_SubEvents verifies that SubEvents are appended between
// OracleCalled and OracleReturned.
func TestDispatch_SubEvents(t *testing.T) {
	t.Parallel()
	subEvent := store.Event{
		Kind:    store.EventKind("oracle.test.internal_step"),
		Payload: json.RawMessage(`{"step":1}`),
	}
	o := oracle.New(oracle.AskFunc(func(_ context.Context, _ oracle.AskRequest) (oracle.AskResponse, error) {
		return oracle.AskResponse{
			Submission: json.RawMessage(`{"ok":true}`),
			SubEvents:  []store.Event{subEvent},
		}, nil
	}))

	ctx, sink := buildDispatchCtx(t, o)
	dr := sampleDispatchRequest()

	_, err := host.Dispatch(ctx, dr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kinds := sink.kindsInOrder()
	// Expected order: OracleCalled, oracle.test.internal_step, OracleReturned
	if len(kinds) < 3 {
		t.Fatalf("expected at least 3 events, got %d: %v", len(kinds), kinds)
	}
	if kinds[0] != store.OracleCalled {
		t.Errorf("events[0]: got %q, want OracleCalled", kinds[0])
	}
	if kinds[1] != store.EventKind("oracle.test.internal_step") {
		t.Errorf("events[1]: got %q, want oracle.test.internal_step", kinds[1])
	}
	if kinds[len(kinds)-1] != store.OracleReturned {
		t.Errorf("events[last]: got %q, want OracleReturned", kinds[len(kinds)-1])
	}
}

// TestDispatch_DefaultPluginResolution verifies that empty PluginName resolves
// to oracle.claude.
func TestDispatch_DefaultPluginResolution(t *testing.T) {
	t.Parallel()
	called := false
	o := oracle.New(oracle.AskFunc(func(_ context.Context, _ oracle.AskRequest) (oracle.AskResponse, error) {
		called = true
		return oracle.AskResponse{Submission: json.RawMessage(`{"ok":true}`)}, nil
	}))

	ctx, _ := buildDispatchCtx(t, o)
	dr := sampleDispatchRequest()
	dr.PluginName = "" // empty → should resolve to oracle.claude

	_, err := host.Dispatch(ctx, dr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("oracle.Ask was not called when PluginName was empty")
	}
}

// TestDispatch_CallIDPreserved verifies that the CallID in the request is
// written to the event payload.
func TestDispatch_CallIDPreserved(t *testing.T) {
	t.Parallel()
	o := oracle.New(oracle.AskFunc(func(_ context.Context, req oracle.AskRequest) (oracle.AskResponse, error) {
		return oracle.AskResponse{Submission: json.RawMessage(`{}`)}, nil
	}))

	ctx, sink := buildDispatchCtx(t, o)
	dr := sampleDispatchRequest()
	dr.Req.CallID = "my-stable-call-id"

	_, err := host.Dispatch(ctx, dr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check OracleCalled event carries the CallID.
	var foundCallID bool
	for _, e := range sink.events {
		if e.Kind == store.OracleCalled && e.CallID == "my-stable-call-id" {
			foundCallID = true
		}
	}
	if !foundCallID {
		t.Error("OracleCalled event does not carry the expected CallID")
	}
}
