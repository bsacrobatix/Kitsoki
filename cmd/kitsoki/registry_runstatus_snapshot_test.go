package main

import (
	"context"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/host"
)

func TestWireRunstatusSnapshotUsesDaemonStoreAndAppScope(t *testing.T) {
	store := artifactjob.NewMemoryStore()
	if _, err := store.Register(context.Background(), artifactjob.RegisterRequest{
		ID: "job-a", AppID: "app-a", Status: artifactjob.StatusRunning,
	}); err != nil {
		t.Fatalf("register app-a job: %v", err)
	}
	if _, err := store.Register(context.Background(), artifactjob.RegisterRequest{
		ID: "job-b", AppID: "app-b", Status: artifactjob.StatusRunning,
	}); err != nil {
		t.Fatalf("register app-b job: %v", err)
	}

	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	registry := &SessionRegistry{daemonJobs: store}
	registry.wireRunstatusSnapshot(&sessionRuntime{HostRegistry: hostRegistry}, "app-a")

	result, err := hostRegistry.Invoke(
		context.Background(),
		"host.runstatus.snapshot",
		map[string]any{"max_jobs": 1, "max_bytes": 4096},
	)
	if err != nil {
		t.Fatalf("invoke snapshot: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("snapshot result error = %q", result.Error)
	}
	snapshot := result.Data["snapshot"].(map[string]any)
	jobs := snapshot["jobs"].([]any)
	if len(jobs) != 1 || jobs[0].(map[string]any)["job_ref"] != "job-a" {
		t.Fatalf("scoped jobs = %#v", jobs)
	}
}
