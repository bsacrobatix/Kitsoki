package study

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/dbruntime/pgtest"
)

// Mirrors TestSQLiteRestartRetainsIdentityEventsRetryAndBudget against
// Postgres: identity, budget, retries, and — critically — the exact event
// sequence must survive a restart, because runstatus.study.events
// {since_sequence} pages on that ordering.
func TestPostgresRestartRetainsIdentityEventsRetryAndBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	s, err := NewPostgresStore(db)
	require.NoError(t, err)
	now := time.Date(2026, 7, 16, 1, 2, 3, 0, time.UTC)
	s.memory.SetClock(func() time.Time { return now })
	st, created, err := s.Submit(ctx, SubmitRequest{IdempotencyKey: "same-request", Plan: testPlan()})
	require.NoError(t, err)
	require.True(t, created)
	snap, err := s.Get(ctx, st.ID)
	require.NoError(t, err)
	require.Equal(t, CellBlocked, snap.Cells[1].Phase)
	a := snap.Cells[0].Attempts[0]
	require.NoError(t, s.Record(ctx, st.ID, "source", a.ID, Result{ResultDigest: "sha256:source", Cost: 2}))
	require.NoError(t, s.Record(ctx, st.ID, "analysis", snap.Cells[1].Attempts[0].ID, Result{FailureKind: "access"}))
	retry, err := s.Retry(ctx, st.ID, "analysis")
	require.NoError(t, err)
	require.Equal(t, 2, retry.Number)
	restarted, err := NewPostgresStore(db)
	require.NoError(t, err)
	got, err := restarted.Get(ctx, st.ID)
	require.NoError(t, err)
	require.Equal(t, 2.0, got.Study.Budget.Used)
	require.Len(t, got.Cells[1].Attempts, 2)
	events, err := restarted.Events(ctx, st.ID, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 5)
	for i, e := range events {
		require.Equal(t, int64(i+1), e.Sequence)
	}
	// The since_sequence cursor stays exact across the restart: asking for
	// events after N returns the identical suffix the pre-restart store held.
	before, err := s.Events(ctx, st.ID, 3)
	require.NoError(t, err)
	after, err := restarted.Events(ctx, st.ID, 3)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, int64(4), after[0].Sequence)
	again, created, err := restarted.Submit(ctx, SubmitRequest{IdempotencyKey: "same-request", Plan: testPlan()})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, st.ID, again.ID)
}
