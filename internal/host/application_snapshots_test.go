package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
	"kitsoki/internal/materializationstatus"
	"kitsoki/internal/store"
)

type deliveryStreamSourceFunc func(context.Context, int) ([]DeliveryStreamRecord, error)

func (f deliveryStreamSourceFunc) ListDeliveryStreams(ctx context.Context, limit int) ([]DeliveryStreamRecord, error) {
	return f(ctx, limit)
}

type federationSourceFunc func(context.Context) (FederationProjection, error)

func (f federationSourceFunc) FederationSnapshot(ctx context.Context) (FederationProjection, error) {
	return f(ctx)
}

func TestApplicationSnapshotSentinelsFailClosed(t *testing.T) {
	for name, handler := range map[string]Handler{
		"streams":         StreamsSnapshotHandler,
		"federation":      FederationSnapshotHandler,
		"materialization": MaterializationSnapshotHandler,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := handler(context.Background(), map[string]any{"op": "snapshot"})
			require.NoError(t, err)
			assert.Contains(t, result.Error, "unavailable outside configured daemon scope")
		})
	}
}

func TestApplicationSnapshotSchemasAndEffectsAreReadOnly(t *testing.T) {
	for namespace, requiredInput := range map[string]string{
		"host.streams": "max_streams", "host.federation": "max_workers",
		"host.materialization": "max_jobs",
	} {
		class, deterministic := ClassifyDispatchedCall(namespace, map[string]any{"op": "snapshot"})
		assert.Equal(t, effect.Read, class, namespace)
		assert.True(t, deterministic, namespace)
		class, deterministic = ClassifyDispatchedCall(namespace+".snapshot", nil)
		assert.Equal(t, effect.Read, class, namespace+".snapshot")
		assert.True(t, deterministic, namespace+".snapshot")
		spec, ok := opschema.Builtins().Lookup(namespace, "snapshot")
		require.True(t, ok, namespace)
		assert.Equal(t, "int", spec.Input[requiredInput].Type)
		assert.Equal(t, "int", spec.Input["max_bytes"].Type)
		assert.Equal(t, "object", spec.Output["snapshot"].Type)
	}
}

func TestStreamsSnapshotIsScopedBoundedAndStrict(t *testing.T) {
	source := deliveryStreamSourceFunc(func(_ context.Context, limit int) ([]DeliveryStreamRecord, error) {
		assert.Equal(t, 4, limit)
		valid := DeliveryStreamRecord{
			ID: "candidate-1", Lane: "staging", State: "gating",
			ImmutableHead: strings.Repeat("a", 40), TargetRef: "refs/heads/staging/local",
			ProposalState: "receipt-admitted", GateStatus: "running",
			NextAction: "advance", ReceiptRef: "receipt-1",
		}
		duplicate := valid
		invalid := valid
		invalid.ID = "/tmp/private"
		return []DeliveryStreamRecord{valid, duplicate, invalid}, nil
	})
	handler := NewStreamsSnapshotHandler(source, "pog-application", "pog")
	result, err := handler(context.Background(), map[string]any{
		"op": "snapshot", "scope": "pog", "max_streams": 3, "max_bytes": 8192,
	})
	require.NoError(t, err)
	snapshot := result.Data["snapshot"].(map[string]any)
	assert.Equal(t, "pog-application", snapshot["application_id"])
	assert.Len(t, snapshot["streams"], 1)
	assert.Equal(t, 2, snapshot["invalid_count"])
	assert.NotContains(t, snapshot, "path")

	_, err = handler(context.Background(), map[string]any{
		"op": "snapshot", "scope": "other", "max_streams": 3, "max_bytes": 8192,
	})
	assert.ErrorContains(t, err, "does not match server-bound scope")
	_, err = handler(context.Background(), map[string]any{
		"op": "snapshot", "scope": "pog", "max_streams": 3, "max_bytes": 8192,
		"path": "/tmp/private",
	})
	assert.ErrorContains(t, err, `unsupported arg "path"`)
	_, err = handler(context.Background(), map[string]any{
		"op": "snapshot", "scope": "pog", "max_streams": 3, "max_bytes": 1,
	})
	assert.ErrorContains(t, err, "refusing to truncate")
}

func TestFederationSnapshotExposesOnlyPublicHealthAndPolicy(t *testing.T) {
	source := federationSourceFunc(func(context.Context) (FederationProjection, error) {
		return FederationProjection{
			Workers: []FederationWorker{{
				ID: "worker-1", Label: "Linux builder", Placement: "remote",
				Health: "healthy", Enabled: true, Jobs: 2,
				Capabilities: FederationCapabilities{
					Placements: []string{"remote"}, Isolation: "capsule", Networks: []string{"restricted"},
				},
			}},
			Policy: []FederationPolicy{{
				Lane: "delivery", WorkerClasses: []string{"remote"}, NetworkProfiles: []string{"restricted"},
			}},
		}, nil
	})
	handler := NewFederationSnapshotHandler(source, "pog-application")
	result, err := handler(context.Background(), map[string]any{
		"op": "snapshot", "max_workers": 10, "max_bytes": 8192,
	})
	require.NoError(t, err)
	snapshot := result.Data["snapshot"].(map[string]any)
	assert.Len(t, snapshot["workers"], 1)
	assert.NotContains(t, snapshot, "endpoint")
	assert.NotContains(t, snapshot, "credential")
	assert.NotContains(t, snapshot, "tunnel")
}

func TestMaterializationSnapshotUsesExactApplicationAndOpaqueArtifacts(t *testing.T) {
	sessionStore, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })
	projection, err := materializationstatus.NewSQLiteStore(sessionStore.DB(), nil)
	require.NoError(t, err)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	for _, appID := range []string{"pog-application", "other-application"} {
		_, err := projection.Save(context.Background(), materializationstatus.Record{
			ApplicationID: appID, JobID: "job-" + appID, Status: "done", UpdatedAt: now,
			Artifacts: []materializationstatus.Artifact{{
				Kind: "report", Title: "Readiness", Handle: "ma_" + appID,
			}},
		})
		require.NoError(t, err)
	}

	handler := NewMaterializationSnapshotHandler(projection, "pog-application")
	result, err := handler(context.Background(), map[string]any{
		"op": "snapshot", "application_id": "pog-application", "max_jobs": 10, "max_bytes": 8192,
	})
	require.NoError(t, err)
	snapshot := result.Data["snapshot"].(map[string]any)
	jobs := snapshot["jobs"].([]any)
	require.Len(t, jobs, 1)
	assert.Equal(t, "job-pog-application", jobs[0].(map[string]any)["job_id"])

	_, err = handler(context.Background(), map[string]any{
		"op": "snapshot", "application_id": "other-application", "max_jobs": 10, "max_bytes": 8192,
	})
	assert.ErrorContains(t, err, "does not match server-bound application")
}
