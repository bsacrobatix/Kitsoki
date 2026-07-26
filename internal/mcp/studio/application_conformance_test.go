package studio_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationconformance"
	"kitsoki/internal/mcp/studio"
)

// conformance-consumer:studio-mcp
const studioConformanceFixture = "application-conformance-v1.json"

func TestStudioApplicationConformanceFixture(t *testing.T) {
	fixture, err := applicationconformance.Load()
	require.NoError(t, err)
	storyPath, err := applicationconformance.MaterializeStory(t.TempDir())
	require.NoError(t, err)
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	client := connectInProcess(ctx, t, srv)
	opened, err := callTool(ctx, client, "session.new", map[string]any{
		"story_path": storyPath, "harness": "replay",
		"trace": filepath.Join(t.TempDir(), "trace.jsonl"),
	})
	require.NoError(t, err)
	var session studio.SessionOpenOK
	require.NoError(t, json.Unmarshal([]byte(contentText(opened)), &session))

	framed, err := callTool(ctx, client, "application.frame", map[string]any{"handle": session.Handle})
	require.NoError(t, err)
	var frame appplatform.Frame
	require.NoError(t, json.Unmarshal([]byte(contentText(framed)), &frame))
	require.Equal(t, fixture.Expected.FrameSchema, frame.Schema)
	require.Equal(t, fixture.Expected.ApplicationSemanticRef, frame.Semantic.Ref)
	require.Equal(t, fixture.Action.SemanticRef, frame.Actions[0].Semantic.Ref)

	called, err := callTool(ctx, client, "application.action", map[string]any{
		"handle": session.Handle, "action": fixture.Action.ID,
		"frame_revision": frame.Revision, "input": fixture.Action.Input,
	})
	require.NoError(t, err)
	require.False(t, called.IsError, contentText(called))
	var outcome appplatform.OutcomeEnvelope
	require.NoError(t, json.Unmarshal([]byte(contentText(called)), &outcome))
	normalized := applicationconformance.Normalize(outcome)
	require.Equal(t, fixture.Expected.OutcomeSchema, normalized.Schema)
	require.Equal(t, fixture.Expected.ReceiptSchema, normalized.ReceiptSchema)
	require.Equal(t, appplatform.TransportMCP, normalized.Transport)
	require.Equal(t, fixture.Action.HandlerSemanticRef, normalized.SemanticRef)

	event, err := callTool(ctx, client, "application.event", map[string]any{
		"handle": session.Handle, "event": fixture.Event.ID, "input": fixture.Event.Input,
	})
	require.NoError(t, err)
	require.False(t, event.IsError, contentText(event))
	var eventOutcome appplatform.OutcomeEnvelope
	require.NoError(t, json.Unmarshal([]byte(contentText(event)), &eventOutcome))
	require.Equal(t, fixture.Event.ID, eventOutcome.Receipt.EventID)
	require.Equal(t, appplatform.EventBackground, eventOutcome.Receipt.EventMode)

	feedback, err := callTool(ctx, client, "application.feedback", map[string]any{
		"handle": session.Handle, "page": fixture.Page, "ref": fixture.Feedback.Ref,
		"instruction": fixture.Feedback.Instruction, "kind": fixture.Feedback.Kind,
		"idempotency_key": fixture.Feedback.IdempotencyKey,
	})
	require.NoError(t, err)
	require.False(t, feedback.IsError, contentText(feedback))
	body := contentText(feedback)
	require.Contains(t, body, fixture.Feedback.Ref)
	for _, excluded := range fixture.Feedback.Excluded {
		require.False(t, strings.Contains(body, excluded), "feedback leaked %q", excluded)
	}
}
