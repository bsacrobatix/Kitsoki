package runstatus

import (
	"encoding/json"
	"strings"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
	"kitsoki/internal/viz"
)

// FromHistory converts a real store.History into a Snapshot suitable for the
// runstatus UI. Used by both the fromsession exporter (real SQLite-backed
// sessions) and the fromflow exporter (in-memory store from a flow run), so
// the two paths emit identical event shapes.
//
// sessionID is the value to copy into Snapshot.Session.SessionID — the caller
// supplies it since History rows don't carry it.
//
// Every store.Event maps 1:1 to a TraceEvent; no synthesis or back-fill is
// performed. Oracle events (OracleCalled, OracleReturned, OracleError) are
// already written inline into the history by the orchestrator (wave 3-oracle)
// and appear in Events verbatim.
func FromHistory(hist store.History, def *app.AppDef, sessionID string) (Snapshot, error) {
	var (
		currentState string
		lastTurn     int
		terminal     bool
		started      time.Time
	)

	events := make([]TraceEvent, 0, len(hist))
	rawLines := make([][]byte, 0, len(hist))
	for _, ev := range hist {
		if started.IsZero() {
			started = ev.Ts
		}

		// Decode payload into attrs.
		var attrs map[string]any
		if len(ev.Payload) > 0 {
			_ = json.Unmarshal(ev.Payload, &attrs)
		}

		// Track current state for SessionHeader.
		if ev.Kind == store.StateEntered {
			if sp, ok := attrs["state"].(string); ok {
				currentState = sp
			}
		}
		if string(ev.StatePath) != "" {
			currentState = string(ev.StatePath)
		}

		if int(ev.Turn) > lastTurn {
			lastTurn = int(ev.Turn)
		}

		// call_id lives on the Event directly; merge into attrs so the SPA sees it.
		if ev.CallID != "" {
			if attrs == nil {
				attrs = make(map[string]any)
			}
			attrs["call_id"] = ev.CallID
		}

		level := "INFO"
		switch ev.Kind {
		case store.HarnessError, store.ValidationFailed, store.GuardRejected:
			level = "ERROR"
		}

		events = append(events, TraceEvent{
			Time:       ev.Ts,
			Level:      level,
			Msg:        string(ev.Kind),
			Turn:       int(ev.Turn),
			StatePath:  string(ev.StatePath),
			ParentTurn: int(ev.ParentTurn),
			Attrs:      attrs,
		})

		// Populate RawLines for Layer 7 byte-equality assertions (finding 2.5).
		// MarshalEventLine produces the same bytes as JSONLSink.Append writes for
		// the same event, so joinLines(snap.RawLines) == original JSONL event section.
		if raw, merr := store.MarshalEventLine(ev); merr == nil {
			rawLines = append(rawLines, raw)
		} else {
			rawLines = append(rawLines, nil) // gap marker; test can detect
		}
	}

	if currentState != "" {
		if st, ok := app.Compile(def).LookupState(app.StatePath(strings.ReplaceAll(currentState, "/", "."))); ok && st != nil && st.Terminal {
			terminal = true
		}
	}

	fc, err := viz.FlowchartWithMap(def, viz.FlowchartOptions{Detail: viz.DetailStates})
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		Session: SessionHeader{
			SessionID:    sessionID,
			AppID:        def.App.ID,
			CurrentState: currentState,
			Turn:         lastTurn,
			StartedAt:    started,
			Terminal:     terminal,
		},
		App: def,
		Mermaid: MermaidSnapshot{
			Source:  fc.Source,
			NodeMap: fc.NodeMap,
		},
		Events:   events,
		RawLines: rawLines,
	}, nil
}
