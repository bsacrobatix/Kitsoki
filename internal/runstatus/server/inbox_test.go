package server_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/jobs"
	"kitsoki/internal/journal"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
)

// inboxFixture is a live cloak session with a JobStore wired into both the
// orchestrator and the web Driver — the shape `kitsoki web` builds. It returns
// the httptest server, the JobStore (so a test can post a notification without
// an LLM), and the orchestrator session id (the teleport target's origin).
type inboxFixture struct {
	ts  *httptest.Server
	js  *jobs.JobStore
	sid app.SessionID
}

func buildInboxFixture(t *testing.T) inboxFixture {
	t.Helper()
	def, err := app.Load("../../../testdata/apps/cloak/app.yaml")
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

	js, err := jobs.NewJobStore(s.DB())
	require.NoError(t, err)

	orch := orchestrator.New(def, m, s, nil,
		orchestrator.WithJournalWriter(jw),
		orchestrator.WithJournalReader(jr),
		orchestrator.WithJobStore(js),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	sink, err := store.OpenJSONL(filepath.Join(t.TempDir(), "run.jsonl"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sink.Close() })

	live := server.NewLiveSession(sink, def, string(sid), string(orch.InitialState()))
	orch.SetEventSink(live)
	require.NoError(t, orch.RunInitialOnEnter(ctx, sid))

	driver := server.OrchestratorDriver{Orch: orch, SID: sid, Jobs: js}
	srv := server.NewMulti(&singleInboxProvider{
		entry: server.Entry{Source: live, Driver: driver},
		sid:   string(sid),
	}, server.WithPollInterval(20*time.Millisecond))

	// Register the cross-session relay against this session (what web.go does
	// via SetNotifier→AttachSession).
	srv.AttachSession(orch, sid, string(sid), js)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return inboxFixture{ts: ts, js: js, sid: sid}
}

// singleInboxProvider is a one-session SessionProvider keyed by the
// orchestrator session id (so session-routed RPCs resolve), enough for the
// inbox RPC tests.
type singleInboxProvider struct {
	entry server.Entry
	sid   string
}

func (p *singleInboxProvider) Get(id string) (server.Entry, bool) {
	if id == p.sid || id == "" {
		return p.entry, true
	}
	return server.Entry{}, false
}
func (p *singleInboxProvider) List() []runstatus.SessionHeader { return nil }

// The provider must satisfy server.SessionProvider; only Get matters for the
// inbox RPC tests — the rest are inert stubs.
func (p *singleInboxProvider) ListStories() []server.StoryHeader { return nil }
func (p *singleInboxProvider) Rescan() ([]server.StoryHeader, error) {
	return nil, nil
}
func (p *singleInboxProvider) NewSession(context.Context, string) (string, error) {
	return "", nil
}
func (p *singleInboxProvider) Reload(context.Context, string) (bool, error) { return false, nil }
func (p *singleInboxProvider) Staleness(context.Context, string) (bool, string, error) {
	return false, "", nil
}

// postNotification inserts a teleportable notification for the fixture session
// without any LLM — the deterministic stand-in for a terminal background job.
func (f inboxFixture) postNotification(t *testing.T, title string) string {
	t.Helper()
	n := &jobs.Notification{
		SessionID:     f.sid,
		CreatedAt:     time.Now(),
		Severity:      jobs.SeveritySuccess,
		Title:         title,
		Body:          "done",
		TeleportState: "foyer", // a real state in the cloak fixture
		OriginKind:    "job",
		OriginRef:     "job:test",
	}
	require.NoError(t, f.js.InsertNotification(context.Background(), n))
	return n.ID
}

// TestInbox_ListReadDismiss exercises the three CRUD RPCs over the wire.
func TestInbox_ListReadDismiss(t *testing.T) {
	f := buildInboxFixture(t)
	id := f.postNotification(t, "Background turn ready")

	var listed struct {
		Notifications []map[string]any `json:"notifications"`
	}
	rpcCall(t, f.ts, "runstatus.session.notifications.list",
		map[string]any{"session_id": string(f.sid)}, &listed)
	require.Len(t, listed.Notifications, 1)
	assert.Equal(t, id, listed.Notifications[0]["ID"])
	assert.Equal(t, "Background turn ready", listed.Notifications[0]["Title"])

	// read
	var ok struct {
		OK bool `json:"ok"`
	}
	rpcCall(t, f.ts, "runstatus.session.notifications.read",
		map[string]any{"session_id": string(f.sid), "id": id}, &ok)
	assert.True(t, ok.OK)

	// dismiss → drops out of the list
	rpcCall(t, f.ts, "runstatus.session.notifications.dismiss",
		map[string]any{"session_id": string(f.sid), "id": id}, &ok)
	assert.True(t, ok.OK)

	rpcCall(t, f.ts, "runstatus.session.notifications.list",
		map[string]any{"session_id": string(f.sid)}, &listed)
	assert.Empty(t, listed.Notifications, "dismissed notification should drop out")
}

// TestInbox_Teleport proves the teleport RPC resolves the notification and
// jumps the session to its origin state via Orchestrator.Teleport.
func TestInbox_Teleport(t *testing.T) {
	f := buildInboxFixture(t)
	id := f.postNotification(t, "ready")

	var res turnResultWire
	rpcCall(t, f.ts, "runstatus.session.teleport",
		map[string]any{"session_id": string(f.sid), "notification_id": id}, &res)

	assert.Equal(t, "foyer", res.State, "teleport should land at the notification's origin state")
	assert.NotEmpty(t, res.View, "teleport re-renders the destination room")
}

// TestInbox_TeleportNotTeleportable proves a notification with no destination
// state surfaces as a transport error (the surface renders it read-only).
func TestInbox_TeleportNotTeleportable(t *testing.T) {
	f := buildInboxFixture(t)
	n := &jobs.Notification{
		SessionID:  f.sid,
		CreatedAt:  time.Now(),
		Severity:   jobs.SeverityInfo,
		Title:      "informational only",
		OriginKind: "job",
	}
	require.NoError(t, f.js.InsertNotification(context.Background(), n))

	code, msg := rpcCallExpectError(t, f.ts, "runstatus.session.teleport",
		map[string]any{"session_id": string(f.sid), "notification_id": n.ID})
	assert.NotZero(t, code)
	assert.Contains(t, msg, "teleport")
}

// TestInbox_NilJobStore proves the nil-safety contract directly on the Driver:
// a session with no JobStore reports an empty inbox, read/dismiss no-op, and
// teleport returns the typed ErrNoInbox.
func TestInbox_NilJobStore(t *testing.T) {
	d := server.OrchestratorDriver{} // Jobs == nil
	ctx := context.Background()

	notifs, err := d.ListNotifications(ctx)
	require.NoError(t, err)
	assert.Nil(t, notifs)

	assert.NoError(t, d.MarkNotificationRead(ctx, "x"))
	assert.NoError(t, d.DismissNotification(ctx, "x"))

	_, err = d.Teleport(ctx, "x")
	assert.ErrorIs(t, err, server.ErrNoInbox)
}
