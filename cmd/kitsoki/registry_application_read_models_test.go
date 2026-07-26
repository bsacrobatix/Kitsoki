package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/host"
	"kitsoki/internal/materializationstatus"
	"kitsoki/internal/store"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
)

func TestWireApplicationReadModelsUsesExactAppAndDaemonOwnedScope(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".kitsoki-root"), nil, 0o644))
	appPath := filepath.Join(root, "stories", "pog-application", "app.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(appPath), 0o755))
	require.NoError(t, os.WriteFile(appPath, []byte("app: {}\n"), 0o644))
	queueDir := filepath.Join(root, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(queueDir, 0o755))
	state := queue.State{Schema: queue.Schema, Candidates: []queue.Candidate{{
		ID: "candidate-pog", ProjectID: "pog", TargetRef: "refs/heads/staging/local",
		TargetPolicy: queue.WaveAutoPolicy, SHA: strings.Repeat("a", 40),
		ValidatedSHA: strings.Repeat("b", 40), ReceiptID: "receipt-pog",
		Status: queue.Gating, Phase: queue.Gating,
	}, {
		ID: "candidate-other", ProjectID: "other", TargetRef: "refs/heads/main",
		TargetPolicy: queue.StewardApprovedPolicy, SHA: strings.Repeat("c", 40),
		ReceiptID: "receipt-other", Status: queue.Queued, Phase: queue.Queued,
	}}}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(queueDir, "state.json"), raw, 0o600))

	sessionStore, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })
	projection, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), nil)
	require.NoError(t, err)
	_, err = projection.Save(context.Background(), materializationstatus.Record{
		ApplicationID: "pog-application", JobID: "materialize-1",
		Status: "done", UpdatedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	registry := &SessionRegistry{
		cfg: webconfig.WebConfig{ApplicationReadModels: map[string]webconfig.ApplicationReadModelConfig{
			"pog-application": {
				Streams:         &webconfig.StreamReadModelConfig{Scope: "pog"},
				Federation:      true,
				Materialization: true,
			},
		}, Workers: []workerregistry.Entry{{
			ID: "worker-1", Label: "Remote worker", Placement: "workstation",
			Endpoint: "https://private.invalid", CredentialEnv: "PRIVATE_TOKEN",
			Enabled: true,
			Capabilities: workerregistry.Capabilities{
				Placements: []string{"workstation"}, Isolation: "capsule", Networks: []string{"restricted"},
			},
		}}},
		daemonJobs:       artifactjob.NewMemoryStore(),
		materializations: projection,
	}
	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireApplicationReadModels(
		&sessionRuntime{HostRegistry: hostRegistry}, "pog-application", appPath,
	)

	streams, err := hostRegistry.Invoke(context.Background(), "host.streams.snapshot", map[string]any{
		"scope": "pog", "max_streams": 10, "max_bytes": 8192,
	})
	require.NoError(t, err)
	streamRows := streams.Data["snapshot"].(map[string]any)["streams"].([]any)
	require.Len(t, streamRows, 1)
	assert.Equal(t, "candidate-pog", streamRows[0].(map[string]any)["id"])
	assert.Equal(t, strings.Repeat("b", 40), streamRows[0].(map[string]any)["immutable_head"])

	federation, err := hostRegistry.Invoke(context.Background(), "host.federation.snapshot", map[string]any{
		"max_workers": 10, "max_bytes": 8192,
	})
	require.NoError(t, err)
	federationJSON, err := json.Marshal(federation.Data)
	require.NoError(t, err)
	assert.NotContains(t, string(federationJSON), "private.invalid")
	assert.NotContains(t, string(federationJSON), "PRIVATE_TOKEN")

	materialization, err := hostRegistry.Invoke(context.Background(), "host.materialization.snapshot", map[string]any{
		"application_id": "pog-application", "max_jobs": 10, "max_bytes": 8192,
	})
	require.NoError(t, err)
	assert.Len(t, materialization.Data["snapshot"].(map[string]any)["jobs"], 1)

	otherRegistry := host.NewRegistry()
	host.RegisterBuiltins(otherRegistry)
	registry.wireApplicationReadModels(
		&sessionRuntime{HostRegistry: otherRegistry}, "other-application", appPath,
	)
	sentinel, err := otherRegistry.Invoke(context.Background(), "host.streams.snapshot", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, sentinel.Error, "unavailable outside configured daemon scope")
}

func TestQueueDeliveryStreamSourceRejectsSymlinkedState(t *testing.T) {
	root := t.TempDir()
	queueDir := filepath.Join(root, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(queueDir, 0o755))
	outside := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"schema":"capsule-merge-queue/v2","candidates":[]}`), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(queueDir, "state.json")))

	_, err := (queueDeliveryStreamSource{
		store: queue.Store{ProjectRoot: root}, projectID: "pog",
	}).ListDeliveryStreams(context.Background(), 10)
	require.ErrorContains(t, err, "contains a symlink")
}

func TestEnableDaemonInterruptsActiveMaterializationsAndRetainsCompleted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "daemon.db")
	sessionStore, err := store.Open(dbPath)
	require.NoError(t, err)
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	projection, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), func() time.Time { return started })
	require.NoError(t, err)
	for _, record := range []materializationstatus.Record{
		{ApplicationID: "pog-application", JobID: "active", Status: "running", UpdatedAt: started},
		{ApplicationID: "pog-application", JobID: "complete", Status: "done", UpdatedAt: started},
	} {
		_, err := projection.Save(context.Background(), record)
		require.NoError(t, err)
	}
	require.NoError(t, sessionStore.Close())

	registry := NewRegistry(webconfig.WebConfig{
		ApplicationReadModels: map[string]webconfig.ApplicationReadModelConfig{
			"pog-application": {Materialization: true},
		},
	}, nil, runtimeBase{})
	require.NoError(t, registry.EnableDaemon(dbPath))
	defer registry.Close()

	restored, appID, ok := registry.MaterializationProjection()
	require.True(t, ok)
	assert.Equal(t, "pog-application", appID)
	active, found, err := restored.Get(context.Background(), appID, "active")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "interrupted", active.Status)
	require.Len(t, active.ReceiptIDs, 1)
	complete, found, err := restored.Get(context.Background(), appID, "complete")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "done", complete.Status)
}
