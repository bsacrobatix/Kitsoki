package journal_test

import (
	"testing"

	"kitsoki/internal/journal"
)

func TestDefaultPolicy_WorldEvery20Turns(t *testing.T) {
	t.Parallel()

	p := journal.DefaultPolicy()
	doc := journal.DocID("world")

	// Turns where checkpoint should fire.
	fireTurns := []int64{20, 40, 60, 80, 100}
	for _, turn := range fireTurns {
		ctx := journal.CheckpointContext{Turn: turn}
		if !p.ShouldCheckpoint(doc, ctx) {
			t.Errorf("world: ShouldCheckpoint(turn=%d) = false, want true", turn)
		}
	}

	// Turns where it should not.
	noFireTurns := []int64{0, 1, 10, 19, 21, 39, 41}
	for _, turn := range noFireTurns {
		ctx := journal.CheckpointContext{Turn: turn}
		if p.ShouldCheckpoint(doc, ctx) {
			t.Errorf("world: ShouldCheckpoint(turn=%d) = true, want false", turn)
		}
	}
}

func TestDefaultPolicy_StateEvery20Turns(t *testing.T) {
	t.Parallel()

	p := journal.DefaultPolicy()
	doc := journal.DocID("state")

	if !p.ShouldCheckpoint(doc, journal.CheckpointContext{Turn: 20}) {
		t.Error("state: ShouldCheckpoint(turn=20) = false, want true")
	}
	if p.ShouldCheckpoint(doc, journal.CheckpointContext{Turn: 21}) {
		t.Error("state: ShouldCheckpoint(turn=21) = true, want false")
	}
}

func TestDefaultPolicy_ChatsEvery10Messages(t *testing.T) {
	t.Parallel()

	p := journal.DefaultPolicy()
	doc := journal.DocID("chats/chat-abc")

	// Should fire at 10, 20, 30 messages.
	fireCounts := []int{10, 20, 30}
	for _, n := range fireCounts {
		ctx := journal.CheckpointContext{ChatMessageCount: n}
		if !p.ShouldCheckpoint(doc, ctx) {
			t.Errorf("chats: ShouldCheckpoint(messages=%d) = false, want true", n)
		}
	}

	// Should not fire for non-multiples.
	noFireCounts := []int{0, 1, 9, 11, 19, 21}
	for _, n := range noFireCounts {
		ctx := journal.CheckpointContext{ChatMessageCount: n}
		if p.ShouldCheckpoint(doc, ctx) {
			t.Errorf("chats: ShouldCheckpoint(messages=%d) = true, want false", n)
		}
	}
}

func TestDefaultPolicy_JobsOnStatusTransition(t *testing.T) {
	t.Parallel()

	p := journal.DefaultPolicy()
	doc := journal.DocID("jobs/job-xyz")

	if !p.ShouldCheckpoint(doc, journal.CheckpointContext{JobStatusChanged: true}) {
		t.Error("jobs: ShouldCheckpoint(status changed) = false, want true")
	}
	if p.ShouldCheckpoint(doc, journal.CheckpointContext{JobStatusChanged: false}) {
		t.Error("jobs: ShouldCheckpoint(status not changed) = true, want false")
	}
}

func TestDefaultPolicy_ChatsIgnoresTurnForNonMultiple(t *testing.T) {
	t.Parallel()

	// The turn counter alone should not trigger a chat checkpoint —
	// only the message count matters.
	p := journal.DefaultPolicy()
	doc := journal.DocID("chats/any")
	ctx := journal.CheckpointContext{Turn: 20, ChatMessageCount: 5}
	if p.ShouldCheckpoint(doc, ctx) {
		t.Error("chats: ShouldCheckpoint(turn=20, messages=5) = true, want false")
	}
}
