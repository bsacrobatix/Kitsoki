package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/campaign"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/store"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
)

func TestWireApplicationMaintenanceUsesExactApplicationAndDaemonAuthority(t *testing.T) {
	sessionStore, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })
	_, err = sessionStore.CreateSession(context.Background(), &app.AppDef{
		App: app.AppMeta{ID: "pog-application", Version: "1.0.0"},
	})
	require.NoError(t, err)
	maintenanceJobs, err := jobs.NewJobStore(sessionStore.DB())
	require.NoError(t, err)

	registry := &SessionRegistry{
		cfg: webconfig.WebConfig{
			ApplicationMaintenance: map[string]webconfig.ApplicationMaintenanceConfig{
				"pog-application": {
					SessionReconciliation: &webconfig.SessionReconciliationConfig{
						MaxSessions: 10, MaxJobs: 20,
					},
					WorkerFleet: &webconfig.WorkerFleetConfig{
						MaxWorkers: 10, MaxBytes: 8192,
					},
					CampaignSupervision: &webconfig.CampaignSupervisionConfig{
						MaxCampaigns: 10, MaxBytes: 8192,
						Remediation: webconfig.CampaignRemediationConfig{
							Mode: "propose", MaxProposals: 5,
							Statuses: []string{"failed", "interrupted"},
						},
					},
				},
			},
			Workers: []workerregistry.Entry{{
				ID: "worker-1", Label: "Private operator label",
				Placement: "workstation", Enabled: true,
				Endpoint: "https://private.invalid",
				Tunnel:   nil, CredentialEnv: "PRIVATE_TOKEN",
			}},
		},
		daemonStore:     sessionStore,
		maintenanceJobs: maintenanceJobs,
		campaignStore:   campaign.NewMemoryStore(),
	}
	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireApplicationMaintenance(
		&sessionRuntime{HostRegistry: hostRegistry}, "pog-application",
	)

	for _, verb := range []string{
		host.SessionReconciliationVerb,
		host.WorkerFleetVerb,
		host.CampaignSupervisionVerb,
	} {
		result, invokeErr := hostRegistry.Invoke(context.Background(), verb, nil)
		require.NoError(t, invokeErr, verb)
		require.Empty(t, result.Error, verb)
		require.NotNil(t, result.Data["receipt"], verb)
	}
	workerResult, err := hostRegistry.Invoke(context.Background(), host.WorkerFleetVerb, nil)
	require.NoError(t, err)
	workerJSON, err := json.Marshal(workerResult.Data)
	require.NoError(t, err)
	assert.NotContains(t, string(workerJSON), "private.invalid")
	assert.NotContains(t, string(workerJSON), "PRIVATE_TOKEN")
	assert.NotContains(t, string(workerJSON), "Private operator label")

	otherRegistry := host.NewRegistry()
	host.RegisterBuiltins(otherRegistry)
	registry.wireApplicationMaintenance(
		&sessionRuntime{HostRegistry: otherRegistry}, "other-application",
	)
	result, err := otherRegistry.Invoke(context.Background(), host.SessionReconciliationVerb, nil)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "unavailable")
}
