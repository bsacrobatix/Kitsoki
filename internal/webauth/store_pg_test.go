// Postgres-dialect tests for the webauth Store. Same core paths as
// store_test.go, driven through NewPostgresStore over a per-test database
// from internal/dbruntime/pgtest (skips when no Postgres can start).
package webauth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/dbruntime/pgtest"
)

func newPGTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewPostgresStore(pgtest.Open(t))
	require.NoError(t, err)
	return store
}

func TestPGStore_InviteCreateLookupRedeem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newPGTestStore(t)

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

	// The user now resolves by GitHub id, and shows up in ListInvites'
	// redeemed bookkeeping.
	found, ok, err := store.UserByGitHubID(ctx, 42)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, u.ID, found.ID)

	invites, err := store.ListInvites(ctx)
	require.NoError(t, err)
	require.Len(t, invites, 1)
	require.NotNil(t, invites[0].RedeemedAt)
	assert.Equal(t, u.ID, invites[0].RedeemedBy)
}

func TestPGStore_EnsureAdminAndNoDemotion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newPGTestStore(t)
	gh := GitHubUser{ID: 7, Login: "root", Name: "Root"}
	u, err := store.EnsureAdmin(ctx, gh)
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, u.Role)

	// Calling again refreshes rather than duplicating.
	u2, err := store.EnsureAdmin(ctx, gh)
	require.NoError(t, err)
	assert.Equal(t, u.ID, u2.ID)

	// Redeeming a user-role invite must not demote the existing admin.
	_, code, err := store.CreateInvite(ctx, "Root Again", RoleUser)
	require.NoError(t, err)
	u3, err := store.RedeemInvite(ctx, code, gh)
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, u3.Role, "a user-role invite must not demote an existing admin")
}

func TestPGStore_SessionLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newPGTestStore(t)
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

	// Unknown tokens stay a clean miss.
	_, ok, err = store.SessionUser(ctx, "nope")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestPGStore_SchemaIdempotent(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	for i := 0; i < 3; i++ {
		_, err := NewPostgresStore(db)
		require.NoErrorf(t, err, "NewPostgresStore apply #%d", i+1)
	}
}
