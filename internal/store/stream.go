package store

// stream.go defines the OPTIONAL durable-stream read surface over the event
// log: a cursor-addressable, globally ordered view of every appended event.
// It is deliberately a separate interface from [Store] — the Store contract
// (and its SQLite/JSONL implementations) is unchanged; only backends whose
// events table carries a global stream position implement EventStream. Today
// that is the Postgres store (stream_pos BIGSERIAL, see schema_pg.sql and
// postgres_stream.go); SQLite callers get (nil, false) from [AsEventStream]
// and must fall back to their existing history reads.
//
// Ordering / no-miss contract (see postgres_stream.go for the mechanism): a
// cursor advanced to the Pos of the last entry a reader consumed never skips
// a row. Positions are not dense — a rolled-back append leaves a permanent,
// never-filled gap in the sequence — so readers must treat Pos as opaque and
// monotonic, not contiguous.

import (
	"context"

	"kitsoki/internal/app"
)

// StreamCursor is a position in the global event stream (the events table's
// stream_pos column). Cursors are client-held: a reader persists the Pos of
// the last entry it processed and resumes with ReadStream(after=thatPos).
// The zero cursor reads from the beginning of the stream.
type StreamCursor = int64

// StreamEntry is one event as seen from the global stream: the event itself
// plus its global position and owning session.
type StreamEntry struct {
	// Pos is the entry's global stream position. Strictly increasing across
	// the whole stream; NOT contiguous (rolled-back appends leave gaps).
	Pos StreamCursor
	// Session is the session the event belongs to.
	Session app.SessionID
	// Event is the full-fidelity event row.
	Event Event
}

// EventStream is the optional durable-stream capability of a Store. Backends
// that expose a global cursor over the event log implement it; discover it
// with [AsEventStream]. All methods are safe for concurrent use.
type EventStream interface {
	// ReadStream returns up to limit entries with Pos > after, in ascending
	// Pos order, across ALL sessions. Pass limit=0 for no limit. An empty
	// result means the cursor is at the tail (not an error).
	ReadStream(ctx context.Context, after StreamCursor, limit int) ([]StreamEntry, error)

	// ReadSessionStream is ReadStream restricted to one session. The cursor
	// space is the same global one — a per-session reader can hand its cursor
	// to a global reader and vice versa.
	ReadSessionStream(ctx context.Context, session app.SessionID, after StreamCursor, limit int) ([]StreamEntry, error)

	// WaitForEvents blocks until at least one entry with Pos > after exists,
	// then returns nil; the caller follows up with ReadStream to fetch it.
	// Returns ctx.Err() when ctx is cancelled first. Wake-ups are driven by
	// backend notifications when available, with a periodic re-check fallback
	// — the cursor read is the source of truth, notifications are only an
	// accelerant — so a lost notification delays the return, never loses it.
	WaitForEvents(ctx context.Context, after StreamCursor) error
}

// AsEventStream reports whether s exposes the durable-stream capability,
// returning it if so. Non-stream backends (SQLite, memory) return (nil,
// false) — callers choose their own fallback; nothing here panics or errors.
func AsEventStream(s Store) (EventStream, bool) {
	es, ok := s.(EventStream)
	return es, ok
}
