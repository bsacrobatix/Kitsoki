package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
)

// stubSource is a minimal in-memory [server.Source] for the routing tests: it
// returns a fixed snapshot/AppDef and never touches a file. The multi-session
// dispatch path only needs Snapshot/Events/AppDef.
type stubSource struct {
	header runstatus.SessionHeader
	def    *app.AppDef
	events []runstatus.TraceEvent
}

func (s *stubSource) Snapshot() (runstatus.Snapshot, error) {
	return runstatus.Snapshot{Session: s.header, App: s.def, Events: s.events}, nil
}
func (s *stubSource) Events() ([]runstatus.TraceEvent, error) { return s.events, nil }
func (s *stubSource) AppDef() *app.AppDef                     { return s.def }

// stubProvider is a [server.SessionProvider] built from func fields, so each
// routing test wires only the behaviour it asserts. It is the deterministic
// stand-in the test_plan calls for — no orchestrator, no LLM.
type stubProvider struct {
	mu sync.Mutex

	entries  map[string]server.Entry
	stories  []server.StoryHeader
	newFn    func(ctx context.Context, storyPath string) (string, error)
	reloadFn func(ctx context.Context, sessionID string) (bool, error)
	rescanFn func() ([]server.StoryHeader, error)
}

func newStubProvider() *stubProvider {
	return &stubProvider{entries: map[string]server.Entry{}}
}

func (p *stubProvider) Get(sessionID string) (server.Entry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[sessionID]
	return e, ok
}

func (p *stubProvider) List() []runstatus.SessionHeader {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]runstatus.SessionHeader, 0, len(p.entries))
	for _, e := range p.entries {
		snap, _ := e.Source.Snapshot()
		out = append(out, snap.Session)
	}
	return out
}

func (p *stubProvider) NewSession(ctx context.Context, storyPath string) (string, error) {
	return p.newFn(ctx, storyPath)
}

func (p *stubProvider) Reload(ctx context.Context, sessionID string) (bool, error) {
	return p.reloadFn(ctx, sessionID)
}

func (p *stubProvider) ListStories() []server.StoryHeader {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stories
}

func (p *stubProvider) Rescan() ([]server.StoryHeader, error) { return p.rescanFn() }

// put registers a routable entry under sessionID with a stub source carrying
// the given header + def.
func (p *stubProvider) put(sessionID string, header runstatus.SessionHeader, def *app.AppDef) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[sessionID] = server.Entry{Source: &stubSource{header: header, def: def}}
}

// TestMulti_NewSessionRoundTrip proves session.new returns an id that
// subsequent session-routed reads resolve to: the stub provider creates an
// entry on NewSession, and session.get / session.app then route by that id.
func TestMulti_NewSessionRoundTrip(t *testing.T) {
	t.Parallel()
	p := newStubProvider()
	p.newFn = func(_ context.Context, storyPath string) (string, error) {
		assert.Equal(t, "/abs/story/app.yaml", storyPath)
		def := &app.AppDef{App: app.AppMeta{ID: "story-app", Version: "1.0.0"}}
		p.put("sess-new", runstatus.SessionHeader{SessionID: "sess-new", AppID: "story-app", CurrentState: "start"}, def)
		return "sess-new", nil
	}

	ts := httptest.NewServer(server.NewMulti(p).Handler())
	defer ts.Close()

	var created struct {
		SessionID string `json:"session_id"`
	}
	rpcCall(t, ts, "runstatus.session.new", map[string]any{"story_path": "/abs/story/app.yaml"}, &created)
	require.Equal(t, "sess-new", created.SessionID)

	var hdr runstatus.SessionHeader
	rpcCall(t, ts, "runstatus.session.get", map[string]any{"session_id": created.SessionID}, &hdr)
	assert.Equal(t, "sess-new", hdr.SessionID)
	assert.Equal(t, "story-app", hdr.AppID)

	var appOut app.AppDef
	rpcCall(t, ts, "runstatus.session.app", map[string]any{"session_id": created.SessionID}, &appOut)
	assert.Equal(t, "story-app", appOut.App.ID)
}

// TestMulti_UnknownSession proves a session-routed RPC with an id the provider
// does not know returns a structured not-found error, never a nil-deref.
func TestMulti_UnknownSession(t *testing.T) {
	t.Parallel()
	p := newStubProvider()
	ts := httptest.NewServer(server.NewMulti(p).Handler())
	defer ts.Close()

	code, msg := rpcCallExpectError(t, ts, "runstatus.session.get", map[string]any{"session_id": "ghost"})
	assert.Equal(t, -32002, code, "unknown session_id should return codeNotFound")
	assert.Contains(t, msg, "ghost")
}

// TestMulti_SessionReload proves session.reload returns prev_state_exists
// mirroring Orchestrator.Reload semantics, for both a valid reload (true) and a
// reload where the current state was removed (false).
func TestMulti_SessionReload(t *testing.T) {
	t.Parallel()

	t.Run("prev state still exists", func(t *testing.T) {
		t.Parallel()
		p := newStubProvider()
		p.put("s1", runstatus.SessionHeader{SessionID: "s1"}, testDef())
		p.reloadFn = func(_ context.Context, sid string) (bool, error) {
			assert.Equal(t, "s1", sid)
			return true, nil
		}
		ts := httptest.NewServer(server.NewMulti(p).Handler())
		defer ts.Close()

		var res struct {
			OK              bool `json:"ok"`
			PrevStateExists bool `json:"prev_state_exists"`
		}
		rpcCall(t, ts, "runstatus.session.reload", map[string]any{"session_id": "s1"}, &res)
		assert.True(t, res.OK)
		assert.True(t, res.PrevStateExists)
	})

	t.Run("prev state removed", func(t *testing.T) {
		t.Parallel()
		p := newStubProvider()
		p.put("s2", runstatus.SessionHeader{SessionID: "s2"}, testDef())
		p.reloadFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
		ts := httptest.NewServer(server.NewMulti(p).Handler())
		defer ts.Close()

		var res struct {
			OK              bool `json:"ok"`
			PrevStateExists bool `json:"prev_state_exists"`
		}
		rpcCall(t, ts, "runstatus.session.reload", map[string]any{"session_id": "s2"}, &res)
		assert.True(t, res.OK)
		assert.False(t, res.PrevStateExists, "removed current state must report prev_state_exists:false")
	})
}

// TestMulti_StoriesRescan proves stories.rescan reflects a story added between
// calls: the provider's catalogue grows and stories.list/rescan return the new
// entry.
func TestMulti_StoriesRescan(t *testing.T) {
	t.Parallel()
	p := newStubProvider()
	p.stories = []server.StoryHeader{{Path: "/a/app.yaml", AppID: "a", Title: "A"}}
	p.rescanFn = func() ([]server.StoryHeader, error) {
		p.mu.Lock()
		p.stories = append(p.stories, server.StoryHeader{Path: "/b/app.yaml", AppID: "b", Title: "B"})
		out := p.stories
		p.mu.Unlock()
		return out, nil
	}
	ts := httptest.NewServer(server.NewMulti(p).Handler())
	defer ts.Close()

	var before []server.StoryHeader
	rpcCall(t, ts, "runstatus.stories.list", map[string]any{}, &before)
	require.Len(t, before, 1)
	assert.Equal(t, "a", before[0].AppID)

	var after []server.StoryHeader
	rpcCall(t, ts, "runstatus.stories.rescan", map[string]any{}, &after)
	require.Len(t, after, 2, "rescan should surface the newly-added story")
	assert.Equal(t, "b", after[1].AppID)

	// stories.list now reflects the rescanned catalogue.
	var relisted []server.StoryHeader
	rpcCall(t, ts, "runstatus.stories.list", map[string]any{}, &relisted)
	assert.Len(t, relisted, 2)
}

// TestMulti_SessionsList proves sessions.list returns the provider's live
// sessions (not a single trace's snap.Session) — one header per live entry.
func TestMulti_SessionsList(t *testing.T) {
	t.Parallel()
	p := newStubProvider()
	p.put("s1", runstatus.SessionHeader{SessionID: "s1", AppID: "a"}, testDef())
	p.put("s2", runstatus.SessionHeader{SessionID: "s2", AppID: "b"}, testDef())
	ts := httptest.NewServer(server.NewMulti(p).Handler())
	defer ts.Close()

	var list []runstatus.SessionHeader
	rpcCall(t, ts, "runstatus.sessions.list", map[string]any{}, &list)
	require.Len(t, list, 2)
	ids := map[string]bool{}
	for _, h := range list {
		ids[h.SessionID] = true
	}
	assert.True(t, ids["s1"] && ids["s2"], "both live sessions listed, got %+v", list)
}

// TestMulti_NewSessionInvalidStory proves an invalid story surfaces as a
// structured error (so the UI can show it before navigating), not a panic.
func TestMulti_NewSessionInvalidStory(t *testing.T) {
	t.Parallel()
	p := newStubProvider()
	p.newFn = func(context.Context, string) (string, error) {
		return "", assert.AnError
	}
	ts := httptest.NewServer(server.NewMulti(p).Handler())
	defer ts.Close()

	code, _ := rpcCallExpectError(t, ts, "runstatus.session.new", map[string]any{"story_path": "/bad.yaml"})
	assert.Equal(t, -32000, code, "an invalid story should return a structured server error")
}

// TestMulti_SSERoutesBySession proves the per-session SSE path: a subscription
// captures its session_id and the poller resolves THAT session's live Source
// each tick, so an event appended to the subscribed session arrives — and an
// event appended to a different session does not bleed into the stream. This is
// the routing analogue of TestServer_SubscribeAndStream.
func TestMulti_SSERoutesBySession(t *testing.T) {
	t.Parallel()
	def := testDef()
	_, liveA := openLiveSink(t, def, "sa", "main")
	_, liveB := openLiveSink(t, def, "sb", "main")

	p := newStubProvider()
	p.mu.Lock()
	p.entries["sa"] = server.Entry{Source: liveA}
	p.entries["sb"] = server.Entry{Source: liveB}
	p.mu.Unlock()

	srv := server.NewMulti(p, server.WithPollInterval(20*time.Millisecond))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var sub struct {
		SubscriptionID string `json:"subscription_id"`
	}
	rpcCall(t, ts, "runstatus.session.subscribe", map[string]any{"session_id": "sa"}, &sub)
	require.NotEmpty(t, sub.SubscriptionID)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/rpc/events?subscription_id="+sub.SubscriptionID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	type result struct {
		state string
		found bool
	}
	resCh := make(chan result, 1)
	var once sync.Once
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var frame struct {
				Params struct {
					Event runstatus.TraceEvent `json:"event"`
				} `json:"params"`
			}
			if json.Unmarshal([]byte(data), &frame) != nil {
				continue
			}
			once.Do(func() { resCh <- result{frame.Params.Event.StatePath, true} })
			return
		}
		once.Do(func() { resCh <- result{found: false} })
	}()

	// Append to session B FIRST — it must NOT appear on session A's stream — then
	// to A, which must.
	require.NoError(t, liveB.Append(store.Event{
		Turn: 1, Kind: store.StateEntered, StatePath: "wrong-session",
		Payload: json.RawMessage(`{}`),
	}))
	require.NoError(t, liveA.Append(store.Event{
		Turn: 1, Kind: store.StateEntered, StatePath: "right-session",
		Payload: json.RawMessage(`{}`),
	}))

	select {
	case res := <-resCh:
		require.True(t, res.found, "expected an event on session A's stream")
		assert.Equal(t, "right-session", res.state, "stream must carry only the subscribed session's events")
	case <-ctx.Done():
		t.Fatal("timed out waiting for SSE event")
	}
}

// TestReadOnlySurface_LifecycleUnsupported proves the single-entry adapter
// behind server.New / NewWithSource reports a structured codeReadOnly for the
// lifecycle RPCs (no orchestrator/registry), never nil-derefing, while reads
// keep working.
func TestReadOnlySurface_LifecycleUnsupported(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(server.New(twoTurnTrace(t), testDef()).Handler())
	defer ts.Close()

	for _, m := range []struct {
		method string
		params map[string]any
	}{
		{"runstatus.session.new", map[string]any{"story_path": "/x.yaml"}},
		{"runstatus.session.reload", map[string]any{"session_id": "s-1"}},
		{"runstatus.stories.rescan", map[string]any{}},
	} {
		code, msg := rpcCallExpectError(t, ts, m.method, m.params)
		assert.Equal(t, -32001, code, "%s should report codeReadOnly", m.method)
		assert.Contains(t, msg, "read-only", "%s message should explain the surface is read-only", m.method)
	}

	// stories.list is a tolerant read on the read-only surface: empty, not an error.
	var stories []server.StoryHeader
	rpcCall(t, ts, "runstatus.stories.list", map[string]any{}, &stories)
	assert.Empty(t, stories)

	// Reads still route to the single entry (session_id ignored).
	var hdr runstatus.SessionHeader
	rpcCall(t, ts, "runstatus.session.get", map[string]any{"session_id": "s-1"}, &hdr)
	assert.Equal(t, "s-1", hdr.SessionID)
}
