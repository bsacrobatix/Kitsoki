package orchestrator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/chats"
	"kitsoki/internal/journal"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestAttachSession_SurfacesPendingDrives verifies the resume bundle picks up
// pending chat_input_queue rows owned by the session — the continue × claude-
// code-sessions integration surface. A drive submitted by the previous run
// must show up as PendingDrives on resume so the TUI can re-dispatch it
// (instead of silently letting it linger).
func TestAttachSession_SurfacesPendingDrives(t *testing.T) {
	def, err := app.Load("../../testdata/apps/cloak/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	jw, err := journal.NewSQLiteWriter(s.DB())
	require.NoError(t, err)
	jr, err := journal.NewSQLiteReader(s.DB())
	require.NoError(t, err)

	chatStore, err := chats.NewStore(s.DB(), chats.WithJournalWriter(jw))
	require.NoError(t, err)

	orch := orchestrator.New(def, m, s, nil,
		orchestrator.WithJournalWriter(jw),
		orchestrator.WithJournalReader(jr),
		orchestrator.WithChatsConcrete(chatStore),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	// Create a chat, then enqueue a drive against it carrying our
	// origin_session_id. PendingDrives is keyed on origin_session_id (queue
	// column), not on chats.session_id — that's the BackgroundedChats
	// pathway. So we don't need to back-set chats.session_id here.
	ch, err := chatStore.Create(ctx, def.App.ID, "test-room", string(sid), "test chat")
	require.NoError(t, err)

	_, err = chatStore.Enqueue(ctx, chats.EnqueueOptions{
		ChatID:          ch.ID,
		Transport:       chats.DriveTransportStateMachine,
		Actor:           "test-actor",
		Payload:         "test drive payload",
		OriginSessionID: string(sid),
	})
	require.NoError(t, err)

	// Simulate restart by building a fresh orchestrator over the same store.
	m2, err := machine.New(def)
	require.NoError(t, err)
	orch2 := orchestrator.New(def, m2, s, nil,
		orchestrator.WithJournalReader(jr),
		orchestrator.WithChatsConcrete(chatStore),
	)

	bundle, err := orch2.AttachSession(sid)
	require.NoError(t, err)
	require.NotNil(t, bundle)

	require.Len(t, bundle.PendingDrives, 1,
		"resume must surface one pending drive — the one we enqueued before restart")
	d := bundle.PendingDrives[0]
	require.Equal(t, ch.ID, d.ChatID)
	require.Equal(t, chats.DriveStatusPending, d.Status)
	require.Equal(t, "test drive payload", d.Payload)
}

// TestAttachSession_NoChatsStore_NoSurfacing verifies the graceful degradation
// when no concrete chats.Store is wired — bundle returns successfully with
// empty PendingDrives / BackgroundedChats slices (rather than nil-dereffing).
func TestAttachSession_NoChatsStore_NoSurfacing(t *testing.T) {
	def, err := app.Load("../../testdata/apps/cloak/app.yaml")
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Construct WITHOUT WithChatsConcrete.
	orch := orchestrator.New(def, m, s, nil)
	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	bundle, err := orch.AttachSession(sid)
	require.NoError(t, err)
	require.Empty(t, bundle.PendingDrives, "no PendingDrives without chats.Store")
	require.Empty(t, bundle.BackgroundedChats, "no BackgroundedChats without chats.Store")
}
