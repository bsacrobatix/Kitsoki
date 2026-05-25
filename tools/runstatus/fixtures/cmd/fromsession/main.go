// fromsession reads a recorded kitsoki session from the SQLite event log and
// emits a runstatus Snapshot JSON file. Use it to turn a real local session
// into a fixture for the run-status UI.
//
//	go run ./tools/runstatus/fixtures/cmd/fromsession \
//	    --db ~/.local/share/kitsoki/sessions.db \
//	    --session f911eedd-... \
//	    --app stories/bugfix/app.yaml \
//	    -o /tmp/bugfix.snapshot.json
//
// The produced JSON should not be committed if the underlying session
// contains internal project data; this tool is for local debugging and
// demoing.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/store"
	"kitsoki/internal/viz"
)

func main() {
	var (
		dbPath    string
		sessionID string
		appPath   string
		outPath   string
	)
	flag.StringVar(&dbPath, "db", "", "path to kitsoki sessions.db")
	flag.StringVar(&sessionID, "session", "", "session id to export")
	flag.StringVar(&appPath, "app", "", "path to app.yaml")
	flag.StringVar(&outPath, "o", "", "output snapshot.json path")
	flag.Parse()

	if dbPath == "" || sessionID == "" || appPath == "" || outPath == "" {
		fmt.Fprintln(os.Stderr, "all of --db --session --app -o are required")
		os.Exit(2)
	}

	def, err := app.Load(appPath)
	if err != nil {
		die("load app", err)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		die("open store", err)
	}
	defer func() { _ = s.Close() }()

	hist, err := s.LoadHistory(app.SessionID(sessionID))
	if err != nil {
		die("load history", err)
	}
	if len(hist) == 0 {
		die("no events", fmt.Errorf("session %q has no events", sessionID))
	}

	events, currentState, lastTurn, terminal, started := mapEvents(hist, def)

	fc, err := viz.FlowchartWithMap(def, viz.FlowchartOptions{Detail: viz.DetailStates})
	if err != nil {
		fmt.Fprintln(os.Stderr, "warn: flowchart:", err)
	}

	snap := runstatus.Snapshot{
		Session: runstatus.SessionHeader{
			SessionID:    sessionID,
			AppID:        def.App.ID,
			CurrentState: currentState,
			Turn:         lastTurn,
			StartedAt:    started,
			Terminal:     terminal,
		},
		App: def,
		Mermaid: runstatus.MermaidSnapshot{
			Source:  fc.Source,
			NodeMap: fc.NodeMap,
		},
		Events: events,
	}

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		die("marshal", err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		die("write", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d events to %s\n", len(events), outPath)
}

// mapEvents converts store.Event records into runstatus.TraceEvent records,
// tracking the most recent state path so each event carries one even though
// the source events don't store it inline.
func mapEvents(hist store.History, def *app.AppDef) (out []runstatus.TraceEvent, currentState string, lastTurn int, terminal bool, started time.Time) {
	out = make([]runstatus.TraceEvent, 0, len(hist))
	for i, ev := range hist {
		if i == 0 {
			started = ev.Ts
		}

		var payload map[string]any
		if len(ev.Payload) > 0 {
			_ = json.Unmarshal(ev.Payload, &payload)
		}

		// Track the current state from StateEntered events.
		if ev.Kind == store.StateEntered {
			if sp, ok := payload["state"].(string); ok {
				currentState = sp
			}
		}

		te := runstatus.TraceEvent{
			Time:      ev.Ts,
			Level:     levelFor(ev.Kind),
			Msg:       msgFor(ev.Kind),
			SessionID: "",
			Turn:      int(ev.Turn),
			StatePath: currentState,
			Attrs:     payload,
		}
		out = append(out, te)

		if int(ev.Turn) > lastTurn {
			lastTurn = int(ev.Turn)
		}
	}

	if currentState != "" {
		if st, ok := app.Compile(def).LookupState(app.StatePath(strings.ReplaceAll(currentState, "/", "."))); ok && st != nil && st.Terminal {
			terminal = true
		}
	}
	return
}

// msgFor maps a stored EventKind to the slog `msg` convention the SPA uses
// to pick subsystem chips. The prefixes (turn./harness./machine./host./oracle.)
// match those emitted by the live engine's slog handlers.
func msgFor(k store.EventKind) string {
	switch k {
	case store.TurnStarted:
		return "turn.start"
	case store.TurnEnded:
		return "turn.end"
	case store.LLMCalled:
		return "oracle.ask.start"
	case store.LLMToolCall:
		return "oracle.tool_call"
	case store.ValidationFailed:
		return "machine.validation_failed"
	case store.TransitionApplied:
		return "machine.transition"
	case store.EffectApplied:
		return "machine.effect"
	case store.HostInvoked:
		return "harness.called"
	case store.HostDispatched:
		return "harness.dispatched"
	case store.HostReturned:
		return "harness.returned"
	case store.StateExited:
		return "machine.state_exited"
	case store.StateEntered:
		return "machine.state_entered"
	case store.IntentAccepted:
		return "machine.intent_accepted"
	case store.GuardRejected:
		return "machine.guard_rejected"
	case store.OffPathEntered:
		return "machine.off_path_entered"
	case store.OffPathExited:
		return "machine.off_path_exited"
	case store.OffPathQuestion:
		return "oracle.off_path.question"
	case store.OffPathAnswer:
		return "oracle.off_path.answer"
	case store.JobSubmitted:
		return "scheduler.submitted"
	case store.JobCompleted:
		return "scheduler.completed"
	case store.TimeoutFired:
		return "machine.timeout"
	case store.HarnessError:
		return "harness.error"
	}
	return "event." + string(k)
}

func levelFor(k store.EventKind) string {
	switch k {
	case store.HarnessError, store.ValidationFailed, store.GuardRejected:
		return "ERROR"
	}
	return "INFO"
}

func die(what string, err error) {
	fmt.Fprintf(os.Stderr, "fromsession: %s: %v\n", what, err)
	os.Exit(1)
}
