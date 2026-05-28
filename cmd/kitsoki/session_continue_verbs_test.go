// session_continue_verbs_test.go — happy-path tests for the four continue-mode
// session subcommands: checkpoint, journal, forget, detach.
// These exercise the full command path in-process against a temp-dir SQLite DB.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/store"
)

// ─── checkpoint ───────────────────────────────────────────────────────────────

// TestSessionCheckpoint_HappyPath creates a session, runs a checkpoint, and
// verifies the JSON output shape and that rows are present in the journal table.
func TestSessionCheckpoint_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	// Create session.
	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:CKPT-1",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	// Force a checkpoint.
	out, err = runKitsoki(t, "session", "checkpoint",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, sid, result["session_id"])

	cps, ok := result["checkpoints"].([]any)
	require.True(t, ok, "checkpoints should be a list")
	require.Len(t, cps, 2, "expected world and state checkpoints")

	docs := make(map[string]bool)
	for _, cp := range cps {
		row := cp.(map[string]any)
		docs[row["doc"].(string)] = true
		assert.Greater(t, row["doc_version"].(float64), float64(0))
	}
	assert.True(t, docs["world"], "world checkpoint expected")
	assert.True(t, docs["state"], "state checkpoint expected")

	// Verify rows in the journal table via direct DB access.
	s2, openErr := store.Open(dbPath)
	require.NoError(t, openErr)
	defer s2.Close()

	rows, qErr := s2.DB().QueryContext(context.Background(),
		`SELECT doc, kind FROM journal WHERE session_id = ? ORDER BY doc`,
		sid,
	)
	require.NoError(t, qErr)
	defer rows.Close()

	found := map[string]string{}
	for rows.Next() {
		var doc, kind string
		require.NoError(t, rows.Scan(&doc, &kind))
		found[doc] = kind
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, "world.checkpoint", found["world"])
	assert.Equal(t, "state.checkpoint", found["state"])
}

// TestSessionCheckpoint_SingleDoc with --doc world only checkpoints that doc.
func TestSessionCheckpoint_SingleDoc(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:CKPT-2",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	out, err = runKitsoki(t, "session", "checkpoint",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--id", sid,
		"--doc", "world",
	)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	cps := result["checkpoints"].([]any)
	require.Len(t, cps, 1)
	assert.Equal(t, "world", cps[0].(map[string]any)["doc"])
}

// ─── journal ──────────────────────────────────────────────────────────────────

// TestSessionJournal_EmptySession verifies that journal on a fresh session
// produces no output (no rows yet).
func TestSessionJournal_EmptySession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:JRNL-1",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	out, err = runKitsoki(t, "session", "journal",
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out), "no journal entries expected for fresh session")
}

// TestSessionJournal_AfterCheckpoint verifies JSONL output after a checkpoint.
func TestSessionJournal_AfterCheckpoint(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:JRNL-2",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	// Write a checkpoint so there are rows to dump.
	_, err = runKitsoki(t, "session", "checkpoint",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	// Dump journal — should yield JSONL.
	out, err = runKitsoki(t, "session", "journal",
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.GreaterOrEqual(t, len(lines), 2, "expected at least world + state checkpoint entries")

	docs := map[string]bool{}
	for _, line := range lines {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), "each line must be valid JSON: %s", line)
		assert.Equal(t, sid, entry["session_id"])
		if doc, ok := entry["doc"].(string); ok {
			docs[doc] = true
		}
	}
	assert.True(t, docs["world"])
	assert.True(t, docs["state"])
}

// TestSessionJournal_DocFilter filters by --doc world.
func TestSessionJournal_DocFilter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:JRNL-3",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	_, err = runKitsoki(t, "session", "checkpoint",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	out, err = runKitsoki(t, "session", "journal",
		"--db", dbPath,
		"--id", sid,
		"--doc", "world",
	)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		assert.Equal(t, "world", entry["doc"])
	}
}

// ─── forget ───────────────────────────────────────────────────────────────────

// TestSessionForget_HappyPath creates a session, runs forget with --yes, and
// verifies the JSON output shape and that the session no longer exists.
func TestSessionForget_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:FORGET-1",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	// Write a checkpoint so journal rows exist.
	_, err = runKitsoki(t, "session", "checkpoint",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	// Forget with --yes.
	out, err = runKitsoki(t, "session", "forget",
		"--db", dbPath,
		"--id", sid,
		"--yes",
	)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, sid, result["session_id"])

	deletedTables, ok := result["deleted_tables"].(map[string]any)
	require.True(t, ok, "deleted_tables should be a map")
	// sessions row must have been deleted.
	assert.Equal(t, float64(1), deletedTables["sessions"])
	// events and snapshots may be 0 for a fresh session but the keys must exist.
	_, hasEvents := deletedTables["events"]
	assert.True(t, hasEvents, "events key expected in deleted_tables")

	// Verify the session is gone.
	s2, openErr := store.Open(dbPath)
	require.NoError(t, openErr)
	defer s2.Close()
	_, lookErr := s2.LookupByKey(context.Background(), "jira", "FORGET-1")
	assert.ErrorIs(t, lookErr, store.ErrSessionNotFound)
}

// TestSessionForget_InteractiveConfirm passes 'y' on stdin and verifies success.
func TestSessionForget_InteractiveConfirm(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:FORGET-2",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	// Run forget with interactive 'y' on stdin.
	root := rootForTest()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"session", "forget", "--db", dbPath, "--id", sid})
	root.SetContext(context.Background())
	require.NoError(t, root.Execute())

	var result map[string]any
	require.NoError(t, json.Unmarshal(outBuf.Bytes(), &result))
	assert.Equal(t, sid, result["session_id"])
}

// TestSessionForget_AbortedByN passes 'N' on stdin and verifies no deletion.
func TestSessionForget_AbortedByN(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:FORGET-3",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	root := rootForTest()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader("N\n"))
	root.SetArgs([]string{"session", "forget", "--db", dbPath, "--id", sid})
	root.SetContext(context.Background())
	require.NoError(t, root.Execute())

	// Session must still exist.
	s2, openErr := store.Open(dbPath)
	require.NoError(t, openErr)
	defer s2.Close()
	gotSID, lookErr := s2.LookupByKey(context.Background(), "jira", "FORGET-3")
	require.NoError(t, lookErr)
	assert.Equal(t, sid, string(gotSID))
}

// ─── detach ───────────────────────────────────────────────────────────────────

// TestSessionDetach_NoLock verifies that detach on a session with no lock
// prints "no lock held" and returns {"detached":false}.
func TestSessionDetach_NoLock(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:DETACH-1",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	out, err = runKitsoki(t, "session", "detach",
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, sid, result["session_id"])
	assert.Equal(t, false, result["detached"])
}

// TestSessionDetach_StaleDeadPID inserts a lock row with a dead PID and
// verifies detach removes it.
func TestSessionDetach_StaleDeadPID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:DETACH-2",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	// Insert a fake stale lock with an impossible PID on this host.
	thisHost, _ := os.Hostname()
	s2, openErr := store.Open(dbPath)
	require.NoError(t, openErr)

	_, insertErr := s2.DB().ExecContext(context.Background(),
		`INSERT INTO session_locks (session_id, owner_pid, owner_host, acquired_at)
		 VALUES (?, ?, ?, ?)`,
		sid, 999999999, thisHost, 0,
	)
	require.NoError(t, insertErr)
	s2.Close()

	out, err = runKitsoki(t, "session", "detach",
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, sid, result["session_id"])
	assert.Equal(t, true, result["detached"])
	priorOwner, ok := result["prior_owner"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(999999999), priorOwner["pid"])
}

// TestSessionDetach_CrossHost inserts a lock row with a different hostname and
// verifies detach removes it (cross-host locks cannot be probed for liveness).
func TestSessionDetach_CrossHost(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")

	out, err := runKitsoki(t, "session", "create",
		"--app", cloakAppFlag(),
		"--db", dbPath,
		"--key", "jira:DETACH-3",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	sid := created["session_id"].(string)

	// Insert a lock from a different host.
	s2, openErr := store.Open(dbPath)
	require.NoError(t, openErr)

	_, insertErr := s2.DB().ExecContext(context.Background(),
		`INSERT INTO session_locks (session_id, owner_pid, owner_host, acquired_at)
		 VALUES (?, ?, ?, ?)`,
		sid, 12345, "other-host.example.com", 0,
	)
	require.NoError(t, insertErr)
	s2.Close()

	out, err = runKitsoki(t, "session", "detach",
		"--db", dbPath,
		"--id", sid,
	)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, true, result["detached"])
	priorOwner := result["prior_owner"].(map[string]any)
	assert.Equal(t, "other-host.example.com", priorOwner["host"])
}
