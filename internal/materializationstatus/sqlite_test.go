package materializationstatus_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/materializationstatus"
	"kitsoki/internal/store"
)

func TestSQLiteStorePersistsScopedTerminalProjection(t *testing.T) {
	sessionStore, err := store.Open(t.TempDir() + "/sessions.db")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	projection, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), func() time.Time { return now })
	require.NoError(t, err)
	saved, err := projection.Save(context.Background(), materializationstatus.Record{
		ApplicationID: "pog-application",
		JobID:         "job-1",
		SessionID:     "session-1",
		Status:        "done",
		Stages: []materializationstatus.Stage{{
			ID: "render", Title: "Render artifact", Status: "complete",
		}},
		Artifacts: []materializationstatus.Artifact{{
			Kind: "report", Title: "Readiness report", Handle: "ma_0123456789abcdef",
		}},
		UpdatedAt: now,
	})
	require.NoError(t, err)
	require.Len(t, saved.ReceiptIDs, 1)
	assert.Regexp(t, `^mr_[a-f0-9]{32}$`, saved.ReceiptIDs[0])

	reopened, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), nil)
	require.NoError(t, err)
	got, found, err := reopened.Get(context.Background(), "pog-application", "job-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, saved, got)

	other, err := reopened.List(context.Background(), "other-application", 10)
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestSQLiteStoreRestartInterruptsOnlyActiveJobs(t *testing.T) {
	sessionStore, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })

	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	restarted := started.Add(time.Minute)
	projection, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), func() time.Time { return restarted })
	require.NoError(t, err)
	for _, record := range []materializationstatus.Record{
		{ApplicationID: "app-a", JobID: "running-job", Status: "running", UpdatedAt: started},
		{ApplicationID: "app-a", JobID: "done-job", Status: "done", UpdatedAt: started},
		{ApplicationID: "app-b", JobID: "waiting-job", Status: "awaiting_input", UpdatedAt: started},
	} {
		_, err := projection.Save(context.Background(), record)
		require.NoError(t, err)
	}

	count, err := projection.InterruptActive(context.Background(), "daemon_restarted")
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	running, found, err := projection.Get(context.Background(), "app-a", "running-job")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "interrupted", running.Status)
	assert.Equal(t, restarted, running.UpdatedAt)
	require.Len(t, running.ReceiptIDs, 1)

	done, found, err := projection.Get(context.Background(), "app-a", "done-job")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "done", done.Status)
	assert.Equal(t, started, done.UpdatedAt)
}

func TestSQLiteStoreRejectsPathBearingProjectionAndIsRaceSafe(t *testing.T) {
	sessionStore, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })
	projection, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), nil)
	require.NoError(t, err)

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	_, err = projection.Save(context.Background(), materializationstatus.Record{
		ApplicationID: "app-a", JobID: "job-path", Status: "done", UpdatedAt: now,
		Artifacts: []materializationstatus.Artifact{{Kind: "report", Title: "Report", Handle: "/tmp/private/report.md"}},
	})
	require.ErrorIs(t, err, materializationstatus.ErrInvalid)

	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, saveErr := projection.Save(context.Background(), materializationstatus.Record{
				ApplicationID: "app-a", JobID: "job-race", Status: "running", UpdatedAt: now,
			})
			errs <- saveErr
		}()
	}
	wg.Wait()
	close(errs)
	for saveErr := range errs {
		require.NoError(t, saveErr)
	}
	got, found, err := projection.Get(context.Background(), "app-a", "job-race")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "running", got.Status)
}

func TestSQLiteStoreLifecycleNeverRegresses(t *testing.T) {
	sessionStore, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })
	projection, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), nil)
	require.NoError(t, err)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	base := materializationstatus.Record{
		ApplicationID: "app-a", JobID: "job-monotonic", Status: "running", UpdatedAt: now,
		Stages: []materializationstatus.Stage{{ID: "build", Title: "Build", Status: "complete"}},
	}
	_, err = projection.Save(context.Background(), base)
	require.NoError(t, err)
	regressed := base
	regressed.Stages = append([]materializationstatus.Stage(nil), base.Stages...)
	regressed.Stages[0].Status = "waiting"
	saved, err := projection.Save(context.Background(), regressed)
	require.NoError(t, err)
	assert.Equal(t, "complete", saved.Stages[0].Status)

	terminal := base
	terminal.Stages = append([]materializationstatus.Stage(nil), base.Stages...)
	terminal.Status = "done"
	terminal.UpdatedAt = now.Add(time.Second)
	saved, err = projection.Save(context.Background(), terminal)
	require.NoError(t, err)
	assert.Equal(t, "done", saved.Status)

	lateRunning := base
	lateRunning.UpdatedAt = now.Add(2 * time.Second)
	saved, err = projection.Save(context.Background(), lateRunning)
	require.NoError(t, err)
	assert.Equal(t, "done", saved.Status)
	assert.Equal(t, terminal.UpdatedAt, saved.UpdatedAt)
}
