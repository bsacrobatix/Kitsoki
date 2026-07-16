package study

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func testPlan() Plan {
	return Plan{Revision: "rev-1", Digest: "sha256:plan", Budget: Budget{Limit: 10, Currency: "USD"}, Waves: []Wave{{ID: "wave-1", Order: 1, Cells: []CellPlan{{ID: "source"}}}, {ID: "wave-2", Order: 2, Cells: []CellPlan{{ID: "analysis", DependsOn: []string{"source"}}}}}}
}
func TestSQLiteRestartRetainsIdentityEventsRetryAndBudget(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/studies.db")
	require.NoError(t, err)
	defer db.Close()
	s, err := NewSQLiteStore(db)
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
	restarted, err := NewSQLiteStore(db)
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
	again, created, err := restarted.Submit(ctx, SubmitRequest{IdempotencyKey: "same-request", Plan: testPlan()})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, st.ID, again.ID)
}
