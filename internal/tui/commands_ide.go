package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"kitsoki/internal/host"
	"kitsoki/internal/ide"
	"kitsoki/internal/tui/blocks"
)

// commands_ide.go — the `/ide` slash command: connect/disconnect the live
// MCP-over-ws link to the editor and report status. The link substrate is
// internal/ide (slice 1); this file is slice 2's operator surface. Connect
// dials asynchronously (a ws handshake) and reports back via ideConnectDoneMsg;
// disconnect and status are synchronous. Once connected the same *ide.Link is
// pushed onto the orchestrator (SetIDELink) so per-turn host.ide.* dispatch
// resolves it, and the footer chip + ambient-selection capture light up. See
// docs/tui/README.md ("Editor awareness: /ide").

// ideLinkHandle is the slice-2 view of the IDE link: the host.IDELink subset
// the orchestrator/footer/capture read, plus the lifecycle methods the `/ide`
// command drives. *ide.Link is the production implementation; tests inject an
// in-memory fake so the footer chip and ambient-capture paths run without a
// real ws socket. Keeping it an interface (rather than the concrete *ide.Link)
// is the seam that makes those paths fast-testable.
type ideLinkHandle interface {
	host.IDELink
	Candidates(ctx context.Context) ([]ide.Lock, error)
	ConnectLock(ctx context.Context, lock ide.Lock) (ide.LinkInfo, error)
	Close() error
}

// ideConnectDoneMsg carries the result of an async `/ide connect` dial back to
// Update. link is the freshly-dialed handle on success (nil on failure); err
// distinguishes "no editor found" (ide.ErrNoIDE) from a dial failure so the
// transcript can phrase it precisely.
type ideConnectDoneMsg struct {
	link ideLinkHandle
	info ide.LinkInfo
	err  error
}

// handleIDESlash dispatches the `/ide [subcommand]` family. Bare `/ide`
// connects when off and shows status when already connected (the convenience
// alias). connect/disconnect/status are explicit.
func (m RootModel) handleIDESlash(args []string) (tea.Model, tea.Cmd) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "":
		// Bare /ide: connect if off, else show status. There is no args[0]
		// here (sub == "" only when args is empty), so pass no connect args —
		// slicing args[1:] on the empty slice would panic ([1:0]).
		if m.ideConnected() {
			m.transcript.AppendBlock(m.renderIDEStatusBlock())
			return m, nil
		}
		return m.ideConnect(nil)
	case "connect":
		return m.ideConnect(args[1:])
	case "disconnect":
		return m.ideDisconnect()
	case "status":
		m.transcript.AppendBlock(m.renderIDEStatusBlock())
		return m, nil
	default:
		m.transcript.AppendBlock(m.ideBlock(
			fmt.Sprintf("unknown subcommand %q — try /ide [connect|disconnect|status]", sub)))
		return m, nil
	}
}

// ideConnect discovers candidate lock files and dials. When exactly one matches
// (or an arg selects one) it dials asynchronously; when several match and none
// is selected it prints a picker so the operator can re-run `/ide connect <n>`.
func (m RootModel) ideConnect(args []string) (tea.Model, tea.Cmd) {
	if m.ideConnected() {
		m.transcript.AppendBlock(m.ideBlock(
			fmt.Sprintf("already connected to %s", m.ideLink.IDEName())))
		return m, nil
	}

	cwd, _ := os.Getwd()
	link := m.ideLink
	if link == nil {
		link = ide.NewLink(cwd, nil)
	}
	m.ideLink = link

	candidates, err := link.Candidates(context.Background())
	if err != nil {
		m.transcript.AppendBlock(m.ideBlock(fmt.Sprintf("discovery failed: %v", err)))
		return m, nil
	}
	if len(candidates) == 0 {
		m.transcript.AppendBlock(m.ideBlock("no editor found — open this workspace in VS Code (or a fork) and retry"))
		return m, nil
	}

	// Picker: when several lock files match and the operator did not pick
	// one, show the numbered list and dial nothing yet.
	if len(candidates) > 1 {
		if len(args) == 0 {
			m.transcript.AppendBlock(m.renderIDEPickerBlock(candidates))
			return m, nil
		}
		idx, perr := parsePickIndex(args[0], len(candidates))
		if perr != nil {
			m.transcript.AppendBlock(m.ideBlock(perr.Error()))
			return m, nil
		}
		return m.ideDialAsync(link, candidates[idx])
	}

	// Exactly one candidate — dial it.
	return m.ideDialAsync(link, candidates[0])
}

// ideDialAsync stores the link on the model (so a redial reuses it) and returns
// a tea.Cmd that dials off the UI goroutine, reporting back via
// ideConnectDoneMsg. The handshake is a real ws round-trip; doing it inline
// would block the render loop.
func (m RootModel) ideDialAsync(link ideLinkHandle, lock ide.Lock) (tea.Model, tea.Cmd) {
	m.ideLink = link
	m.transcript.AppendBlock(m.ideBlock(
		fmt.Sprintf("connecting to %s (port %d)…", displayIDEName(lock.IDEName), lock.Port)))
	cmd := func() tea.Msg {
		info, err := link.ConnectLock(context.Background(), lock)
		return ideConnectDoneMsg{link: link, info: info, err: err}
	}
	return m, cmd
}

// handleIDEConnectDone finalizes an async dial: on success it pushes the link
// onto the orchestrator (so host.ide.* dispatch and the ide.connected world key
// resolve it) and prints the connected status block; on failure it reports the
// error and drops the half-open link.
func (m RootModel) handleIDEConnectDone(msg ideConnectDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// A failed dial leaves no usable link; drop it so the footer stays off.
		if m.ideLink != nil {
			_ = m.ideLink.Close()
			m.ideLink = nil
		}
		m.orch.SetIDELink(nil)
		if errors.Is(msg.err, ide.ErrNoIDE) {
			m.transcript.AppendBlock(m.ideBlock("no editor found — open this workspace in VS Code (or a fork) and retry"))
		} else {
			m.transcript.AppendBlock(m.ideBlock(fmt.Sprintf("connect failed: %v", msg.err)))
		}
		return m, nil
	}
	m.ideLink = msg.link
	// Push the live link onto the orchestrator so dispatchHostCalls injects
	// it (host.WithIDELink) and the inner-claude env scrub engages.
	m.orch.SetIDELink(msg.link)
	m.transcript.AppendBlock(m.renderIDEStatusBlock())
	return m, nil
}

// ideDisconnect closes the link and detaches it from the orchestrator, which
// restores the normal oracle subprocess env (the scrub is gated on a connected
// link) and flips the footer chip off.
func (m RootModel) ideDisconnect() (tea.Model, tea.Cmd) {
	if !m.ideConnected() {
		m.transcript.AppendBlock(m.ideBlock("not connected"))
		return m, nil
	}
	name := m.ideLink.IDEName()
	_ = m.ideLink.Close()
	m.ideLink = nil
	// Detach from the orchestrator: IDELinkFromContext(ctx) goes nil, so the
	// env scrub stops and host.ide.* return the not-connected result again.
	m.orch.SetIDELink(nil)
	m.transcript.AppendBlock(m.ideBlock(fmt.Sprintf("disconnected from %s", displayIDEName(name))))
	return m, nil
}

// ideConnected reports whether a live link is held.
func (m RootModel) ideConnected() bool {
	return m.ideLink != nil && m.ideLink.Connected()
}

// captureIDEAmbient reads the active editor selection at turn-submit and, when
// present and not deny-ruled, stashes it on the model (pendingIDEAmbient) for
// injection onto the turn ctx and appends exactly one
// `⧉ Selected N lines from <file>` echo via the settled-line path. It is a
// no-op (clears the pending value, emits nothing) when the link is off, the
// selection is empty, or the active file matches the deny list — so a turn with
// no usable editor context behaves exactly as before. Returns the updated
// model.
//
// The selection is read through the slice-1 host.ide.get_selection handler with
// the link injected, so kitsoki has one selection-parsing path. The handler
// returns the typed not-connected/empty result rather than an error, so this
// never fails a turn.
func (m RootModel) captureIDEAmbient() RootModel {
	m.pendingIDEAmbient = host.IDEAmbient{}
	if !m.ideConnected() {
		return m
	}

	ctx := host.WithIDELink(context.Background(), m.ideLink)
	res, err := host.IDEGetSelectionHandler(ctx, nil)
	if err != nil || res.Data == nil {
		return m
	}
	if connected, _ := res.Data["connected"].(bool); !connected {
		return m
	}
	file, _ := res.Data["file"].(string)
	text, _ := res.Data["text"].(string)
	if strings.TrimSpace(file) == "" || text == "" {
		// No active selection — nothing rides the turn, no echo.
		return m
	}
	if m.ideFileDenied(file) {
		// Deny-ruled file: attach nothing, echo nothing (parity with
		// Claude Code's Read deny-rule suppression).
		return m
	}

	lines := selectionLineCount(text)
	m.pendingIDEAmbient = host.IDEAmbient{
		File:      file,
		Selection: text,
		Lines:     lines,
		Range:     ideRangeLabel(res.Data["range"]),
	}

	// Exactly one settled line per turn carrying a selection. Rendered
	// through the inline-routing settled-line path as a clean system line
	// (ideSelectionEcho) so it reads `⧉ Selected N lines from <file>` with
	// no routing decoration — the echo is the operator's source of truth
	// for what rode the turn.
	ir := m.newInlineRouter()
	echo := fmt.Sprintf("⧉ Selected %d %s from %s", lines, pluralLines(lines), file)
	m.transcript.AppendBlock(ir.ideSelectionEcho(echo))
	return m
}

// ideFileDenied reports whether path matches any deny-list pattern. Each
// pattern is tried as a filepath.Match glob against both the full (cleaned)
// path and its base name, so "*.env" and "/abs/secrets/*" both deny as
// expected. A malformed pattern is treated as non-matching (filepath.Match
// returns an error only on bad syntax, which we ignore rather than denying
// everything).
func (m RootModel) ideFileDenied(path string) bool {
	if len(m.ideDeny) == 0 {
		return false
	}
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	for _, pat := range m.ideDeny {
		if ok, err := filepath.Match(pat, clean); err == nil && ok {
			return true
		}
		if ok, err := filepath.Match(pat, base); err == nil && ok {
			return true
		}
	}
	return false
}

// selectionLineCount counts the lines a selection spans. An empty trailing
// newline does not add a phantom line; a non-empty single line counts as 1.
func selectionLineCount(text string) int {
	if text == "" {
		return 0
	}
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}

// pluralLines returns "line" or "lines" for the echo's grammar.
func pluralLines(n int) string {
	if n == 1 {
		return "line"
	}
	return "lines"
}

// ideRangeLabel renders the editor selection range (a map[string]any with
// start/end {line,character}) as a compact "L:C-L:C" string for the ambient
// scope's `range` field. Returns "" when the range is absent or unparseable —
// the echo's line count is the authoritative source of truth, the range is a
// best-effort convenience for prompts.
func ideRangeLabel(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	start := positionLabel(m["start"])
	end := positionLabel(m["end"])
	switch {
	case start != "" && end != "":
		return start + "-" + end
	case start != "":
		return start
	default:
		return ""
	}
}

// positionLabel renders one {line,character} position as "line:character".
func positionLabel(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	line, lok := numField(m["line"])
	ch, _ := numField(m["character"])
	if !lok {
		return ""
	}
	return fmt.Sprintf("%d:%d", line, ch)
}

// numField coerces a JSON number (float64 after json.Unmarshal) or int to int.
func numField(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

// ideBlock renders a one-line `/ide` chat block via the blocks renderer
// (SlashOutput) — no hand-rolled ANSI.
func (m RootModel) ideBlock(line string) string {
	return blocks.New(m.transcript.width, m.currentTheme()).SlashOutput("ide: " + line)
}

// renderIDEStatusBlock renders the read-only `/ide status` block: connected?,
// ideName, workspace, port.
func (m RootModel) renderIDEStatusBlock() string {
	r := blocks.New(m.transcript.width, m.currentTheme())
	if !m.ideConnected() {
		return r.SlashOutput("ide: off (no editor connected) — /ide connect to attach")
	}
	var sb strings.Builder
	sb.WriteString(r.SlashOutput("ide: connected ✓"))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("    editor:    %s\n", displayIDEName(m.ideLink.IDEName())))
	sb.WriteString(fmt.Sprintf("    workspace: %s\n", emptyDash(m.ideLink.Workspace())))
	sb.WriteString(fmt.Sprintf("    port:      %d", m.ideLink.Port()))
	return sb.String()
}

// renderIDEPickerBlock lists the candidate lock files so the operator can
// re-run `/ide connect <n>`. Best-first order is preserved from Discover.
func (m RootModel) renderIDEPickerBlock(candidates []ide.Lock) string {
	r := blocks.New(m.transcript.width, m.currentTheme())
	var sb strings.Builder
	sb.WriteString(r.SlashOutput("ide: several editors match this workspace — pick one with /ide connect <n>"))
	sb.WriteString("\n")
	for i, c := range candidates {
		ws := ""
		if len(c.WorkspaceFolders) > 0 {
			ws = c.WorkspaceFolders[0]
		}
		sb.WriteString(fmt.Sprintf("    %d) %s · port %d · %s\n",
			i, displayIDEName(c.IDEName), c.Port, emptyDash(ws)))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// parsePickIndex parses a picker selection, validating it against the candidate
// count. The selection is 0-indexed to match the printed list.
func parsePickIndex(s string, n int) (int, error) {
	var idx int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &idx); err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	if idx < 0 || idx >= n {
		return 0, fmt.Errorf("pick 0–%d", n-1)
	}
	return idx, nil
}

// displayIDEName falls back to a generic label when the lock omits ideName so
// the status/echo lines never render an empty editor name.
func displayIDEName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "editor"
	}
	return name
}

// emptyDash renders an em-dash for an empty string so status rows never show a
// blank value.
func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
