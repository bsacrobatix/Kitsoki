// Package tui — bridge between orchestrator.SessionObserver and the
// Bubble Tea message loop.
//
// Background context: the orchestrator's per-session listener goroutine
// fires handleJobTerminal when a background job reaches a terminal
// state (done/failed/cancelled).  Before this bridge existed, that path
// committed a synthetic turn to the event log but had no way to notify
// the TUI; the main transcript stayed frozen until the user pressed a
// key (the inbox badge updated independently via a 2 s / 200 ms poller).
//
// The bridge here implements orchestrator.SessionObserver and forwards
// each OnBackgroundTurn into the program's tea.Msg channel via
// tea.Program.Send.  Send is the only documented cross-goroutine entry
// point on *tea.Program (Bubbletea v1.3.10, tea.go:774 — non-blocking
// when the program has stopped).  The TUI's existing turnOutcomeMsg
// handler then drives the re-render — no new render path is added.
//
// Lifecycle: AttachOrchestratorObserver registers an observer that
// holds a *tea.Program reference and a session-ID filter.  The observer
// remains registered until DetachOrchestratorObserver is called (or the
// orchestrator itself is GC'd) — typically the caller defers Detach
// alongside p.Run().

package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"kitsoki/internal/app"
	"kitsoki/internal/orchestrator"
)

// orchObserver is the SessionObserver impl that forwards background-
// turn notifications into a *tea.Program.
//
// It filters by sid because in theory a single orchestrator can host
// multiple sessions (e.g. headless tests share one across many).  In
// the real TUI today there is one program per session, but the filter
// is cheap and removes a footgun.
type orchObserver struct {
	prog *tea.Program
	sid  app.SessionID
}

// OnBackgroundTurn implements orchestrator.SessionObserver.  It wraps
// the outcome into a turnOutcomeMsg with a synthetic input label
// ("(background)") so the transcript pane distinguishes background
// re-renders from user-typed turns in its scroll-back.
//
// Errors are never set here: handleJobTerminal already logged any
// failure path before reaching the notification step, and an outcome
// of nil short-circuits inside notifyBackgroundTurn.
func (o *orchObserver) OnBackgroundTurn(sid app.SessionID, outcome *orchestrator.TurnOutcome) {
	if o.prog == nil || outcome == nil {
		return
	}
	if sid != o.sid {
		return
	}
	o.prog.Send(turnOutcomeMsg{
		outcome: outcome,
		input:   "(background)",
		err:     nil,
	})
}

// AttachOrchestratorObserver wires a SessionObserver into orch that
// pushes background-turn outcomes back into prog's tea.Msg loop for
// sid.  Returns a detach function — the caller typically defers it
// alongside the program's lifetime:
//
//	prog := tea.NewProgram(rootModel, …)
//	detach := tui.AttachOrchestratorObserver(orch, prog, sid)
//	defer detach()
//	_, err = prog.Run()
//
// Safe to call from any goroutine.  Passing a nil orch or nil prog is
// a no-op (and the returned detach func is a no-op).
func AttachOrchestratorObserver(orch *orchestrator.Orchestrator, prog *tea.Program, sid app.SessionID) func() {
	if orch == nil || prog == nil {
		return func() {}
	}
	obs := &orchObserver{prog: prog, sid: sid}
	orch.RegisterObserver(obs)
	return func() { orch.UnregisterObserver(obs) }
}
