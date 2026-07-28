package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workqueue"
)

func TestRegistryWorkQueueIsDaemonOnlyConfiguredAndAppScoped(t *testing.T) {
	registry := NewRegistry(webconfig.WebConfig{
		WorkQueues: map[string]map[string]workqueue.QueueConfig{
			"operations": {
				"feedback": {
					MaxInputBytes: 1024, MaxAttempts: 4,
					RequiredCapabilities: []string{"linux", "go"},
					Priority:             7,
				},
			},
		},
	}, nil, runtimeBase{})
	hostRegistry := host.NewRegistry()
	host.RegisterBuiltins(hostRegistry)
	rt := &sessionRuntime{HostRegistry: hostRegistry}

	registry.wireWorkQueue(rt, "operations")
	unavailable, err := hostRegistry.Invoke(
		context.Background(),
		"host.work_queue.snapshot",
		map[string]any{"max_items": 10, "max_bytes": 64 * 1024},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unavailable.Error, "unavailable outside daemon mode") {
		t.Fatalf("ordinary web result = %#v", unavailable)
	}

	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	registry.wireWorkQueue(rt, "operations")

	submitted, err := hostRegistry.Invoke(
		context.Background(),
		"host.work_queue.enqueue",
		map[string]any{
			"queue":           "feedback",
			"idempotency_key": "feedback:one",
			"input":           map[string]any{"feedback_ref": "one"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	workRef, _ := submitted.Data["work_ref"].(string)
	if !strings.HasPrefix(workRef, "wq_") ||
		submitted.Data["status"] != "queued" ||
		submitted.Data["replayed"] != false {
		t.Fatalf("submit = %#v", submitted.Data)
	}
	replay, err := hostRegistry.Invoke(
		context.Background(),
		"host.work_queue.enqueue",
		map[string]any{
			"queue":           "feedback",
			"idempotency_key": "feedback:one",
			"input":           map[string]any{"feedback_ref": "one"},
		},
	)
	if err != nil || replay.Data["work_ref"] != workRef ||
		replay.Data["replayed"] != true {
		t.Fatalf("replay = %#v, %v", replay.Data, err)
	}

	got, err := hostRegistry.Invoke(
		context.Background(),
		"host.work_queue.get",
		map[string]any{"work_ref": workRef},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"feedback_ref", "operations"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("get leaks private value %q: %s", forbidden, raw)
		}
	}

	snapshot, err := hostRegistry.Invoke(
		context.Background(),
		"host.work_queue.snapshot",
		map[string]any{"max_items": 10, "max_bytes": 64 * 1024},
	)
	if err != nil {
		t.Fatal(err)
	}
	view, ok := snapshot.Data["snapshot"].(map[string]any)
	if !ok || view["schema"] != "kitsoki/work-queue-snapshot/v1" {
		t.Fatalf("snapshot = %#v", snapshot.Data)
	}

	otherHosts := host.NewRegistry()
	host.RegisterBuiltins(otherHosts)
	registry.wireWorkQueue(
		&sessionRuntime{HostRegistry: otherHosts},
		"other-application",
	)
	other, err := otherHosts.Invoke(
		context.Background(),
		"host.work_queue.get",
		map[string]any{"work_ref": workRef},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(other.Error, "unavailable outside daemon mode") {
		t.Fatalf("unconfigured app result = %#v", other)
	}
}
