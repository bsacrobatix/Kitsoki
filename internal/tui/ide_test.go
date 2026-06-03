package tui_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	tuipkg "kitsoki/internal/tui"
)

// TestIDEStatus_OffWhenDisconnected asserts /ide status reports the off state
// when no link is connected, and renders no footer chip.
func TestIDEStatus_OffWhenDisconnected(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))

	require.Empty(t, tuipkg.IDEFooterChipForTest(rm), "no chip when disconnected")

	rm = tuipkg.HandleIDESlashForTest(rm, []string{"status"})
	content := tuipkg.GetTranscriptContent(rm)
	require.Contains(t, content, "off", "status should report off when disconnected; got %q", content)
}

// TestIDEBare_DoesNotPanic is the regression guard for the bare `/ide` crash:
// with empty args the dispatcher took the `sub == ""` branch and sliced
// args[1:] on the empty slice, panicking "slice bounds out of range [1:0]".
// Bare /ide must instead route to connect (here: a disconnected fake whose
// discovery finds nothing, so it reports "no editor found").
func TestIDEBare_DoesNotPanic(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))

	// A closed fake is disconnected and its Candidates() returns nothing, so
	// the connect path is deterministic and hits no real lock files.
	fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1)
	_ = fake.Close()
	tuipkg.SetIDELinkForTest(&rm, fake)

	rm = tuipkg.HandleIDESlashForTest(rm, nil)
	require.Contains(t, tuipkg.GetTranscriptContent(rm), "no editor found",
		"bare /ide should route to connect, not panic")
}

// TestIDEStatus_ReportsConnectedDetails asserts /ide status surfaces the
// editor name, workspace, and port from the live link.
func TestIDEStatus_ReportsConnectedDetails(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))

	fake := tuipkg.NewFakeIDELink("VS Code", "/home/cloud-user/code/kitsoki", 25118)
	tuipkg.SetIDELinkForTest(&rm, fake)

	rm = tuipkg.HandleIDESlashForTest(rm, []string{"status"})
	content := tuipkg.GetTranscriptContent(rm)
	require.Contains(t, content, "VS Code")
	require.Contains(t, content, "/home/cloud-user/code/kitsoki")
	require.Contains(t, content, "25118")
}

// TestIDEDisconnect_DetachesAndFlipsChipOff asserts /ide disconnect closes the
// link, detaches it from the orchestrator, and the footer chip goes off.
func TestIDEDisconnect_DetachesAndFlipsChipOff(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))

	fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1234)
	tuipkg.SetIDELinkForTest(&rm, fake)
	require.NotEmpty(t, tuipkg.IDEFooterChipForTest(rm), "chip on while connected")

	rm = tuipkg.HandleIDESlashForTest(rm, []string{"disconnect"})
	require.Empty(t, tuipkg.IDEFooterChipForTest(rm), "chip off after disconnect")
	require.Contains(t, tuipkg.GetTranscriptContent(rm), "disconnected")
}

// TestIDEIndicator_RendersWithConcurrentLogging is the NON-NEGOTIABLE combined
// I/O test: it captures View() output while slog writes concurrently and the
// link toggles connected/disconnected, asserting the footer chip is correct and
// uncorrupted. Must fail without the footer element (no chip today).
func TestIDEIndicator_RendersWithConcurrentLogging(t *testing.T) {
	// Not parallel: rebinds slog default.
	orch, sid := setupCloak(t)
	rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))

	captured := tuipkg.CaptureSlog(t)
	defer captured.Restore()

	// Connected: the chip must render with the editor name and ✓.
	fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 25118)
	tuipkg.SetIDELinkForTest(&rm, fake)

	// Drive View() while logging concurrently, mirroring the real terminal
	// where slog output and the live transcript share stderr.
	var connectedView string
	helper := tuipkg.NewConcurrentIOTester(t, captured)
	helper.LogConcurrently(func() {
		for i := 0; i < 50; i++ {
			slog.Info("oracle.event", "turn", i)
		}
	}).RenderConcurrently(func() {
		for i := 0; i < 50; i++ {
			connectedView = rm.View()
		}
	})

	require.Contains(t, connectedView, "⧉ ide: VS Code ✓",
		"footer chip must render the connected indicator")
	// The chip glyph must never bleed into a slog line.
	captured.AssertNoMixedOutput("INFO", "⧉ ide:", "ide chip in log lines")

	// Disconnected: the chip disappears from the View().
	tuipkg.SetIDELinkForTest(&rm, nil)
	offView := rm.View()
	require.NotContains(t, offView, "⧉ ide:", "chip hidden when disconnected")
}

// TestSelectionEcho_AppearsOncePerTurn asserts exactly one
// `⧉ Selected N lines from <file>` line per turn carrying a selection, the
// matching ambient context is staged for the prompt, and nothing is echoed when
// disconnected or when the file is deny-ruled.
func TestSelectionEcho_AppearsOncePerTurn(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)

	const file = "/home/cloud-user/code/kitsoki/internal/foo/x.go"
	const sel = "line one\nline two\nline three"

	t.Run("connected with selection echoes once and stages args.ide", func(t *testing.T) {
		rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))
		fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1)
		fake.SetSelection(file, sel, map[string]any{
			"start": map[string]any{"line": float64(12), "character": float64(0)},
			"end":   map[string]any{"line": float64(14), "character": float64(8)},
		})
		tuipkg.SetIDELinkForTest(&rm, fake)

		rm = tuipkg.CaptureIDEAmbientForTest(rm)

		content := tuipkg.GetTranscriptContent(rm)
		echo := "⧉ Selected 3 lines from " + file
		require.Equal(t, 1, strings.Count(content, echo),
			"exactly one selection echo per turn; got %q", content)

		amb := tuipkg.PendingIDEAmbientForTest(rm)
		require.Equal(t, file, amb.File)
		require.Equal(t, sel, amb.Selection)
		require.Equal(t, 3, amb.Lines)
		require.Equal(t, "12:0-14:8", amb.Range)
	})

	t.Run("disconnected echoes nothing", func(t *testing.T) {
		rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))
		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.NotContains(t, tuipkg.GetTranscriptContent(rm), "⧉ Selected")
		require.Empty(t, tuipkg.PendingIDEAmbientForTest(rm).File)
	})

	t.Run("deny-ruled file attaches nothing", func(t *testing.T) {
		rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))
		fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1)
		fake.SetSelection("/secrets/creds.env", "TOKEN=abc", nil)
		tuipkg.SetIDELinkForTest(&rm, fake)
		tuipkg.SetIDEDenyForTest(&rm, []string{"*.env"})

		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.NotContains(t, tuipkg.GetTranscriptContent(rm), "⧉ Selected",
			"deny-ruled file must not echo")
		require.Empty(t, tuipkg.PendingIDEAmbientForTest(rm).File,
			"deny-ruled file must not stage ambient context")
	})

	t.Run("empty selection echoes nothing", func(t *testing.T) {
		rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))
		fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1)
		fake.SetSelection(file, "", nil)
		tuipkg.SetIDELinkForTest(&rm, fake)

		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.NotContains(t, tuipkg.GetTranscriptContent(rm), "⧉ Selected")
		require.Empty(t, tuipkg.PendingIDEAmbientForTest(rm).File)
	})
}

// TestSelectionEcho_InjectsOnlyOnChange asserts a selection feeds the turn (and
// echoes) only when it differs from the one that last rode a turn: a held
// selection must not silently re-shape every follow-up, a changed selection
// rides again, and a deselect resets the tracker so reselecting the same range
// counts as new.
func TestSelectionEcho_InjectsOnlyOnChange(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	const file = "/home/cloud-user/code/kitsoki/internal/foo/x.go"

	t.Run("unchanged selection rides only the first turn", func(t *testing.T) {
		rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))
		fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1)
		fake.SetSelection(file, "line one\nline two", nil)
		tuipkg.SetIDELinkForTest(&rm, fake)

		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.Equal(t, file, tuipkg.PendingIDEAmbientForTest(rm).File)
		require.Equal(t, 1, strings.Count(tuipkg.GetTranscriptContent(rm), "⧉ Selected"))

		// Same selection on the next turn — no inject, no second echo.
		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.Empty(t, tuipkg.PendingIDEAmbientForTest(rm).File,
			"unchanged selection must not ride a follow-up turn")
		require.Equal(t, 1, strings.Count(tuipkg.GetTranscriptContent(rm), "⧉ Selected"),
			"unchanged selection must not echo again")
	})

	t.Run("changed selection rides again", func(t *testing.T) {
		rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))
		fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1)
		fake.SetSelection(file, "first", nil)
		tuipkg.SetIDELinkForTest(&rm, fake)

		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.Equal(t, "first", tuipkg.PendingIDEAmbientForTest(rm).Selection)

		fake.SetSelection(file, "second different selection", nil)
		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.Equal(t, "second different selection", tuipkg.PendingIDEAmbientForTest(rm).Selection,
			"a changed selection must ride the turn")
		require.Equal(t, 2, strings.Count(tuipkg.GetTranscriptContent(rm), "⧉ Selected"))
	})

	t.Run("deselect then reselect same range rides again", func(t *testing.T) {
		rm, _ := tuipkg.ExtractRootModel(buildModel(t, orch, sid))
		fake := tuipkg.NewFakeIDELink("VS Code", "/ws", 1)
		fake.SetSelection(file, "same", nil)
		tuipkg.SetIDELinkForTest(&rm, fake)

		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.Equal(t, "same", tuipkg.PendingIDEAmbientForTest(rm).Selection)

		// Deselect: an empty selection resets the change-tracker.
		fake.SetSelection(file, "", nil)
		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.Empty(t, tuipkg.PendingIDEAmbientForTest(rm).File)

		// Reselect the identical range — counts as new again.
		fake.SetSelection(file, "same", nil)
		rm = tuipkg.CaptureIDEAmbientForTest(rm)
		require.Equal(t, "same", tuipkg.PendingIDEAmbientForTest(rm).Selection,
			"reselecting after a deselect must ride again")
	})
}
