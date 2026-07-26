package applicationmaintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/campaign"
	"kitsoki/internal/jobs"
	"kitsoki/internal/store"
)

type sessionSourceStub struct {
	sessions []store.SessionSummary
}

func (s sessionSourceStub) ListSessions(context.Context, string, int) ([]store.SessionSummary, error) {
	return append([]store.SessionSummary(nil), s.sessions...), nil
}

type jobReconcilerStub struct {
	result jobs.ProcessBoundReconcileResult
}

func (s jobReconcilerStub) ReconcileProcessBoundJobs(
	context.Context,
	[]app.SessionID,
	int,
) (jobs.ProcessBoundReconcileResult, error) {
	return s.result, nil
}

type workerSourceStub struct {
	workers []WorkerObservation
}

func (s workerSourceStub) ObserveWorkers(context.Context) ([]WorkerObservation, error) {
	return append([]WorkerObservation(nil), s.workers...), nil
}

type campaignStoreStub struct {
	schedules []campaign.Schedule
}

func (s campaignStoreStub) List(context.Context, string, int) ([]campaign.Schedule, error) {
	return append([]campaign.Schedule(nil), s.schedules...), nil
}

func TestSessionServiceReportsOnlyPrivacySafeRestartTruth(t *testing.T) {
	service := SessionService{
		ApplicationID: "pog-application",
		Sessions: sessionSourceStub{sessions: []store.SessionSummary{{
			ID: "private-session-id", AppID: "pog-application", Status: "active",
		}}},
		Jobs: jobReconcilerStub{result: jobs.ProcessBoundReconcileResult{
			Examined: 3, Interrupted: 2, Deferred: 1,
			RestartTruth: "cross_host_lease_required",
		}},
		MaxSessions: 10,
		MaxJobs:     20,
	}
	receipt, err := service.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SessionReceiptSchema, receipt["schema"])
	assert.Equal(t, "deferred", receipt["status"])
	assert.Equal(t, 2, receipt["interrupted_jobs"])
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-session-id")
}

func TestSessionServiceRejectsForeignSessionAndBounds(t *testing.T) {
	service := SessionService{
		ApplicationID: "pog-application",
		Sessions: sessionSourceStub{sessions: []store.SessionSummary{{
			ID: "foreign", AppID: "other",
		}}},
		Jobs:        jobReconcilerStub{},
		MaxSessions: 10,
		MaxJobs:     20,
	}
	_, err := service.Reconcile(context.Background())
	assert.ErrorContains(t, err, "foreign application")

	service.Sessions = sessionSourceStub{sessions: make([]store.SessionSummary, 2)}
	service.MaxSessions = 1
	_, err = service.Reconcile(context.Background())
	assert.ErrorContains(t, err, "refusing to truncate")
}

func TestWorkerServiceIsObserveOnlyAndCannotCarryTransportAuthority(t *testing.T) {
	service := WorkerService{
		ApplicationID: "pog-application",
		Source: workerSourceStub{workers: []WorkerObservation{{
			ID: "worker-1", Placement: "workstation", Health: "online",
			Enabled: true, Jobs: 2,
			Capabilities: WorkerCapabilities{
				Placements: []string{"workstation"},
				Isolation:  "capsule",
				Networks:   []string{"restricted"},
			},
		}}},
		MaxWorkers: 10,
		MaxBytes:   DefaultMaxReceiptBytes,
	}
	receipt, err := service.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, WorkerReceiptSchema, receipt["schema"])
	assert.Equal(t, true, receipt["observe_only"])
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	for _, private := range []string{"endpoint", "tunnel", "credential", "url", "last_error"} {
		assert.NotContains(t, string(raw), private)
	}

	service.MaxBytes = 1
	_, err = service.Reconcile(context.Background())
	assert.ErrorContains(t, err, "refusing to truncate")
	service.MaxBytes = DefaultMaxReceiptBytes
	service.Source = workerSourceStub{workers: []WorkerObservation{{
		ID: "worker-1", Placement: "workstation", Health: "online",
		Capabilities: WorkerCapabilities{Networks: []string{"https://secret.invalid"}},
	}}}
	_, err = service.Reconcile(context.Background())
	assert.ErrorContains(t, err, "invalid or duplicate")
}

func TestCampaignServiceProducesBoundedProposalOnlyRemediation(t *testing.T) {
	schedules := []campaign.Schedule{{
		Definition: campaign.Definition{
			ID: "failed-campaign", AppID: "pog-application", Enabled: true,
		},
		LastStatus: "failed",
		LastError:  "/private/path: bearer secret",
	}, {
		Definition: campaign.Definition{
			ID: "paused-campaign", AppID: "pog-application", Enabled: true, Paused: true,
		},
	}, {
		Definition: campaign.Definition{
			ID: "interrupted-campaign", AppID: "pog-application", Enabled: true,
		},
		LastStatus: "interrupted",
	}}
	service := CampaignService{
		ApplicationID: "pog-application",
		Store:         campaignStoreStub{schedules: schedules},
		MaxCampaigns:  10,
		Policy: CampaignPolicy{
			MaxProposals: 2,
			Statuses:     []string{"failed", "interrupted"},
		},
		MaxBytes: DefaultMaxReceiptBytes,
	}
	receipt, err := service.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, CampaignReceiptSchema, receipt["schema"])
	assert.Equal(t, "attention", receipt["status"])
	assert.Equal(t, true, receipt["proposal_only"])
	assert.Len(t, receipt["remediation_proposals"], 2)
	assert.Equal(t, 1, receipt["omitted_proposals"])
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "/private/path")
	assert.NotContains(t, string(raw), "secret")
	assert.NotContains(t, string(raw), "command")
}
