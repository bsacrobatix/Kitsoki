package studio

// session_runtime.go — the trace-backed driving runtime behind a SessionHandle.
//
// A driving handle owns a live orchestrator wired to a JSONL trace plus a
// headless TUI model used purely as the slice-1 Frame composer — the same two
// pieces `kitsoki drive` assembles inline (cmd/kitsoki/drive.go,
// cmd/kitsoki/trace_session.go), reproduced here because those live in package
// main and the studio package cannot import them. The runtime is the studio's
// equivalent of setupTraceSession + newDriveModel: load the story, build a
// JSONL-backed orchestrator over the handle's harness, create a session, run
// the initial on_enter, and seed a RootModel so every drive/submit/continue
// folds its TurnOutcome through the one canonical ApplyTurnOutcome path before
// ComposeFrame paints the still.
//
// The interpretive seam is the harness the handle carries (replay by default,
// per shared decision 3). The runtime never builds a live harness itself — it
// drives whatever the HarnessBuilder produced — so the no-LLM default is owned
// upstream in handles.go and proven by the no-live-fallthrough test.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/app"
	"kitsoki/internal/harness"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/oracle"
	"kitsoki/internal/orchestrator"
	rsserver "kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
	"kitsoki/internal/tui"
)

// noRouteHarness is a placeholder harness for a runtime that never routes free
// text — the spec-render path (render.tui/png/web on a {story_path, state}
// spec), which only teleports + re-renders. The oracle registry constructor
// requires a non-nil harness even when no oracle will ever fire, so this
// satisfies the contract while failing loudly if anything DID try to route.
// Mirrors cmd/kitsoki noRunHarness (package main, not importable here).
type noRouteHarness struct{}

func (noRouteHarness) RunTurn(context.Context, harness.TurnInput) (mcpsdk.CallToolParams, error) {
	return mcpsdk.CallToolParams{}, fmt.Errorf("studio: noRouteHarness.RunTurn called (a spec render must never route free text)")
}
func (noRouteHarness) Close() error { return nil }

// sessionRuntime is the live driving substrate for one SessionHandle: the wired
// orchestrator, its JSONL trace sink, the bound session id, and the headless
// frame-composer model. It is stored on the handle (SessionHandle.Runtime) and
// torn down by CloseSession. Not safe for concurrent turns on one handle — the
// MCP server processes one tool call at a time per connection, and a handle is
// single-writer by construction.
type sessionRuntime struct {
	def   *app.AppDef
	orch  *orchestrator.Orchestrator
	sink  *store.JSONLSink
	sid   app.SessionID
	model tui.RootModel

	// driver binds the orchestrator + sid to the runstatus Driver API so the
	// session tools call the exact same Turn/SubmitDirect/ContinueTurn seam the
	// web surface drives (internal/runstatus/server/driver.go). This is the
	// "OrchestratorDriver directly" path the proposal names.
	driver rsserver.Driver

	// lastTurnErr is the orchestrator error from the most recent
	// drive/submit/continue (nil on success). turnResponse surfaces it as
	// outcome.error / mode="error" alongside the frame — a replay miss is a
	// turn-level failure the agent should see, not a transport error.
	lastTurnErr error

	closers []func()
}

// Close tears down the runtime in reverse construction order (LIFO), mirroring
// the defer order setupTraceSession uses. Idempotent: a second Close is a no-op.
func (rt *sessionRuntime) Close() {
	for i := len(rt.closers) - 1; i >= 0; i-- {
		rt.closers[i]()
	}
	rt.closers = nil
}

// newSessionRuntime builds the driving runtime for storyPath against the handle's
// harness h, writing the durable trace to tracePath. It is the studio twin of
// cmd/kitsoki setupTraceSession + newDriveModel:
//
//  1. open (or create) the JSONL trace;
//  2. load the story from disk;
//  3. build a JSONL-backed orchestrator over h;
//  4. create a session and record the effective story;
//  5. run the initial on_enter so the first frame matches a fresh TUI session;
//  6. seed the headless RootModel used as the Frame composer.
//
// h is owned by the runtime on success (Close tears it down) and on every error
// path (h is closed before returning), so the caller must NOT close it.
func newSessionRuntime(ctx context.Context, storyPath, tracePath string, h harness.Harness) (*sessionRuntime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if storyPath == "" {
		if h != nil {
			_ = h.Close()
		}
		return nil, &openError{Code: ErrBadRequest, Msg: "session: story_path is required"}
	}
	if tracePath == "" {
		if h != nil {
			_ = h.Close()
		}
		return nil, &openError{Code: ErrBadRequest, Msg: "session: trace path is required"}
	}

	rt := &sessionRuntime{}

	// A nil harness means "this runtime never routes free text" (the spec-render
	// path). The oracle registry still requires a non-nil harness, so substitute
	// a no-route placeholder that fails loudly if anything tries to route.
	if h == nil {
		h = noRouteHarness{}
	}

	// Own the harness immediately so EVERY error path below tears it down.
	rt.closers = append(rt.closers, func() { _ = h.Close() })

	// Ensure the trace directory exists, then open the JSONL trace.
	if dir := filepath.Dir(tracePath); dir != "" && dir != "." {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			rt.Close()
			return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: create trace dir: %v", mkErr)}
		}
	}
	sink, err := store.OpenJSONL(tracePath)
	if err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: open trace %q: %v", tracePath, err)}
	}
	rt.sink = sink
	rt.closers = append(rt.closers, func() { _ = sink.Close() })

	def, err := app.Load(storyPath)
	if err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: load story %q: %v", storyPath, err)}
	}
	rt.def = def

	m, err := machine.New(def)
	if err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: build machine: %v", err)}
	}

	// In-memory store for session/snapshot metadata; event writes redirect to
	// the JSONL sink via WithEventSink (the in-memory store is never the event
	// authority). This mirrors setupTraceSession exactly.
	s, err := store.OpenMemory()
	if err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: open in-memory store: %v", err)}
	}
	rt.closers = append(rt.closers, func() { _ = s.Close() })

	hostReg := host.NewRegistry()
	host.RegisterBuiltins(hostReg)
	if err := hostReg.ValidateAllowList(def.Hosts); err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: validate hosts: %v", err)}
	}

	oracleReg, oracleRegErr := oracle.BuildRegistryFromDef(def, h)
	if oracleRegErr != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: build oracle registry: %v", oracleRegErr)}
	}
	rt.closers = append(rt.closers, func() { _ = oracleReg.Close() })

	orch := orchestrator.New(def, m, s, h,
		orchestrator.WithHostRegistry(hostReg),
		orchestrator.WithEventSink(sink),
		orchestrator.WithEventSinkAuthority(true),
		orchestrator.WithOracleRegistry(oracleReg),
	)
	rt.orch = orch

	sid, err := orch.NewSession(ctx)
	if err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: new session: %v", err)}
	}
	rt.sid = sid
	rt.driver = rsserver.OrchestratorDriver{Orch: orch, SID: sid}

	if err := orch.RecordEffectiveStory(ctx, sid); err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: record effective story: %v", err)}
	}

	// Run the initial state's on_enter chain so the first frame and the session
	// world match a fresh TUI session (drive does the same on a fresh trace).
	if err := orch.RunInitialOnEnter(ctx, sid); err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: fmt.Sprintf("session: run initial on_enter: %v", err)}
	}

	model, err := newComposerModel(orch, sid)
	if err != nil {
		rt.Close()
		return nil, &openError{Code: ErrBadRequest, Msg: err.Error()}
	}
	rt.model = model

	return rt, nil
}

// newComposerModel seeds the headless TUI model used solely as the slice-1
// Frame composer (the studio twin of cmd/kitsoki newDriveModel). It loads the
// journey, renders the initial typed view, and constructs a RootModel with no
// app path (edit mode disabled) so a drive folds outcomes through the same
// ApplyTurnOutcome path the live TUI runs.
func newComposerModel(orch *orchestrator.Orchestrator, sid app.SessionID) (tui.RootModel, error) {
	j, err := orch.LoadJourney(sid)
	if err != nil {
		return tui.RootModel{}, fmt.Errorf("session: load journey: %w", err)
	}
	initialView, typedView, env, rr, err := orch.InitialViewTyped(j.World)
	if err != nil {
		return tui.RootModel{}, fmt.Errorf("session: render initial view: %w", err)
	}
	return tui.NewRootModel(orch, sid, "", initialView,
		tui.WithInitialTypedView(typedView, env, rr),
	), nil
}

// drive routes free text through the orchestrator turn loop (orch.Turn — the
// interpretive seam), folds the outcome into the composer model, and returns the
// outcome plus the recomposed Frame at the given geometry. The Frame is always
// returned, even on a turn error, so the caller can show the agent the screen
// alongside the structured failure.
func (rt *sessionRuntime) drive(ctx context.Context, input string, cols, rows int) (*orchestrator.TurnOutcome, tui.Frame) {
	out, err := rt.driver.Turn(ctx, input)
	rt.lastTurnErr = err
	rt.model = rt.model.ApplyTurnOutcome(out, input, err)
	return out, tui.ComposeFrame(&rt.model, cols, rows)
}

// submit applies a chosen intent + slots with no routing (SubmitDirect — the
// deterministic menu-pick path) and returns the outcome plus the recomposed
// Frame.
func (rt *sessionRuntime) submit(ctx context.Context, intent string, slots map[string]any, cols, rows int) (*orchestrator.TurnOutcome, tui.Frame) {
	out, err := rt.driver.SubmitDirect(ctx, intent, slots)
	rt.lastTurnErr = err
	rt.model = rt.model.ApplyTurnOutcome(out, "", err)
	return out, tui.ComposeFrame(&rt.model, cols, rows)
}

// cont supplies missing slots for a pending clarification (ContinueTurn) and
// returns the outcome plus the recomposed Frame.
func (rt *sessionRuntime) cont(ctx context.Context, slots map[string]any, cols, rows int) (*orchestrator.TurnOutcome, tui.Frame) {
	out, err := rt.driver.ContinueTurn(ctx, slots)
	rt.lastTurnErr = err
	rt.model = rt.model.ApplyTurnOutcome(out, "", err)
	return out, tui.ComposeFrame(&rt.model, cols, rows)
}

// frame recomposes the current still WITHOUT advancing the machine — the
// read-only re-render render.tui/render.tui_png use on a handle. It reads the
// composer model as-is (the last settled paint), so "look at this" can never
// mutate state, world, or the trace (principle of least surprise).
func (rt *sessionRuntime) frame(cols, rows int) tui.Frame {
	return tui.ComposeFrame(&rt.model, cols, rows)
}

// history returns the JSONL trace events recorded so far, in append order, for
// session.trace. It reads the live sink (the same events `kitsoki turn --trace`
// writes); it never mutates anything.
func (rt *sessionRuntime) history() store.History {
	if rt.sink == nil {
		return nil
	}
	return rt.sink.History()
}
