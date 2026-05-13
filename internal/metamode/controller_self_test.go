package metamode

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/agents"
	"kitsoki/internal/app"
)

// withSelfMode is an option for newTestController that adds a `self`
// meta mode + kitsoki-engineer agent (skeleton; just enough to drive
// cross-app keying tests).
func withSelfMode(c *Controller) {
	if c.AppDef == nil {
		c.AppDef = &app.AppDef{App: app.AppMeta{ID: "test-app"}}
	}
	if c.AppDef.MetaModes == nil {
		c.AppDef.MetaModes = map[string]*app.MetaModeDef{}
	}
	c.AppDef.MetaModes["self"] = &app.MetaModeDef{
		Trigger: "self",
		Label:   "Edit kitsoki",
		Agent:   "kitsoki-engineer",
		Cwd:     "/tmp/kitsoki-repo-fake",
	}
	// Register the agent in whatever fake registry the test wired.
	c.Agents.Register(agents.Agent{
		Name:         "kitsoki-engineer",
		SystemPrompt: "fake engineer prompt",
		Tools:        []string{"Bash", "Read", "Write", "Edit"},
		DefaultCwd:   "/tmp/kitsoki-repo-fake",
	})
}

// TestMetaAppID checks the helper that picks the appID for chat
// resolution: SelfAppID for `self`, the running app's id for anything
// else.
func TestMetaAppID(t *testing.T) {
	require.Equal(t, SelfAppID, metaAppID("self", "running-app"),
		"self mode keys against SelfAppID regardless of running app")
	require.Equal(t, "running-app", metaAppID("story", "running-app"),
		"non-self modes key against the running app")
	require.Equal(t, "running-app", metaAppID("bug", "running-app"),
		"bug mode is per-app, not cross-app")
}

// TestMetaScopeKey checks the helper that picks the scope_key: empty
// for `self` (one conversation, cross-app), the state path for
// everything else.
func TestMetaScopeKey(t *testing.T) {
	require.Equal(t, "", metaScopeKey("self", "foyer"),
		"self uses an empty scope_key — one conversation per user")
	require.Equal(t, "foyer", metaScopeKey("story", "foyer"),
		"non-self modes key against state path")
}

// TestController_Enter_SelfModeKeysCrossApp asserts that entering
// `self` from a running app `cloak` resolves the chat under SelfAppID
// (not the running app's id) and with an empty scope_key (not the
// state path).
func TestController_Enter_SelfModeKeysCrossApp(t *testing.T) {
	c, store, _ := newTestController(t, withSelfMode)
	c.AppDef.App.ID = "cloak" // simulate running cloak
	snap := makeSnapshot("foyer")

	_, err := c.Enter(context.Background(), snap, "self")
	require.NoError(t, err)

	require.Equal(t, SelfAppID, store.gotAppID,
		"self mode must resolve chat under SelfAppID, not the running app's id")
	require.Equal(t, "meta:self", store.gotRoom)
	require.Equal(t, "", store.gotScopeKey,
		"self chats use an empty scope_key for cross-app continuity")
}

// TestController_Enter_NonSelfModesUnchanged asserts the keying
// behaviour for non-self modes is unaffected by the cross-app shim.
func TestController_Enter_NonSelfModesUnchanged(t *testing.T) {
	c, store, _ := newTestController(t)
	c.AppDef.App.ID = "cloak"
	snap := makeSnapshot("foyer")

	_, err := c.Enter(context.Background(), snap, "story")
	require.NoError(t, err)

	require.Equal(t, "cloak", store.gotAppID,
		"non-self modes must still key against the running app")
	require.Equal(t, "foyer", store.gotScopeKey,
		"non-self modes still key by state path")
}

// TestController_EnterByChatID_AcceptsSelfFromAnyApp asserts that a
// chat row whose AppID() is SelfAppID can be resumed from a running
// app with a different id.
func TestController_EnterByChatID_AcceptsSelfFromAnyApp(t *testing.T) {
	c, store, _ := newTestController(t, withSelfMode)
	c.AppDef.App.ID = "dev-story" // running app

	// Seed a self chat created from a different app earlier.
	selfChat := &fakeChat{
		id:       "self-chat-id-123",
		appID:    SelfAppID,
		room:     "meta:self",
		scopeKey: "",
		title:    "Edit kitsoki",
	}
	store.rows = append(store.rows, selfChat)

	snap := makeSnapshot("foyer")
	s, err := c.EnterByChatID(context.Background(), snap, "self", "self-chat-id-123")
	require.NoError(t, err, "self chat must be resumable from any running app")
	require.NotNil(t, s)
	require.Equal(t, "self-chat-id-123", s.Chat.ID())
}

// TestController_EnterByChatID_RejectsForeignAppChat asserts the
// guardrail still rejects a chat that belongs to neither the running
// app nor SelfAppID (catches a genuine app mismatch / dangling id).
func TestController_EnterByChatID_RejectsForeignAppChat(t *testing.T) {
	c, store, _ := newTestController(t, withSelfMode)
	c.AppDef.App.ID = "dev-story"

	// Seed a chat belonging to some other app.
	foreign := &fakeChat{
		id:       "foreign-id",
		appID:    "totally-other-app",
		room:     "meta:self",
		scopeKey: "",
		title:    "stranger",
	}
	store.rows = append(store.rows, foreign)

	snap := makeSnapshot("foyer")
	_, err := c.EnterByChatID(context.Background(), snap, "self", "foreign-id")
	require.Error(t, err, "chat belonging to a different app must still be rejected")
}

// TestController_ListChats_MergesSelfChats asserts that calling
// ListChats with the running app's id pulls in cross-app self chats
// too, so /meta list surfaces them without the user needing to know
// the synthetic SelfAppID.
func TestController_ListChats_MergesSelfChats(t *testing.T) {
	c, store, _ := newTestController(t, withSelfMode)
	c.AppDef.App.ID = "cloak"

	// One app-scoped chat under `cloak` and one self chat under
	// SelfAppID. Different updated-at so the sort order is deterministic.
	cloakChat := &fakeChat{
		id:        "cloak-chat",
		appID:     "cloak",
		room:      "meta:story",
		scopeKey:  "foyer",
		title:     "improve the story",
		updatedAt: time.Unix(2_000, 0).UTC(),
	}
	selfChat := &fakeChat{
		id:        "self-chat",
		appID:     SelfAppID,
		room:      "meta:self",
		scopeKey:  "",
		title:     "Edit kitsoki",
		updatedAt: time.Unix(3_000, 0).UTC(), // newer
	}
	store.rows = append(store.rows, cloakChat, selfChat)

	got, err := c.ListChats(context.Background(), "cloak")
	require.NoError(t, err)
	require.Len(t, got, 2, "ListChats must merge cross-app self chats with the running app's chats")

	ids := []string{got[0].ID, got[1].ID}
	sort.Strings(ids)
	require.Equal(t, []string{"cloak-chat", "self-chat"}, ids,
		"both rows must appear in the merged listing")
	// Self chat is newer, so it sorts first.
	require.Equal(t, "self-chat", got[0].ID, "newer chat must sort first (UpdatedAt desc)")
}

// TestController_ListChats_SelfAppIDPassthrough asserts that asking
// for SelfAppID explicitly returns ONLY self chats (no double-list).
func TestController_ListChats_SelfAppIDPassthrough(t *testing.T) {
	c, store, _ := newTestController(t, withSelfMode)
	c.AppDef.App.ID = "cloak"

	selfChat := &fakeChat{
		id:        "self-chat",
		appID:     SelfAppID,
		room:      "meta:self",
		scopeKey:  "",
		title:     "Edit kitsoki",
		updatedAt: time.Unix(3_000, 0).UTC(),
	}
	store.rows = append(store.rows, selfChat)

	got, err := c.ListChats(context.Background(), SelfAppID)
	require.NoError(t, err)
	require.Len(t, got, 1, "asking explicitly for SelfAppID must not double-list")
	require.Equal(t, "self-chat", got[0].ID)
}
