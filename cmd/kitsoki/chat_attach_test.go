package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/chats"
	"kitsoki/internal/store"
	"kitsoki/internal/tmux"
)

// fakeTmuxBinForCmd returns the path to internal/tmux/testdata/fake-tmux.sh
// so the chat-attach CLI tests can run without a real tmux.
func fakeTmuxBinForCmd(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(repoRoot, "internal", "tmux", "testdata", "fake-tmux.sh")
}

// setupChatAttachEnv wires the fake-tmux script, a per-test state
// directory, and an isolated XDG_STATE_HOME so DefaultSocketPath()
// lands inside the test sandbox.
func setupChatAttachEnv(t *testing.T) (stateDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-tmux.sh requires bash")
	}
	t.Setenv(tmux.TmuxBinEnv, fakeTmuxBinForCmd(t))
	stateDir = t.TempDir()
	t.Setenv("KITSOKI_FAKE_TMUX_STATE_DIR", stateDir)
	// Force DefaultSocketPath to a temp dir so we don't write into
	// the user's real ~/.local/state on the test machine.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Optional claude bin override — the fake-tmux script never
	// actually executes the command, so any string will do; setting
	// the env so future improvements (running the real shell) won't
	// blow up the suite.
	t.Setenv("KITSOKI_CLAUDE_BIN", "/bin/true")
	return stateDir
}

// TestChatAttach_HappyPath walks the full attach flow with the
// fake-tmux script. The fake's "attach" verb returns success
// immediately, so the kitsoki attach command should also return
// cleanly. The chat_pty_sessions row should land in pty_background
// after the attach exits.
func TestChatAttach_HappyPath(t *testing.T) {
	tmuxState := setupChatAttachEnv(t)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	ctx := context.Background()
	c, err := cs.Create(ctx, "bugfix", "live", "PROJ-1", "live chat")
	require.NoError(t, err)
	cleanup()

	stdout, err := runKitsoki(t, "chat", "attach", c.ID,
		"--db", dbPath,
		"--workspace", "/tmp",
	)
	require.NoError(t, err, "attach should succeed; stdout was: %s", stdout)

	// fake-tmux dropped a file for the session under stateDir.
	sessionFile := filepath.Join(tmuxState, "kitsoki-chat-"+c.ID)
	_, err = os.Stat(sessionFile)
	assert.NoError(t, err, "fake-tmux session file should exist")

	// Row landed in pty_background after the attach returned.
	cs2, cleanup2 := openChatStoreForTest(t, dbPath)
	defer cleanup2()
	p, err := cs2.GetPTY(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, chats.PtyModeBackground, p.Mode)
	assert.Equal(t, "kitsoki-chat-"+c.ID, p.TmuxSession)

	// Chat now has a claude_session_id (was minted during attach).
	chatRow, err := cs2.Get(ctx, c.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, chatRow.ClaudeSessionID, "claude_session_id should be allocated")
}

// TestChatAttach_AcceptsTmuxSessionName ensures the helper that
// strips the kitsoki-chat- prefix lets tmux bindings pass
// #{session_name} directly.
func TestChatAttach_AcceptsTmuxSessionName(t *testing.T) {
	setupChatAttachEnv(t)
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	c, err := cs.Create(context.Background(), "bugfix", "live", "", "x")
	require.NoError(t, err)
	cleanup()

	_, err = runKitsoki(t, "chat", "attach", "kitsoki-chat-"+c.ID, "--db", dbPath)
	require.NoError(t, err)
}

// TestChatAttach_LockBusy returns EX_TEMPFAIL when another process
// holds the chat lock. We simulate by inserting a chat_locks row
// owned by this process's PID (lock.go treats same-pid re-entry as
// busy since the lock is not re-entrant).
func TestChatAttach_LockBusy(t *testing.T) {
	setupChatAttachEnv(t)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	c, err := cs.Create(context.Background(), "bugfix", "live", "", "x")
	require.NoError(t, err)
	cleanup()

	// Seed the lock row directly via the store's raw DB. lock.go
	// rejects same-pid re-entry as busy, so using os.Getpid() here
	// is reliable: the attach process is this same Go test binary,
	// so its acquireChatLock will see ownerPID == this PID and
	// return ErrChatBusy.
	s, err := store.Open(dbPath)
	require.NoError(t, err)
	host, _ := os.Hostname()
	_, err = s.DB().Exec(`
		INSERT INTO chat_locks (chat_id, owner_pid, owner_host, acquired_at, heartbeat_at)
		VALUES (?, ?, ?, 0, 0)`,
		c.ID, os.Getpid(), host,
	)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	_, err = runKitsoki(t, "chat", "attach", c.ID, "--db", dbPath)
	require.Error(t, err)
	assert.True(t, IsTempFail(err), "lock busy should be EX_TEMPFAIL; got %v", err)
}

// TestChatDetach_Background flips an attached row to pty_background.
func TestChatDetach_Background(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	ctx := context.Background()
	c, err := cs.Create(ctx, "bugfix", "live", "", "x")
	require.NoError(t, err)
	_, err = cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID: c.ID, TmuxSession: "kitsoki-chat-" + c.ID,
	})
	require.NoError(t, err)
	cleanup()

	stdout, err := runKitsoki(t, "chat", "detach", c.ID,
		"--db", dbPath,
		"--mode", "background",
	)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &resp))
	assert.Equal(t, "pty_background", resp["mode"])

	cs2, cleanup2 := openChatStoreForTest(t, dbPath)
	defer cleanup2()
	p, err := cs2.GetPTY(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, chats.PtyModeBackground, p.Mode)
}

func TestChatDetach_HeadlessRemovesRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	ctx := context.Background()
	c, _ := cs.Create(ctx, "bugfix", "live", "", "x")
	_, _ = cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID: c.ID, TmuxSession: "kitsoki-chat-" + c.ID,
	})
	cleanup()

	stdout, err := runKitsoki(t, "chat", "detach", c.ID,
		"--db", dbPath,
		"--mode", "headless",
	)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &resp))
	assert.Equal(t, true, resp["removed"])

	cs2, cleanup2 := openChatStoreForTest(t, dbPath)
	defer cleanup2()
	_, err = cs2.GetPTY(ctx, c.ID)
	require.True(t, errors.Is(err, chats.ErrNoPTYSession), "row should be gone; got %v", err)
}

func TestChatDetach_InvalidMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	c, _ := cs.Create(context.Background(), "bugfix", "live", "", "x")
	cleanup()
	_, err := runKitsoki(t, "chat", "detach", c.ID,
		"--db", dbPath,
		"--mode", "bogus",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --mode")
}

func TestChatDetach_NoRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	c, _ := cs.Create(context.Background(), "bugfix", "live", "", "x")
	cleanup()
	_, err := runKitsoki(t, "chat", "detach", c.ID,
		"--db", dbPath,
		"--mode", "background",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pty session")
}

// TestChatGC_RemovesDeadRowsKeepsAlive seeds two rows; one's tmux
// session is "alive" (fake-tmux state-dir file exists), the other
// isn't. After gc, the dead row should be gone, the alive one kept.
func TestChatGC_RemovesDeadRowsKeepsAlive(t *testing.T) {
	stateDir := setupChatAttachEnv(t)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	ctx := context.Background()

	cAlive, _ := cs.Create(ctx, "bugfix", "live", "", "alive")
	cDead, _ := cs.Create(ctx, "bugfix", "live", "", "dead")
	_, _ = cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID: cAlive.ID, TmuxSession: "kitsoki-chat-" + cAlive.ID,
	})
	_, _ = cs.AttachPTY(ctx, chats.AttachPTYOptions{
		ChatID: cDead.ID, TmuxSession: "kitsoki-chat-" + cDead.ID,
	})
	// Make the "alive" chat's tmux session look live to the fake.
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "kitsoki-chat-"+cAlive.ID), nil, 0o644))
	cleanup()

	stdout, err := runKitsoki(t, "chat", "gc", "--db", dbPath)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &resp))
	count, _ := resp["removed_count"].(float64)
	assert.Equal(t, float64(1), count)

	cs2, cleanup2 := openChatStoreForTest(t, dbPath)
	defer cleanup2()
	if _, err := cs2.GetPTY(ctx, cAlive.ID); err != nil {
		t.Errorf("alive row should still exist: %v", err)
	}
	if _, err := cs2.GetPTY(ctx, cDead.ID); !errors.Is(err, chats.ErrNoPTYSession) {
		t.Errorf("dead row should be removed: %v", err)
	}
}

// TestChatGC_NoRowsIsNoOp returns a clean summary on a fresh store.
func TestChatGC_NoRowsIsNoOp(t *testing.T) {
	setupChatAttachEnv(t)
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cs, cleanup := openChatStoreForTest(t, dbPath)
	cleanup()
	_ = cs
	stdout, err := runKitsoki(t, "chat", "gc", "--db", dbPath)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &resp))
	count, _ := resp["removed_count"].(float64)
	assert.Equal(t, float64(0), count)
}
