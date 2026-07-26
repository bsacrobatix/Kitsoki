package webconfig

import (
	"strings"
	"testing"

	"kitsoki/internal/applicationmaintenance"
)

func TestApplicationMaintenanceDefaultsAndLocalMerge(t *testing.T) {
	base, err := loadConfigText(t, `campaigns:
  catalog: graph/catalog.yaml
application_maintenance:
  pog-application:
    session_reconciliation: {}
    worker_fleet: {}
    campaign_supervision:
      remediation: {}
`)
	if err != nil {
		t.Fatal(err)
	}
	binding := base.ApplicationMaintenance["pog-application"]
	if binding.SessionReconciliation.MaxSessions != applicationmaintenance.DefaultMaxSessions ||
		binding.SessionReconciliation.MaxJobs != applicationmaintenance.DefaultMaxSessionJobs ||
		binding.WorkerFleet.MaxWorkers != applicationmaintenance.DefaultMaxWorkers ||
		binding.WorkerFleet.MaxBytes != applicationmaintenance.DefaultMaxReceiptBytes ||
		binding.CampaignSupervision.MaxCampaigns != applicationmaintenance.DefaultMaxCampaigns ||
		binding.CampaignSupervision.MaxBytes != applicationmaintenance.DefaultMaxReceiptBytes ||
		binding.CampaignSupervision.Remediation.Mode != "propose" ||
		binding.CampaignSupervision.Remediation.MaxProposals != applicationmaintenance.DefaultMaxRemediations {
		t.Fatalf("defaults = %#v", binding)
	}
	local := WebConfig{ApplicationMaintenance: map[string]ApplicationMaintenanceConfig{
		"other-application": {WorkerFleet: &WorkerFleetConfig{MaxWorkers: 10}},
	}}
	merged := mergeConfig(base, local)
	if len(merged.ApplicationMaintenance) != 2 {
		t.Fatalf("merged application maintenance = %#v", merged.ApplicationMaintenance)
	}
}

func TestApplicationMaintenanceRejectsUnboundedOrMutatingPolicy(t *testing.T) {
	for _, body := range []string{
		"application_maintenance:\n  /tmp/app:\n    worker_fleet: {}\n",
		"application_maintenance:\n  app:\n    session_reconciliation:\n      max_sessions: 1000\n",
		"application_maintenance:\n  app:\n    worker_fleet:\n      max_workers: 1000\n",
		"application_maintenance:\n  app:\n    worker_fleet:\n      max_bytes: 9999999\n",
		"application_maintenance:\n  app:\n    campaign_supervision: {}\n",
		"campaigns:\n  catalog: graph/catalog.yaml\napplication_maintenance:\n  app:\n    campaign_supervision:\n      remediation:\n        mode: execute\n",
		"campaigns:\n  catalog: graph/catalog.yaml\napplication_maintenance:\n  app:\n    campaign_supervision:\n      max_bytes: 9999999\n",
		"campaigns:\n  catalog: graph/catalog.yaml\napplication_maintenance:\n  app:\n    campaign_supervision:\n      remediation:\n        statuses: [failed, success]\n",
	} {
		_, err := loadConfigText(t, body)
		if err == nil || !strings.Contains(err.Error(), "application_maintenance") {
			t.Fatalf("body %q error = %v", body, err)
		}
	}
}
