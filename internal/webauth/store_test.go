package webauth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	store, closeDB, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeDB() })
	return store
}

func TestStore_InviteCreateLookupRedeem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	inv, code, err := store.CreateInvite(ctx, "Ana", RoleAdmin)
	require.NoError(t, err)
	assert.NotEmpty(t, inv.ID)
	assert.NotEmpty(t, code)

	got, err := store.LookupInvite(ctx, code)
	require.NoError(t, err)
	assert.Equal(t, inv.ID, got.ID)
	assert.Equal(t, "Ana", got.Name)
	assert.Equal(t, RoleAdmin, got.Role)

	gh := GitHubUser{ID: 42, Login: "ana-gh", Name: "Ana Real Name"}
	u, err := store.RedeemInvite(ctx, code, gh)
	require.NoError(t, err)
	assert.Equal(t, "ana-gh", u.GitHubLogin)
	assert.Equal(t, RoleAdmin, u.Role, "redeemed user inherits the invite's role")

	// Redeeming twice must fail — the invite is now consumed.
	_, err = store.RedeemInvite(ctx, code, gh)
	assert.ErrorIs(t, err, ErrInviteNotFound)

	// A redeemed invite no longer resolves via LookupInvite either.
	_, err = store.LookupInvite(ctx, code)
	assert.ErrorIs(t, err, ErrInviteNotFound)

	// The user now resolves by GitHub id.
	found, ok, err := store.UserByGitHubID(ctx, 42)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, u.ID, found.ID)
}

func TestStore_LookupInvite_UnknownCode(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	_, err := store.LookupInvite(context.Background(), "does-not-exist")
	assert.ErrorIs(t, err, ErrInviteNotFound)
}

func TestStore_CreateInvite_InvalidRole(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	_, _, err := store.CreateInvite(context.Background(), "X", "superuser")
	assert.Error(t, err)
}

func TestStore_EnsureAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)
	gh := GitHubUser{ID: 7, Login: "root", Name: "Root"}
	u, err := store.EnsureAdmin(ctx, gh)
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, u.Role)

	// Calling again refreshes rather than duplicating.
	u2, err := store.EnsureAdmin(ctx, gh)
	require.NoError(t, err)
	assert.Equal(t, u.ID, u2.ID)
}

func TestStore_RedeemInvite_UserRoleNeverDemotesExistingAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)
	gh := GitHubUser{ID: 9, Login: "boss", Name: "Boss"}
	_, err := store.EnsureAdmin(ctx, gh)
	require.NoError(t, err)

	_, code, err := store.CreateInvite(ctx, "Boss Again", RoleUser)
	require.NoError(t, err)
	u, err := store.RedeemInvite(ctx, code, gh)
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, u.Role, "a user-role invite must not demote an existing admin")
}

func TestStore_SessionLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)
	gh := GitHubUser{ID: 1, Login: "sess-user", Name: "Sess"}
	u, err := store.EnsureAdmin(ctx, gh)
	require.NoError(t, err)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	token, err := store.CreateSession(ctx, u.ID, time.Hour)
	require.NoError(t, err)

	got, ok, err := store.SessionUser(ctx, token)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, u.ID, got.ID)

	// Advance past expiry.
	store.SetClock(func() time.Time { return now.Add(2 * time.Hour) })
	_, ok, err = store.SessionUser(ctx, token)
	require.NoError(t, err)
	assert.False(t, ok, "expired session must not resolve")

	// Logout.
	store.SetClock(func() time.Time { return now })
	token2, err := store.CreateSession(ctx, u.ID, time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.DeleteSession(ctx, token2))
	_, ok, err = store.SessionUser(ctx, token2)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestStore_SessionUser_UnknownToken(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	_, ok, err := store.SessionUser(context.Background(), "nope")
	require.NoError(t, err)
	assert.False(t, ok)
}
