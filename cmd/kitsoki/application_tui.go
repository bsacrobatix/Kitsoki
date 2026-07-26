package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/tui"
)

type tuiApplicationSource struct {
	orch *orchestrator.Orchestrator
	sid  app.SessionID
}

func (s tuiApplicationSource) Snapshot() (runstatus.Snapshot, error) {
	journey, err := s.orch.LoadJourney(s.sid)
	if err != nil {
		return runstatus.Snapshot{}, fmt.Errorf("application TUI: load journey: %w", err)
	}
	return runstatus.Snapshot{
		Session: runstatus.SessionHeader{
			SessionID:    string(s.sid),
			AppID:        s.orch.AppDef().App.ID,
			CurrentState: string(journey.State),
			Turn:         int(journey.Turn),
		},
		App: s.orch.AppDef(),
	}, nil
}

func (s tuiApplicationSource) Events() ([]runstatus.TraceEvent, error) {
	return nil, nil
}

func (s tuiApplicationSource) AppDef() *app.AppDef {
	return s.orch.AppDef()
}

func newTUIApplicationActionDispatcher(
	orch *orchestrator.Orchestrator,
	sid app.SessionID,
	tracePath string,
	actor string,
) (tui.ApplicationActionDispatcher, error) {
	driver := server.OrchestratorDriver{Orch: orch, SID: sid}
	journalPath := ""
	if tracePath != "" {
		journalPath = strings.TrimSuffix(tracePath, filepath.Ext(tracePath)) + ".application.jsonl"
	}
	service, err := server.NewSessionApplicationService(
		server.Entry{Source: tuiApplicationSource{orch: orch, sid: sid}, Driver: driver},
		"",
		server.ApplicationRuntime{JournalPath: journalPath},
	)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, envelope appplatform.ActionEnvelope) (tui.ApplicationActionResult, error) {
		if envelope.Actor == "" {
			envelope.Actor = actor
		}
		outcome, err := service.DispatchAction(ctx, appplatform.TransportTUI, envelope)
		if err != nil {
			return tui.ApplicationActionResult{Outcome: outcome}, err
		}
		view, err := driver.View(ctx)
		if err != nil {
			return tui.ApplicationActionResult{Outcome: outcome}, fmt.Errorf("application TUI: load settled view: %w", err)
		}
		return tui.ApplicationActionResult{Outcome: outcome, View: view}, nil
	}, nil
}

func tuiApplicationActor() string {
	if actor := gitOutput("config", "user.name"); actor != "" {
		return actor
	}
	return strings.TrimSpace(os.Getenv("USER"))
}
