// Package tui — single-slot, latest-wins input queue for the routing
// chip's in-flight window (semantic-routing proposal §8.2).
//
// The chats-package queue (internal/chats/queue.go) is the durable,
// SQLite-backed PTY chat input queue: it persists drives across
// restarts, journals every enqueue/dequeue, and serves many concurrent
// dispatchers. That's the wrong shape for the TUI's "user kept typing
// while the resolver was working" case, which is purely a UI-loop
// concern with strictly one in-flight turn and zero durability
// requirements. We do NOT want to write to disk every time the user
// types a line they immediately replace.
//
// So this is a deliberately separate, tiny adapter — the single-slot
// semantics described in §8.2: "latest wins, drop intermediates with
// a small footer indicator (`1 queued · esc to cancel`)". On
// resolution the queued line is replayed through the same submit
// pipeline.
//
// Concurrency: the queue is owned by the RootModel, mutated only from
// the Bubbletea Update goroutine. No locking is required at this
// layer; if a later refactor moves it off the event loop, add a
// sync.Mutex around the three fields.
package tui

import "fmt"

// pendingLine is the single-slot queue used while the chip is
// in flight. Zero value is "empty queue".
type pendingLine struct {
	// text holds the most recent line the user submitted while a
	// turn was resolving. Empty string ≡ "no line queued".
	text string
	// dropped counts how many older queued lines were displaced by
	// newer ones. Surfaced in the footer for transparency
	// ("1 queued (2 dropped)" reads better than just "1 queued"
	// when the user has been hammering Enter).
	dropped int
}

// Enqueue records a submitted line. If a line was already queued, it
// is discarded and the dropped counter increments. The empty input
// is silently ignored (whitespace-only lines are caller-trimmed).
func (q *pendingLine) Enqueue(line string) {
	if line == "" {
		return
	}
	if q.text != "" {
		q.dropped++
	}
	q.text = line
}

// Take returns the queued line (if any) and clears the slot. The
// caller is responsible for re-submitting the line through whatever
// path produced the in-flight turn.
func (q *pendingLine) Take() (string, bool) {
	if q.text == "" {
		return "", false
	}
	out := q.text
	q.text = ""
	q.dropped = 0
	return out, true
}

// HasPending reports whether a line is queued.
func (q *pendingLine) HasPending() bool { return q.text != "" }

// FooterIndicator returns the inline status text for the prompt
// footer: empty when nothing is queued, "1 queued · esc to cancel"
// for one fresh line, "1 queued (N dropped) · esc to cancel" once
// older lines have been displaced. The string is style-free — the
// surrounding view layer is responsible for applying any colour.
func (q *pendingLine) FooterIndicator() string {
	if q.text == "" {
		return ""
	}
	if q.dropped == 0 {
		return "1 queued · esc to cancel"
	}
	return fmt.Sprintf("1 queued (%d dropped) · esc to cancel", q.dropped)
}

// Clear empties the queue without returning the line. Used when the
// in-flight turn is cancelled — there's nothing to replay against.
func (q *pendingLine) Clear() {
	q.text = ""
	q.dropped = 0
}
