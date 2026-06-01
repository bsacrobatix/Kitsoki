package host_test

// Stream-event fidelity tests — assert that assistant narration
// ("thinking") prose reaches the StreamSink in full, never clipped with
// an ellipsis. Regression for the bug where onelinePreview(.,120) was
// the *only* text the transcript ever saw, so a long thought rendered as
// "…before proposing anythin…". The compact Preview (slog trace + tool
// breadcrumb) stays clipped; the new Text field carries the full prose.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"kitsoki/internal/host"
)

// captureStreamSink records every StreamEvent it receives. OnStreamEvent
// runs on the oracle stdout-reader goroutine, so guard with a mutex.
type captureStreamSink struct {
	mu     sync.Mutex
	events []host.StreamEvent
}

func (s *captureStreamSink) OnStreamEvent(_ context.Context, ev host.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *captureStreamSink) all() []host.StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]host.StreamEvent(nil), s.events...)
}

// thinkingRunner emits a stream-json transcript with a long thought as a
// text-only assistant message, the same thought paired with a tool_use
// in one message, then a terminal result.
func thinkingRunner(thought, reply string) host.ClaudeRunner {
	return func(_ context.Context, _ []string, _ string, _ string) (host.ClaudeRun, error) {
		lines := []string{
			`{"type":"system","subtype":"init","session_id":"sess-stream-1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":` + mustJSON(thought) + `}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":` + mustJSON(thought) + `},` +
				`{"type":"tool_use","name":"Read","input":{"file_path":"prompt.md"}}]}}`,
			`{"type":"result","subtype":"success","result":` + mustJSON(reply) + `,"session_id":"sess-stream-1"}`,
		}
		return host.ClaudeRun{Stdout: strings.Join(lines, "\n") + "\n"}, nil
	}
}

// TestOracleStream_ThinkingNotTruncated asserts the full thought reaches
// the StreamSink (Text), while the compact Preview stays clipped — and
// that a combined text+tool_use message surfaces BOTH the full thought
// and the tool breadcrumb rather than one clobbering the other.
func TestOracleStream_ThinkingNotTruncated(t *testing.T) {
	t.Parallel()

	// Echoes the real symptom; must exceed the 120-rune preview cap so a
	// clip would be observable.
	const longThought = "I'll explore the PRD story tree to understand how " +
		"clarification questions are currently handled before proposing " +
		"anything substantial, then trace the routing and gate-decider wiring."
	if got := len([]rune(longThought)); got <= 120 {
		t.Fatalf("fixture must exceed the 120-rune cap to be meaningful; got %d runes", got)
	}

	sink := &memSink{}
	stream := &captureStreamSink{}
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("go"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	ctx := host.WithStreamSink(oracleCtxForTest(sink), stream)
	ctx = host.WithClaudeRunner(ctx, thinkingRunner(longThought, "done"))

	if _, err := host.OracleAskHandler(ctx, map[string]any{"prompt_path": promptPath}); err != nil {
		t.Fatalf("OracleAskHandler: %v", err)
	}

	var sawTextOnly, sawCombined bool
	for _, ev := range stream.all() {
		if ev.Type != "assistant" || ev.Text == "" {
			continue
		}
		// The thought must reach the sink intact — this is the bug.
		if strings.Contains(ev.Text, "…") {
			t.Errorf("assistant Text was clipped with an ellipsis: %q", ev.Text)
		}
		if ev.Text != longThought {
			t.Errorf("assistant Text = %q, want full thought %q", ev.Text, longThought)
		}

		switch ev.Tool {
		case "":
			sawTextOnly = true
			// The compact Preview is still deliberately clipped — that's
			// what makes the full-fidelity Text necessary.
			if !strings.HasSuffix(ev.Preview, "…") {
				t.Errorf("compact Preview should stay clipped; got %q", ev.Preview)
			}
		case "Read":
			sawCombined = true
			// Combined message: Preview carries the tool args, NOT the
			// thought (which lives in Text, in full).
			if ev.Preview != "prompt.md" {
				t.Errorf("combined event Preview = %q, want tool args %q", ev.Preview, "prompt.md")
			}
		}
	}

	if !sawTextOnly {
		t.Fatal("no text-only assistant narration reached the stream sink")
	}
	if !sawCombined {
		t.Fatal("combined text+tool_use event did not surface both Text and Tool")
	}
}
