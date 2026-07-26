package server

// session_events.go is the cursor-based session-events surface over the
// durable event stream (stateless-orchestrator-storage-plan §1.2). When a
// session's store exposes [store.EventStream] (the Postgres backends), two
// additive paths replace the 500 ms whole-history re-read:
//
//   - runstatus.session.events {session_id, since?, limit?} — a paged RPC
//     returning {events, next_cursor, live}. `since` is the opaque int64
//     stream cursor (0 = from the beginning); `limit` caps the page (0 = no
//     limit). Events are the existing [runstatus.TraceEvent] wire shape,
//     mapped 1:1 from the full-fidelity [store.Event] via
//     [runstatus.ToTraceEvent]: Kind→msg, Ts→time, Turn→turn,
//     StatePath→state_path, ParentTurn→parent_turn, Payload→attrs with
//     call_id merged in, and the entry's owning session stamped into
//     session_id. next_cursor is the Pos of the last returned event (or the
//     request's since when the page is empty) — pass it back as `since` to
//     resume exactly after it. live:true marks the answer as served from the
//     durable stream, so the cursor stays valid across server restarts.
//
//   - a cursor-driven SSE loop inside GET /rpc/events: when the subscribed
//     session carries a [SessionStream], the handler blocks on
//     [store.EventStream.WaitForEvents] and reads by cursor instead of
//     re-mapping the whole history on a ticker. The wire frames are the SAME
//     runstatus.event / runstatus.session_gone notifications the ticker path
//     emits, with one additive params field: "cursor", the event's stream
//     position. An optional `since` query parameter overrides the
//     subscription's server-held cursor, so a client that persisted the last
//     cursor it processed can resume without loss even after a server
//     restart (re-subscribe, then connect with since=<last cursor>).
//
// Non-stream sources (SQLite, trace files, in-memory test sources) are
// untouched: the RPC reports codeStreamUnsupported and the SSE handler keeps
// the existing ticker path.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/store"
)

// SessionStream couples a store-level [store.EventStream] with the store
// session id its rows are keyed by. The provider stamps one onto [Entry] when
// (and only when) the session's backing store exposes the durable-stream
// capability — discover it with [store.AsEventStream]. SID is required
// because the provider's public session id (the RPC session_id) need not
// equal the store's session id.
type SessionStream struct {
	Stream store.EventStream
	SID    app.SessionID
}

// streamSSEBatch bounds one cursor read inside the SSE loop so a large
// catch-up is flushed to the client incrementally instead of buffered whole.
const streamSSEBatch = 512

// sessionEventsResult is the runstatus.session.events response shape.
type sessionEventsResult struct {
	// Events is the page of trace events, oldest first, in the exact wire
	// shape runstatus.session.trace and the SSE runstatus.event frames use.
	Events []runstatus.TraceEvent `json:"events"`
	// NextCursor resumes reading exactly after the last returned event: pass
	// it as `since` on the next call. Equal to the request's since when the
	// page is empty (the cursor is at the tail). Opaque, monotonic, NOT
	// contiguous — rolled-back appends leave permanent gaps.
	NextCursor store.StreamCursor `json:"next_cursor"`
	// Live is true when the page was served from the durable stream (always,
	// for this RPC — non-stream backends error instead), telling the client
	// the cursor survives server restarts.
	Live bool `json:"live"`
}

// sessionEvents answers runstatus.session.events (see the file comment for
// the contract). Requires the session's Entry to carry a [SessionStream];
// codeStreamUnsupported otherwise so SQLite/file callers fall back to
// runstatus.session.trace + subscribe.
func (s *Server) sessionEvents(ctx context.Context, params map[string]any) (any, *rpcError) {
	sid, _ := params["session_id"].(string)
	entry, ok := s.provider.Get(sid)
	if !ok {
		return nil, &rpcError{Code: codeNotFound, Message: "unknown session_id: " + sid}
	}
	ss := entry.Stream
	if ss == nil || ss.Stream == nil {
		return nil, &rpcError{
			Code:    codeStreamUnsupported,
			Message: "session store has no durable event stream (postgres backends only); use runstatus.session.trace + session.subscribe instead",
		}
	}
	since := cursorParam(params, "since")
	limit, _ := intParam(params, "limit")
	entries, err := ss.Stream.ReadSessionStream(ctx, ss.SID, since, limit)
	if err != nil {
		return nil, serverErr(err)
	}
	res := sessionEventsResult{
		Events:     make([]runstatus.TraceEvent, 0, len(entries)),
		NextCursor: since,
		Live:       true,
	}
	for _, ent := range entries {
		res.Events = append(res.Events, streamTraceEvent(ent))
		res.NextCursor = ent.Pos
	}
	return res, nil
}

// streamTraceEvent maps one stream entry to the SPA-facing wire event: the
// shared [runstatus.ToTraceEvent] store.Event mapping plus the owning session
// id, which the global stream carries per row (History reads don't).
func streamTraceEvent(ent store.StreamEntry) runstatus.TraceEvent {
	ev := runstatus.ToTraceEvent(ent.Event)
	ev.SessionID = string(ent.Session)
	return ev
}

// cursorParam reads an int64 stream-cursor param (arrives as JSON float64).
// Absent or non-numeric reads as 0 — the beginning of the stream.
func cursorParam(params map[string]any, key string) store.StreamCursor {
	switch v := params[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}

// sessionTailCursor returns the session's current end-of-stream cursor: the
// Pos of its last appended event, or 0 for a session with no events yet. Used
// once at subscribe time to seed "only events appended after subscribe" —
// the stream analogue of the ticker path's sent = len(events) watermark.
func sessionTailCursor(ctx context.Context, ss *SessionStream) (store.StreamCursor, error) {
	entries, err := ss.Stream.ReadSessionStream(ctx, ss.SID, 0, 0)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	return entries[len(entries)-1].Pos, nil
}

// handleEventsStream is the cursor-driven SSE loop behind GET /rpc/events for
// subscriptions whose session carries a [SessionStream]. Two phases:
//
//  1. Catch-up: per-session cursor reads (indexed, cheap) drain everything
//     from the subscription cursor to the session tail.
//  2. Tail: block on WaitForEvents, then read the GLOBAL stream forward and
//     filter for this session. The global read is what keeps the wait cursor
//     advancing past other sessions' appends — waiting on the session cursor
//     alone would spin, because WaitForEvents wakes for ANY Pos > after.
//
// The subscription state that matters is just the cursor: it is stored back
// onto the subscription after every delivery (an SSE reconnect resumes
// there), and the client sees it on every frame, so it can resume across a
// server restart via the `since` query parameter. Session liveness is checked
// every poll interval (a cheap provider map lookup — no event read), emitting
// the same terminal runstatus.session_gone frame as the ticker path.
func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, sub *subscription) {
	ctx := r.Context()
	ss := sub.stream

	sub.mu.Lock()
	cursor := sub.cursor
	sub.mu.Unlock()
	// Client-held cursor override: resume exactly after the last event the
	// client processed, regardless of what this (or a previous) server
	// process remembers.
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			cursor = n
		}
	}

	// Phase 1 — catch-up on this session's rows.
	for {
		entries, err := ss.Stream.ReadSessionStream(ctx, ss.SID, cursor, streamSSEBatch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !s.streamBackoff(ctx) {
				return
			}
			continue
		}
		if len(entries) == 0 {
			break
		}
		cursor = s.emitStreamEntries(w, flusher, sub, entries, cursor)
	}

	// Phase 2 — tail the global stream, filtering for this session.
	waitCursor := cursor
	for {
		if s.streamSessionGone(w, flusher, sub) {
			return
		}
		// Bound the wait by the poll interval so session liveness is
		// re-checked even when no events flow anywhere. The notification
		// wake-up inside WaitForEvents keeps event delivery instant.
		wctx, cancel := context.WithTimeout(ctx, s.poll)
		err := ss.Stream.WaitForEvents(wctx, waitCursor)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue // liveness tick — loop re-checks session_gone
		}
		for {
			entries, err := ss.Stream.ReadStream(ctx, waitCursor, streamSSEBatch)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if !s.streamBackoff(ctx) {
					return
				}
				break
			}
			if len(entries) == 0 {
				break
			}
			waitCursor = entries[len(entries)-1].Pos
			mine := entries[:0:0]
			for _, ent := range entries {
				if ent.Session == ss.SID {
					mine = append(mine, ent)
				}
			}
			if len(mine) > 0 {
				cursor = s.emitStreamEntries(w, flusher, sub, mine, cursor)
			}
		}
	}
}

// emitStreamEntries writes one runstatus.event frame per entry (the existing
// wire shape plus the additive "cursor" param), flushes, advances the
// subscription's stored cursor, and returns the new cursor.
func (s *Server) emitStreamEntries(w io.Writer, flusher http.Flusher, sub *subscription, entries []store.StreamEntry, cursor store.StreamCursor) store.StreamCursor {
	for _, ent := range entries {
		frame := map[string]any{
			"jsonrpc": "2.0",
			"method":  "runstatus.event",
			"params": map[string]any{
				"subscription_id": sub.id,
				"event":           streamTraceEvent(ent),
				"cursor":          ent.Pos,
			},
		}
		b, err := json.Marshal(frame)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		cursor = ent.Pos
	}
	flusher.Flush()
	sub.mu.Lock()
	if cursor > sub.cursor {
		sub.cursor = cursor
	}
	sub.mu.Unlock()
	return cursor
}

// streamSessionGone emits the terminal runstatus.session_gone frame and
// reports true when the subscription's session no longer resolves (evicted —
// swarm-session-cap). Unlike the ticker path there is no "never seen" grace:
// a stream subscription only exists because subscribe resolved the session.
func (s *Server) streamSessionGone(w io.Writer, flusher http.Flusher, sub *subscription) bool {
	if _, ok := s.provider.Get(sub.sessionID); ok {
		return false
	}
	frame := map[string]any{
		"jsonrpc": "2.0",
		"method":  "runstatus.session_gone",
		"params": map[string]any{
			"subscription_id": sub.id,
			"session_id":      sub.sessionID,
		},
	}
	if b, err := json.Marshal(frame); err == nil {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	return true
}

// streamBackoff sleeps one poll interval after a transient stream read error
// so a flapping database doesn't busy-spin the handler. Returns false when
// the request context ended during the sleep.
func (s *Server) streamBackoff(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(s.poll):
		return true
	}
}
