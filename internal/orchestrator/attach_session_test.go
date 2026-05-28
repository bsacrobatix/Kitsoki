package orchestrator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/journal"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestAttachSession verifies the end-to-end resume read path:
//   - Create a session with the first orchestrator instance (simulating a run).
//   - Run a few turns via SubmitDirect.
//   - Construct a *new* Orchestrator pointing at the same store (simulating a
//     process restart) with a journal reader wired in.
//   - Call AttachSession and assert the returned ResumeBundle has:
//     (a) the correct state from the original run,
//     (b) at least one view.rendered TranscriptEntry,
//     (c) a non-empty InitialView string.
func TestAttachSession(t *testing.T) {
	def, err := app.Load("../../testdata/apps/cloak/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Build journal writer and reader sharing the same *sql.DB.
	jw, err := journal.NewSQLiteWriter(s.DB())
	require.NoError(t, err)

	jr, err := journal.NewSQLiteReader(s.DB())
	require.NoError(t, err)

	// ── First orchestrator instance (the original session) ─────────────────────
	orch1 := orchestrator.New(def, m, s, nil,
		orchestrator.WithJournalWriter(jw),
		orchestrator.WithJournalReader(jr),
	)

	ctx := context.Background()

	sid, err := orch1.NewSession(ctx)
	require.NoError(t, err)

	// Run two turns: foyer → go south (bar.dark) → go north (foyer).
	out1, err := orch1.SubmitDirect(ctx, sid, "go", map[string]any{"direction": "south"})
	require.NoError(t, err)
	require.Equal(t, app.StatePath("bar.dark"), out1.NewState)
	require.NotEmpty(t, out1.View, "view should be rendered for transcript test")

	out2, err := orch1.SubmitDirect(ctx, sid, "go", map[string]any{"direction": "north"})
	require.NoError(t, err)
	require.Equal(t, app.StatePath("foyer"), out2.NewState)

	// ── Second orchestrator instance (simulates process restart) ──────────────
	// Use the same store + journal reader/writer, but build a fresh Orchestrator.
	m2, err := machine.New(def)
	require.NoError(t, err)

	orch2 := orchestrator.New(def, m2, s, nil,
		orchestrator.WithJournalWriter(jw),
		orchestrator.WithJournalReader(jr),
	)

	// ── Call AttachSession ─────────────────────────────────────────────────────
	bundle, err := orch2.AttachSession(sid)
	require.NoError(t, err)
	require.NotNil(t, bundle)

	// (a) State must match what the original session ended on.
	require.Equal(t, app.StatePath("foyer"), bundle.Journey.State,
		"resumed state should match last persisted state")

	// Turn count should reflect the turns we ran (both SubmitDirect turns).
	require.GreaterOrEqual(t, int(bundle.Journey.Turn), 2,
		"turn count should be at least 2 after two SubmitDirect calls")

	// (b) At least one view.rendered TranscriptEntry should be present.
	viewRenderedCount := 0
	for _, e := range bundle.TranscriptEntries {
		if e.Kind == journal.KindViewRendered {
			viewRenderedCount++
		}
	}
	require.Greater(t, viewRenderedCount, 0,
		"expected at least one view.rendered transcript entry")

	// (c) InitialView must be non-empty (the last rendered view text).
	require.NotEmpty(t, bundle.InitialView, "InitialView should be populated from view.rendered")

	// (d) No pending clarify should be outstanding (we ran full transitions).
	require.Nil(t, bundle.PendingClarify, "no pending clarify expected after clean transitions")
}

// TestAttachSessionNilReader verifies the graceful fallback when no journal
// reader is wired: AttachSession returns a valid bundle from events only
// (no transcript entries, no pending clarify).
func TestAttachSessionNilReader(t *testing.T) {
	def, err := app.Load("../../testdata/apps/cloak/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// No journal reader wired.
	orch := orchestrator.New(def, m, s, nil)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "go", map[string]any{"direction": "south"})
	require.NoError(t, err)
	require.Equal(t, app.StatePath("bar.dark"), out.NewState)

	// AttachSession without a reader should still succeed (falls back to LoadJourney).
	bundle, err := orch.AttachSession(sid)
	require.NoError(t, err)
	require.NotNil(t, bundle)
	require.Equal(t, app.StatePath("bar.dark"), bundle.Journey.State)
	require.Empty(t, bundle.TranscriptEntries, "no transcript entries without journal reader")
	require.Empty(t, bundle.InitialView, "no InitialView without journal reader")
	require.Nil(t, bundle.PendingClarify)
}
