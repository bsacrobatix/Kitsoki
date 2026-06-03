// Package server implements the live runstatus HTTP surface: it serves the
// bundled runstatus SPA and answers the JSON-RPC + SSE contract that the
// SPA's live data source (tools/runstatus/src/data/live-source.ts) expects.
//
// It is the read side of a kitsoki run. Given the JSONL trace a run writes
// (`kitsoki run --trace run.jsonl`) and the app definition, it parses the
// trace into a [runstatus.Snapshot] on demand and streams newly-appended
// events to connected browsers as the run grows the file. It never mutates
// anything.
//
// Why the JSONL trace and not the SQLite session store: the store persists
// only turn/seq/ts/kind/payload — it drops per-event state_path, call_id, and
// parent_turn, which the SPA needs (notably oracle-call pairing by call_id).
// The JSONL trace is the canonical, full-fidelity record. See
// tools/runstatus/CLAUDE.md: the trace itself must always be correct, so the
// live view is built from it directly.
//
// # Endpoints
//
//	GET  /                                     → the bundled SPA (index.html)
//	POST /rpc                                  → JSON-RPC 2.0 control
//	GET  /rpc/events?subscription_id=<id>      → text/event-stream notifications
//
// # JSON-RPC methods (POST /rpc)
//
//	runstatus.sessions.list      {}                                  → []SessionHeader
//	runstatus.session.get        {session_id}                        → SessionHeader
//	runstatus.session.app        {session_id}                        → AppDef
//	runstatus.session.mermaid    {session_id, detail?}               → {source, node_map}
//	runstatus.session.trace      {session_id, since_turn?, until_turn?, limit?}
//	                                                                 → {events, last_turn}
//	runstatus.session.subscribe  {session_id}                        → {subscription_id}
//	runstatus.session.unsubscribe {subscription_id}                  → {ok: true}
//
// v1 serves a single trace (one session). session_id params are accepted but
// the one trace is always served; sessions.list returns 0–1 entries.
//
// # Streaming
//
// After subscribe, the client opens the SSE stream with the returned
// subscription_id. The server polls the trace file and emits one JSON-RPC
// notification per newly-appended event:
//
//	{"jsonrpc":"2.0","method":"runstatus.event",
//	 "params":{"subscription_id":"…","event":{<TraceEvent>}}}
//
// A subscription remembers how many events it has already delivered, so an SSE
// reconnect with the same subscription_id resumes without re-sending events.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/web"
)

// defaultPollInterval is how often the SSE stream re-reads the trace for newly
// appended events. localhost debug tool; 500ms is responsive without busy-spin.
const defaultPollInterval = 500 * time.Millisecond

// Server answers the runstatus live contract for one [Source].
// It is safe for concurrent use.
type Server struct {
	src    Source
	driver Driver // nil for read-only surfaces (status serve)
	poll   time.Duration

	mu     sync.Mutex
	subs   map[string]*subscription
	nextID int
}

// subscription tracks one SSE stream slot. sent is the number of events
// already delivered, so reconnects resume rather than replay.
type subscription struct {
	id   string
	mu   sync.Mutex
	sent int
}

// Option configures a Server.
type Option func(*Server)

// WithPollInterval overrides the SSE trace-poll interval.
func WithPollInterval(d time.Duration) Option {
	return func(s *Server) {
		if d > 0 {
			s.poll = d
		}
	}
}

// WithDriver attaches the write side: with it set, the session.turn / submit /
// continue / offpath RPCs advance the live session. Without it the surface is
// read-only. `kitsoki web` sets it; `kitsoki status serve` does not.
func WithDriver(d Driver) Option {
	return func(s *Server) { s.driver = d }
}

// New builds a Server that serves the run recorded in the JSONL trace at
// tracePath, interpreted against def — the read-only `kitsoki status serve`
// path.
func New(tracePath string, def *app.AppDef, opts ...Option) *Server {
	return NewWithSource(&traceFileSource{path: tracePath, def: def}, opts...)
}

// NewWithSource builds a Server backed by an arbitrary [Source]. `kitsoki
// status serve` uses [New] (a read-only trace file); `kitsoki web` uses this
// with a live in-process [LiveSession] so the same SPA, RPC, and SSE surface
// observes a running session.
func NewWithSource(src Source, opts ...Option) *Server {
	s := &Server{
		src:  src,
		poll: defaultPollInterval,
		subs: make(map[string]*subscription),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Source supplies the runstatus surface with a view of one session. The server
// is source-agnostic: it never reads a file or an event sink directly. Two
// implementations exist — the read-only trace-file tailer behind [New], and the
// live in-process [LiveSession] behind `kitsoki web`. Implementations MUST be
// safe for concurrent use: the SSE poller and the RPC handlers call them from
// many goroutines at once.
type Source interface {
	// Snapshot returns the full session state now: header, diagram, and events.
	Snapshot() (runstatus.Snapshot, error)
	// Events returns just the trace events known so far. It is the cheap path
	// the SSE poller hits every tick, avoiding a diagram re-render per poll.
	Events() ([]runstatus.TraceEvent, error)
	// AppDef returns the static app definition without building a Snapshot.
	AppDef() *app.AppDef
}

// traceFileSource is the read-only [Source] behind `kitsoki status serve`: it
// re-reads and re-parses the JSONL trace file on each call, so a growing file
// (a live run appending to it) is reflected on the next poll. A not-yet-created
// file is treated as an empty run rather than an error, so the UI can connect
// before the first event is written.
type traceFileSource struct {
	path string
	def  *app.AppDef
}

func (t *traceFileSource) AppDef() *app.AppDef { return t.def }

func (t *traceFileSource) Events() ([]runstatus.TraceEvent, error) {
	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	events, err := runstatus.ParseTrace(f, nil)
	if err != nil {
		return nil, err
	}
	runstatus.AggregateTaskDetails(events)
	return events, nil
}

func (t *traceFileSource) Snapshot() (runstatus.Snapshot, error) {
	events, err := t.Events()
	if err != nil {
		return runstatus.Snapshot{}, err
	}
	// AggregateTaskDetails already ran in Events; SnapshotFromTrace re-runs it
	// (idempotent — it never overwrites existing detail) and renders the
	// diagram + header.
	return runstatus.SnapshotFromTrace(t.def, events, runstatus.HeaderOverrides{}, true), nil
}

// Handler returns the HTTP handler for the runstatus surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", s.handleRPC)
	mux.HandleFunc("/rpc/events", s.handleEvents)
	mux.HandleFunc("/", s.handleIndex)
	return mux
}

// ── Static SPA ────────────────────────────────────────────────────────────

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	index, err := web.IndexHTML()
	if err != nil {
		// SPA not bundled into this binary — actionable 503 rather than a 404.
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}

// ── JSON-RPC ─────────────────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes (subset of the spec plus a generic server error).
const (
	codeParseError    = -32700
	codeMethodMissing = -32601
	codeServerError   = -32000
	// codeReadOnly is returned for a write RPC (turn/submit/continue/offpath)
	// when the surface has no live session Driver — i.e. `kitsoki status serve`,
	// which only observes a recorded trace.
	codeReadOnly = -32001
)

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}})
		return
	}

	result, rerr := s.dispatch(r.Context(), req.Method, req.Params)
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// dispatch routes a JSON-RPC method to its handler. It returns either a result
// value or a *rpcError (never both). session_id params are accepted but
// ignored — this server serves the single configured session.
//
// The read methods (sessions.list / session.*) answer from the [Source]. The
// write methods (session.turn / submit / continue / offpath) advance the live
// session through the [Driver]; they return codeReadOnly when no Driver is set.
func (s *Server) dispatch(ctx context.Context, method string, params map[string]any) (any, *rpcError) {
	if params == nil {
		params = map[string]any{}
	}
	switch method {
	case "runstatus.sessions.list":
		snap, err := s.src.Snapshot()
		if err != nil {
			return nil, serverErr(err)
		}
		// 0 entries until the run has emitted at least one event; else the one
		// session this trace records.
		if len(snap.Events) == 0 {
			return []runstatus.SessionHeader{}, nil
		}
		return []runstatus.SessionHeader{snap.Session}, nil

	case "runstatus.session.get":
		snap, err := s.src.Snapshot()
		if err != nil {
			return nil, serverErr(err)
		}
		return snap.Session, nil

	case "runstatus.session.app":
		return s.src.AppDef(), nil

	case "runstatus.session.mermaid":
		snap, err := s.src.Snapshot()
		if err != nil {
			return nil, serverErr(err)
		}
		return snap.Mermaid, nil

	case "runstatus.session.trace":
		snap, err := s.src.Snapshot()
		if err != nil {
			return nil, serverErr(err)
		}
		return filterTrace(snap, params), nil

	case "runstatus.session.subscribe":
		return s.subscribe()

	case "runstatus.session.unsubscribe":
		id, _ := params["subscription_id"].(string)
		s.unsubscribe(id)
		return map[string]any{"ok": true}, nil

	case "runstatus.session.view":
		// Read of the live session's CURRENT room (render + menu) without
		// advancing it. Requires a Driver (a live session); the read-only
		// trace surface has none, so it returns codeReadOnly like the write
		// RPCs — there is no in-process session to query.
		if s.driver == nil {
			return nil, readOnlyErr(method)
		}
		out, err := s.driver.View(ctx)
		if err != nil {
			return nil, serverErr(err)
		}
		return newTurnResult(out, s.driver), nil

	// ── Write methods (live session only) ─────────────────────────────────
	case "runstatus.session.turn":
		if s.driver == nil {
			return nil, readOnlyErr(method)
		}
		input, _ := params["input"].(string)
		out, err := s.driver.Turn(ctx, input)
		if err != nil {
			return nil, serverErr(err)
		}
		return newTurnResult(out, s.driver), nil

	case "runstatus.session.submit":
		if s.driver == nil {
			return nil, readOnlyErr(method)
		}
		name, _ := params["intent"].(string)
		if name == "" {
			return nil, &rpcError{Code: codeServerError, Message: "session.submit: missing 'intent'"}
		}
		slots, _ := params["slots"].(map[string]any)
		out, err := s.driver.SubmitDirect(ctx, name, slots)
		if err != nil {
			return nil, serverErr(err)
		}
		return newTurnResult(out, s.driver), nil

	case "runstatus.session.continue":
		if s.driver == nil {
			return nil, readOnlyErr(method)
		}
		slots, _ := params["slots"].(map[string]any)
		out, err := s.driver.ContinueTurn(ctx, slots)
		if err != nil {
			return nil, serverErr(err)
		}
		return newTurnResult(out, s.driver), nil

	case "runstatus.session.offpath":
		if s.driver == nil {
			return nil, readOnlyErr(method)
		}
		input, _ := params["input"].(string)
		answer, err := s.driver.AskOffPath(ctx, input)
		if err != nil {
			return nil, serverErr(err)
		}
		return map[string]any{"answer": answer}, nil

	default:
		return nil, &rpcError{Code: codeMethodMissing, Message: "unknown method: " + method}
	}
}

func readOnlyErr(method string) *rpcError {
	return &rpcError{Code: codeReadOnly, Message: method + ": this surface is read-only (no live session)"}
}

func serverErr(err error) *rpcError {
	return &rpcError{Code: codeServerError, Message: err.Error()}
}

// traceResult is the runstatus.session.trace response shape.
type traceResult struct {
	Events   []runstatus.TraceEvent `json:"events"`
	LastTurn int                    `json:"last_turn"`
}

// filterTrace slices snap.Events by the optional since_turn / until_turn /
// limit params. last_turn is the high-water turn of the whole run so the
// client knows where to resume on reconnect.
func filterTrace(snap runstatus.Snapshot, params map[string]any) traceResult {
	since, hasSince := intParam(params, "since_turn")
	until, hasUntil := intParam(params, "until_turn")
	limit, hasLimit := intParam(params, "limit")

	out := make([]runstatus.TraceEvent, 0, len(snap.Events))
	for _, ev := range snap.Events {
		if hasSince && ev.Turn < since {
			continue
		}
		if hasUntil && ev.Turn > until {
			continue
		}
		out = append(out, ev)
		if hasLimit && limit > 0 && len(out) >= limit {
			break
		}
	}
	return traceResult{Events: out, LastTurn: snap.Session.Turn}
}

// intParam reads a numeric param (arrives as JSON float64) as an int.
func intParam(params map[string]any, key string) (int, bool) {
	switch v := params[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

// ── Subscriptions + SSE ─────────────────────────────────────────────────────

func (s *Server) subscribe() (map[string]any, *rpcError) {
	// Seed sent with the current event count so the stream carries only events
	// appended after subscribe; the initial load comes from session.trace.
	events, err := s.src.Events()
	if err != nil {
		return nil, serverErr(err)
	}
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("sub-%d", s.nextID)
	s.subs[id] = &subscription{id: id, sent: len(events)}
	s.mu.Unlock()
	return map[string]any{"subscription_id": id}, nil
}

func (s *Server) unsubscribe(id string) {
	s.mu.Lock()
	delete(s.subs, id)
	s.mu.Unlock()
}

func (s *Server) lookupSub(id string) *subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subs[id]
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("subscription_id")
	sub := s.lookupSub(id)
	if sub == nil {
		http.Error(w, "unknown subscription", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		s.streamNew(w, flusher, sub)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// streamNew emits a runstatus.event notification for every event appended to
// the trace since the subscription's last delivery, then advances the
// watermark.
func (s *Server) streamNew(w http.ResponseWriter, flusher http.Flusher, sub *subscription) {
	sub.mu.Lock()
	defer sub.mu.Unlock()

	events, err := s.src.Events()
	if err != nil || len(events) <= sub.sent {
		return
	}
	for _, ev := range events[sub.sent:] {
		frame := map[string]any{
			"jsonrpc": "2.0",
			"method":  "runstatus.event",
			"params": map[string]any{
				"subscription_id": sub.id,
				"event":           ev,
			},
		}
		b, err := json.Marshal(frame)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	sub.sent = len(events)
	flusher.Flush()
}
