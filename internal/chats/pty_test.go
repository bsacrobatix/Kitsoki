package chats_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"kitsoki/internal/chats"
)

func TestPTY_AttachInsertsRow(t *testing.T) {
	cs, fake := openTestStore(t)
	ctx := context.Background()

	c, err := cs.Create(ctx, "app1", "live_coding", "PROJ-1", "title")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	fake.Advance(time.Second)
	p, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:         c.ID,
		TmuxSession:    "kitsoki-chat-" + c.ID,
		PermissionMode: "default",
		WorkspacePath:  "/work/proj-1",
	})
	if err != nil {
		t.Fatalf("AttachPTY: %v", err)
	}
	if p.Mode != chats.PtyModeAttached {
		t.Errorf("mode = %q, want %q", p.Mode, chats.PtyModeAttached)
	}
	if p.PermissionMode != "default" {
		t.Errorf("permission_mode = %q, want default", p.PermissionMode)
	}
	if p.LastIdleAt != nil {
		t.Errorf("last_idle_at should be nil on fresh row, got %v", *p.LastIdleAt)
	}
	host, _ := os.Hostname()
	if p.TmuxHost != host {
		t.Errorf("tmux_host = %q, want this host %q", p.TmuxHost, host)
	}
}

func TestPTY_AttachThenDetachFlipsMode(t *testing.T) {
	cs, fake := openTestStore(t)
	ctx := context.Background()
	c, _ := cs.Create(ctx, "app1", "live", "", "t")

	fake.Advance(time.Second)
	if _, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:      c.ID,
		TmuxSession: "kitsoki-chat-" + c.ID,
	}); err != nil {
		t.Fatalf("AttachPTY: %v", err)
	}

	fake.Advance(time.Second)
	p, err := cs.DetachPTY(ctx, c.ID)
	if err != nil {
		t.Fatalf("DetachPTY: %v", err)
	}
	if p.Mode != chats.PtyModeBackground {
		t.Errorf("mode after detach = %q, want %q", p.Mode, chats.PtyModeBackground)
	}
	if !p.UpdatedAt.After(p.CreatedAt) {
		t.Errorf("updated_at not bumped: created=%v updated=%v", p.CreatedAt, p.UpdatedAt)
	}
}

func TestPTY_ReattachFlipsBackgroundToAttached(t *testing.T) {
	cs, fake := openTestStore(t)
	ctx := context.Background()
	c, _ := cs.Create(ctx, "app1", "live", "", "t")
	if _, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:      c.ID,
		TmuxSession: "kitsoki-chat-" + c.ID,
	}); err != nil {
		t.Fatalf("AttachPTY: %v", err)
	}
	if _, err := cs.DetachPTY(ctx, c.ID); err != nil {
		t.Fatalf("DetachPTY: %v", err)
	}

	fake.Advance(time.Second)
	p, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:         c.ID,
		TmuxSession:    "kitsoki-chat-" + c.ID, // same tmux still alive
		PermissionMode: "bypassPermissions",
		WorkspacePath:  "/work",
	})
	if err != nil {
		t.Fatalf("re-AttachPTY: %v", err)
	}
	if p.Mode != chats.PtyModeAttached {
		t.Errorf("mode after re-attach = %q, want %q", p.Mode, chats.PtyModeAttached)
	}
	if p.PermissionMode != "bypassPermissions" {
		t.Errorf("permission_mode not refreshed on re-attach: got %q", p.PermissionMode)
	}
}

func TestPTY_DetachNoRowReturnsErrNoPTYSession(t *testing.T) {
	cs, _ := openTestStore(t)
	ctx := context.Background()
	_, err := cs.DetachPTY(ctx, "NOPE")
	if !errors.Is(err, chats.ErrNoPTYSession) {
		t.Errorf("expected ErrNoPTYSession, got %v", err)
	}
}

func TestPTY_GetNotFound(t *testing.T) {
	cs, _ := openTestStore(t)
	ctx := context.Background()
	_, err := cs.GetPTY(ctx, "NOPE")
	if !errors.Is(err, chats.ErrNoPTYSession) {
		t.Errorf("expected ErrNoPTYSession, got %v", err)
	}
}

func TestPTY_RemoveDeletesRow(t *testing.T) {
	cs, _ := openTestStore(t)
	ctx := context.Background()
	c, _ := cs.Create(ctx, "app1", "live", "", "t")
	if _, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:      c.ID,
		TmuxSession: "kitsoki-chat-" + c.ID,
	}); err != nil {
		t.Fatalf("AttachPTY: %v", err)
	}
	if err := cs.RemovePTY(ctx, c.ID); err != nil {
		t.Fatalf("RemovePTY: %v", err)
	}
	if _, err := cs.GetPTY(ctx, c.ID); !errors.Is(err, chats.ErrNoPTYSession) {
		t.Errorf("Get after Remove: expected ErrNoPTYSession, got %v", err)
	}
}

func TestPTY_RemoveNoRowReturnsErrNoPTYSession(t *testing.T) {
	cs, _ := openTestStore(t)
	if err := cs.RemovePTY(context.Background(), "NOPE"); !errors.Is(err, chats.ErrNoPTYSession) {
		t.Errorf("expected ErrNoPTYSession, got %v", err)
	}
}

func TestPTY_CrossHostRowRejectsLocalMutations(t *testing.T) {
	cs, _ := openTestStore(t)
	ctx := context.Background()
	c, _ := cs.Create(ctx, "app1", "live", "", "t")

	// Insert a row whose tmux_host is some other machine, simulating a
	// chat that was attached on a different host.
	tdb := cs.DBForTest()
	tdb.MustExec(t, `
		INSERT INTO chat_pty_sessions
		  (chat_id, tmux_session, tmux_host, mode,
		   permission_mode, workspace_path,
		   created_at, updated_at, last_idle_at)
		VALUES (?, 'kitsoki-chat-X', 'remotebox', 'pty_attached', '', '', 1, 1, NULL)`,
		c.ID,
	)

	// AttachPTY from this host must refuse with ErrPTYCrossHost.
	if _, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:      c.ID,
		TmuxSession: "kitsoki-chat-X",
	}); !errors.Is(err, chats.ErrPTYCrossHost) {
		t.Errorf("AttachPTY cross-host: expected ErrPTYCrossHost, got %v", err)
	}

	// DetachPTY likewise.
	if _, err := cs.DetachPTY(ctx, c.ID); !errors.Is(err, chats.ErrPTYCrossHost) {
		t.Errorf("DetachPTY cross-host: expected ErrPTYCrossHost, got %v", err)
	}

	// RemovePTY likewise — and the row stays put.
	if err := cs.RemovePTY(ctx, c.ID); !errors.Is(err, chats.ErrPTYCrossHost) {
		t.Errorf("RemovePTY cross-host: expected ErrPTYCrossHost, got %v", err)
	}
	if n := tdb.MustQueryInt(t, `SELECT count(*) FROM chat_pty_sessions WHERE chat_id = ?`, c.ID); n != 1 {
		t.Errorf("cross-host row should not have been deleted; got %d rows", n)
	}
}

func TestPTY_ListPTYForHostFiltersCrossHostRows(t *testing.T) {
	cs, _ := openTestStore(t)
	ctx := context.Background()
	c1, _ := cs.Create(ctx, "app1", "live", "", "local-chat")
	c2, _ := cs.Create(ctx, "app1", "live", "", "remote-chat")

	if _, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:      c1.ID,
		TmuxSession: "kitsoki-chat-1",
	}); err != nil {
		t.Fatalf("AttachPTY local: %v", err)
	}

	tdb := cs.DBForTest()
	tdb.MustExec(t, `
		INSERT INTO chat_pty_sessions
		  (chat_id, tmux_session, tmux_host, mode,
		   permission_mode, workspace_path,
		   created_at, updated_at, last_idle_at)
		VALUES (?, 'kitsoki-chat-2', 'remotebox', 'pty_attached', '', '', 1, 1, NULL)`,
		c2.ID,
	)

	list, err := cs.ListPTYForHost(ctx)
	if err != nil {
		t.Fatalf("ListPTYForHost: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 local row, got %d", len(list))
	}
	if list[0].ChatID != c1.ID {
		t.Errorf("wrong chat returned: %s vs %s", list[0].ChatID, c1.ID)
	}
}

func TestPTY_MarkPTYIdleStampsTimestamp(t *testing.T) {
	cs, fake := openTestStore(t)
	ctx := context.Background()
	c, _ := cs.Create(ctx, "app1", "live", "", "t")
	if _, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID:      c.ID,
		TmuxSession: "kitsoki-chat-" + c.ID,
	}); err != nil {
		t.Fatalf("AttachPTY: %v", err)
	}

	fake.Advance(5 * time.Second)
	if err := cs.MarkPTYIdle(ctx, c.ID); err != nil {
		t.Fatalf("MarkPTYIdle: %v", err)
	}
	p, err := cs.GetPTY(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetPTY: %v", err)
	}
	if p.LastIdleAt == nil {
		t.Fatal("LastIdleAt should be non-nil after MarkPTYIdle")
	}
	if !p.LastIdleAt.Equal(fake.Now()) {
		t.Errorf("LastIdleAt = %v, want %v", *p.LastIdleAt, fake.Now())
	}
}

func TestPTY_MarkPTYIdleNoRow(t *testing.T) {
	cs, _ := openTestStore(t)
	if err := cs.MarkPTYIdle(context.Background(), "NOPE"); !errors.Is(err, chats.ErrNoPTYSession) {
		t.Errorf("expected ErrNoPTYSession, got %v", err)
	}
}

func TestPTY_GCDeadTmuxRemovesDead(t *testing.T) {
	cs, _ := openTestStore(t)
	ctx := context.Background()
	alive, _ := cs.Create(ctx, "app1", "live", "", "alive")
	dead, _ := cs.Create(ctx, "app1", "live", "", "dead")
	for _, c := range []string{alive.ID, dead.ID} {
		if _, err := cs.AttachPTY(ctx, chats.AttachPTYOptions{
			ChatID:      c,
			TmuxSession: "kitsoki-chat-" + c,
		}); err != nil {
			t.Fatalf("AttachPTY %s: %v", c, err)
		}
	}

	probe := func(name string) bool {
		// Only "alive" survives the probe.
		return name == "kitsoki-chat-"+alive.ID
	}
	removed, err := cs.GCDeadTmux(ctx, probe)
	if err != nil {
		t.Fatalf("GCDeadTmux: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := cs.GetPTY(ctx, alive.ID); err != nil {
		t.Errorf("alive row gone: %v", err)
	}
	if _, err := cs.GetPTY(ctx, dead.ID); !errors.Is(err, chats.ErrNoPTYSession) {
		t.Errorf("dead row should be deleted: %v", err)
	}
}

func TestPTY_GCDeadTmuxNilProbeRejected(t *testing.T) {
	cs, _ := openTestStore(t)
	if _, err := cs.GCDeadTmux(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil probe")
	}
}
