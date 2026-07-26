package materializationstatus_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/materializationstatus"
)

func TestPostgresStoreRestartRetainsTerminalProjectionAndReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store, err := materializationstatus.NewPostgresStore(db, func() time.Time { return started })
	require.NoError(t, err)

	terminal, err := store.Save(ctx, materializationstatus.Record{
		ApplicationID: "pog-application",
		JobID:         "materialize-1",
		SessionID:     "session-1",
		Status:        "done",
		Stages: []materializationstatus.Stage{{
			ID: "publish", Title: "Publish", Status: "complete",
		}},
		Artifacts: []materializationstatus.Artifact{{
			Kind: "report", Title: "Readiness", Handle: "artifact:readiness-1",
		}},
		ReceiptIDs: []string{"ar_0123456789abcdef0123456789abcdef"},
		UpdatedAt:  started,
	})
	require.NoError(t, err)

	restarted, err := materializationstatus.NewPostgresStore(db, nil)
	require.NoError(t, err)
	got, found, err := restarted.Get(ctx, "pog-application", "materialize-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, terminal, got)

	// A delayed active replay after restart cannot regress terminal truth.
	replayed, err := restarted.Save(ctx, materializationstatus.Record{
		ApplicationID: "pog-application",
		JobID:         "materialize-1",
		Status:        "running",
		Stages: []materializationstatus.Stage{{
			ID: "publish", Title: "Publish", Status: "in-progress",
		}},
		UpdatedAt: started.Add(time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, terminal, replayed)

	listed, err := restarted.List(ctx, "pog-application", 10)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, terminal, listed[0])
}

func TestPostgresStoreConcurrentLifecycleIsMonotonic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store, err := materializationstatus.NewPostgresStore(db, nil)
	require.NoError(t, err)
	_, err = store.Save(ctx, materializationstatus.Record{
		ApplicationID: "pog-application", JobID: "materialize-race",
		Status: "running", UpdatedAt: now,
		Stages: []materializationstatus.Stage{{ID: "build", Title: "Build", Status: "waiting"}},
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		status := "in-progress"
		if i%3 == 0 {
			status = "complete"
		}
		wg.Add(1)
		go func(i int, stageStatus string) {
			defer wg.Done()
			_, saveErr := store.Save(ctx, materializationstatus.Record{
				ApplicationID: "pog-application", JobID: "materialize-race",
				Status: "running", UpdatedAt: now.Add(time.Duration(i+1) * time.Millisecond),
				Stages: []materializationstatus.Stage{{
					ID: "build", Title: "Build", Status: stageStatus,
				}},
			})
			errs <- saveErr
		}(i, status)
	}
	wg.Wait()
	close(errs)
	for saveErr := range errs {
		require.NoError(t, saveErr)
	}

	got, found, err := store.Get(ctx, "pog-application", "materialize-race")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.Stages, 1)
	assert.Equal(t, "complete", got.Stages[0].Status)

	got.Status = "done"
	got.UpdatedAt = now.Add(time.Minute)
	terminal, err := store.Save(ctx, got)
	require.NoError(t, err)
	late := got
	late.Stages = append([]materializationstatus.Stage(nil), got.Stages...)
	late.Status = "running"
	late.Stages[0].Status = "waiting"
	late.UpdatedAt = now.Add(2 * time.Minute)
	replayed, err := store.Save(ctx, late)
	require.NoError(t, err)
	assert.Equal(t, terminal, replayed)
}

func TestPostgresStoreRestartInterruptsActiveAndRetainsCompleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	restartedAt := started.Add(time.Minute)
	store, err := materializationstatus.NewPostgresStore(db, func() time.Time { return restartedAt })
	require.NoError(t, err)
	for i, status := range []string{"running", "awaiting_input", "done"} {
		_, err := store.Save(ctx, materializationstatus.Record{
			ApplicationID: "pog-application",
			JobID:         fmt.Sprintf("job-%d", i),
			Status:        status,
			UpdatedAt:     started,
		})
		require.NoError(t, err)
	}

	restarted, err := materializationstatus.NewPostgresStore(db, func() time.Time { return restartedAt })
	require.NoError(t, err)
	count, err := restarted.InterruptActive(ctx, "daemon_restarted")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	for i := 0; i < 2; i++ {
		record, found, err := restarted.Get(ctx, "pog-application", fmt.Sprintf("job-%d", i))
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, "interrupted", record.Status)
		assert.Equal(t, restartedAt, record.UpdatedAt)
		require.Len(t, record.ReceiptIDs, 1)
		assert.Regexp(t, `^mr_[a-f0-9]{32}$`, record.ReceiptIDs[0])
	}
	completed, found, err := restarted.Get(ctx, "pog-application", "job-2")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "done", completed.Status)
	assert.Equal(t, started, completed.UpdatedAt)
}
