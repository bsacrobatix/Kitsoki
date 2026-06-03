package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"kitsoki/internal/app"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/tui/blocks"
)

// welcome.go — Claude-Code-style startup banner. Printed once at
// session-start into the terminal's scrollback (via tea.Println on
// the first FlushPending). Scrolls off naturally as content grows.
//
// Content shape: app + story title on the first line; subtitle with
// version/author; a couple of hint lines pointing at /help, /world,
// and /meta; a status footer with session id and starting state.

// buildWelcome returns the rendered welcome block (lipgloss-styled
// rounded box) or "" when there's nothing to advertise. The width is
// the terminal's column count at startup; the box auto-fits.
func buildWelcome(orch *orchestrator.Orchestrator, sid app.SessionID, appPath, themeName string, width int) string {
	def := orch.AppDef()
	if def == nil {
		return ""
	}
	r := blocks.New(width, themeName)
	w := blocks.Welcome{
		Title:    welcomeTitle(def, appPath),
		Subtitle: welcomeSubtitle(def),
		Hints: []string{
			"/help        list commands",
			"/world       inspect current state",
			"/meta [name] enter a meta-mode room (parallel transcript)",
			"/quit        exit",
		},
		Status: welcomeStatus(orch, sid),
	}
	return r.WelcomeBlock(w)
}

func welcomeTitle(def *app.AppDef, appPath string) string {
	parts := []string{"kitsoki"}
	if def.App.Title != "" {
		parts = append(parts, def.App.Title)
	} else if def.App.ID != "" {
		parts = append(parts, def.App.ID)
	} else if appPath != "" {
		parts = append(parts, strings.TrimSuffix(filepath.Base(appPath), ".yaml"))
	}
	return strings.Join(parts, " · ")
}

func welcomeSubtitle(def *app.AppDef) string {
	var bits []string
	if def.App.Version != "" {
		bits = append(bits, "v"+def.App.Version)
	}
	if def.App.Author != "" {
		bits = append(bits, "by "+def.App.Author)
	}
	return strings.Join(bits, " · ")
}

func welcomeStatus(orch *orchestrator.Orchestrator, sid app.SessionID) string {
	state := orch.InitialState()
	id := string(sid)
	if len(id) > 8 {
		id = id[:8] + "…"
	}
	return fmt.Sprintf("session %s · state %s", id, state)
}
