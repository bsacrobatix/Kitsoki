package tui_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	tuipkg "kitsoki/internal/tui"
)

// TestWelcomeBlockPrintsAtStartup confirms the welcome banner is
// queued onto the transcript for the first FlushPending. The chat
// view contract (post-scrollback refactor): users see a Claude-Code-
// style intro that scrolls off as they take turns.
func TestWelcomeBlockPrintsAtStartup(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)

	// The welcome lives in m.transcript.pending until the first
	// Update flushes it. AllContent doesn't see pending (renders
	// from entries only), so we ask the model for its pending dump
	// via a test seam.
	rm, _ := tuipkg.ExtractRootModel(m)
	pending := tuipkg.PendingTranscriptForTest(rm)
	joined := strings.Join(pending, "\n")
	require.Contains(t, joined, "kitsoki",
		"welcome block should advertise the kitsoki name; got %q", joined)
	require.Contains(t, joined, "/help",
		"welcome block should hint at /help")
	require.Contains(t, joined, "session",
		"welcome block should show session/state status")
}
