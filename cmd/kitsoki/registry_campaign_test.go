package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

func TestRegistryCampaignProviderIsDaemonOnlyAndAppScoped(t *testing.T) {
	registry := NewRegistry(webconfig.WebConfig{
		Campaigns: &webconfig.CampaignConfig{Catalog: filepath.Join(t.TempDir(), "catalog.yaml")},
	}, nil, runtimeBase{})
	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	rt := &sessionRuntime{HostRegistry: hostRegistry}

	registry.wireCampaign(rt, "runner")
	result, err := hostRegistry.Invoke(context.Background(), "host.campaign.snapshot", map[string]any{
		"max_campaigns": 10,
		"max_bytes":     64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "unavailable outside daemon mode") {
		t.Fatalf("ordinary web result = %#v", result)
	}

	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	registry.wireCampaign(rt, "runner")
	result, err = hostRegistry.Invoke(context.Background(), "host.campaign.snapshot", map[string]any{
		"max_campaigns": 10,
		"max_bytes":     64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := result.Data["snapshot"].(map[string]any)
	if !ok || snapshot["app_id"] != "runner" {
		t.Fatalf("daemon snapshot = %#v", result)
	}
}

func TestRegistryCampaignWatchDispatchesDurableStoryJob(t *testing.T) {
	_, storyPath := writeStory(t, "campaign-worker", []byte(minimalStory))
	catalogPath := filepath.Join(t.TempDir(), "catalog.yaml")
	catalog := fmt.Sprintf(`schema: project-object-graph/seed-catalog/v0
catalog:
  id: campaign-fixture
type_registry:
  - id: core-node
    schema: graph-type/v0
    extends: null
    required_fields: [id, schema, title, status, visibility]
  - id: campaign
    schema: graph-type/v0
    extends: core-node
nodes:
  - schema: fixture/campaign/v1
    id: campaign-one
    title: Campaign one
    status: active
    visibility: internal
    application_id: runner
    enabled: true
    cadence_seconds: 300
    budget:
      max_ticks_per_day: 2
      max_concurrency: 1
    action:
      kind: story-intent
      story: %q
      intent: go
      input: {}
`, storyPath)
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry(webconfig.WebConfig{
		Campaigns: &webconfig.CampaignConfig{Catalog: catalogPath},
	}, nil, runtimeBase{})
	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry.wireCampaign(&sessionRuntime{HostRegistry: hostRegistry}, "runner")

	result, err := hostRegistry.Invoke(context.Background(), "host.campaign.watch", map[string]any{
		"poll_seconds": 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, ok := result.Data["job_refs"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("watch result = %#v", result)
	}
	jobRef, ok := refs[0].(string)
	if !ok || jobRef == "" {
		t.Fatalf("durable job ref = %#v", refs[0])
	}
	job, err := registry.daemonJobs.Get(context.Background(), artifactjob.JobID(jobRef))
	if err != nil {
		t.Fatal(err)
	}
	if job.Origin.Kind != "campaign" ||
		!strings.HasPrefix(job.Origin.Ref, "campaign:runner/campaign-one/") ||
		job.Story != storyPath {
		t.Fatalf("artifact job = %#v", job)
	}

	snapshotResult, err := hostRegistry.Invoke(context.Background(), "host.campaign.snapshot", map[string]any{
		"max_campaigns": 10,
		"max_bytes":     64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotResult.Data["snapshot"].(map[string]any)
	rows := snapshot["campaigns"].([]any)
	row := rows[0].(map[string]any)
	if row["last_job_ref"] != jobRef || row["last_status"] != "done" {
		t.Fatalf("campaign snapshot = %#v", snapshot)
	}
}
