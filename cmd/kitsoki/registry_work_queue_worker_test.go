package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
	"kitsoki/internal/workqueue"
)

func TestRegistryWorkQueueWorkerClaimsOnlyConfiguredTargetAndQueue(t *testing.T) {
	trustedStory := filepath.Join(t.TempDir(), "worker", "app.yaml")
	bundleRoot := t.TempDir()
	bundle := []byte("retained code bundle")
	if err := os.WriteFile(
		filepath.Join(bundleRoot, "work.bundle"), bundle, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bundle)
	bundleDigest := "sha256:" + hex.EncodeToString(sum[:])
	registry := NewRegistry(webconfig.WebConfig{
		WorkQueueBundleRoot: bundleRoot,
		Workers:             []workerregistry.Entry{{ID: "runner", Label: "Runner", Placement: "workstation", Enabled: true, Capabilities: workerregistry.Capabilities{Labels: []string{"gpu"}}}},
		WorkQueues: map[string]map[string]workqueue.QueueConfig{"target": {
			"allowed": {MaxInputBytes: 1024, MaxAttempts: 2, RequiredCapabilities: []string{"gpu", "placement:workstation"}, ProducesCode: true},
			"private": {MaxInputBytes: 1024, MaxAttempts: 2},
		}},
		WorkQueueWorkers: map[string]webconfig.WorkQueueWorkerBinding{"worker-app": {
			TargetApplication: "target", StoryPath: trustedStory, AllowedQueues: []string{"allowed"}, WorkerID: "runner", MaxConcurrent: 1, LeaseSeconds: 30,
		}},
	}, nil, runtimeBase{})
	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	producerHosts := host.NewRegistry()
	host.RegisterBuiltins(producerHosts)
	registry.wireWorkQueue(&sessionRuntime{HostRegistry: producerHosts}, "target")
	for _, queue := range []string{"allowed", "private"} {
		if _, err := producerHosts.Invoke(context.Background(), "host.work_queue.enqueue", map[string]any{"queue": queue, "idempotency_key": queue + "-1", "input": map[string]any{"secret": queue}}); err != nil {
			t.Fatal(err)
		}
	}

	workerHosts := host.NewRegistry()
	host.RegisterBuiltins(workerHosts)
	registry.wireWorkQueueWorker(&sessionRuntime{HostRegistry: workerHosts}, "worker-app", trustedStory)
	denied, err := workerHosts.Invoke(context.Background(), "host.work_queue_worker.claim", map[string]any{"queue": "private"})
	if err == nil || denied.Error != "" {
		t.Fatalf("private claim = %#v, %v", denied, err)
	}
	claim, err := workerHosts.Invoke(context.Background(), "host.work_queue_worker.claim", map[string]any{"queue": "allowed", "worker_id": "forged", "lease_seconds": 1, "capabilities": []any{"forged"}})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Data["claimed"] != true || claim.Data["payload"].(map[string]any)["secret"] != "allowed" {
		t.Fatalf("claim = %#v", claim.Data)
	}
	ref := claim.Data["work_ref"].(string)
	fence := claim.Data["fence"].(int64)
	complete, err := workerHosts.Invoke(context.Background(), "host.work_queue_worker.complete", map[string]any{
		"work_ref": ref, "fence": fence,
		"receipt": map[string]any{
			"schema": workqueue.ReceiptSchema, "outcome": "done",
			"job_id": ref, "fence": fence, "attempt": 1,
			"bundle_ref": "work.bundle", "bundle_digest": bundleDigest,
		},
	})
	if err != nil || complete.Data["status"] != string(workqueue.StateSucceeded) {
		t.Fatalf("complete = %#v, %v", complete, err)
	}
	receipt, ok := complete.Data["receipt"].(*workqueue.Receipt)
	if !ok || !receipt.Countable {
		t.Fatalf("countable receipt = %#v", complete.Data["receipt"])
	}

	otherHosts := host.NewRegistry()
	host.RegisterBuiltins(otherHosts)
	registry.wireWorkQueueWorker(&sessionRuntime{HostRegistry: otherHosts}, "other-app", "/untrusted/app.yaml")
	unavailable, err := otherHosts.Invoke(context.Background(), "host.work_queue_worker.claim", map[string]any{"queue": "allowed"})
	if err != nil || !strings.Contains(unavailable.Error, "unavailable outside configured daemon mode") {
		t.Fatalf("other app = %#v, %v", unavailable, err)
	}
}

func TestRegistryWorkQueueWorkerRejectsImpersonatedApplicationID(t *testing.T) {
	trustedStory := filepath.Join(t.TempDir(), "trusted", "app.yaml")
	registry := NewRegistry(webconfig.WebConfig{
		Workers: []workerregistry.Entry{{
			ID: "runner", Label: "Runner", Placement: "workstation", Enabled: true,
		}},
		WorkQueues: map[string]map[string]workqueue.QueueConfig{
			"target": {"q": {}},
		},
		WorkQueueWorkers: map[string]webconfig.WorkQueueWorkerBinding{
			"worker-app": {
				TargetApplication: "target", StoryPath: trustedStory,
				AllowedQueues: []string{"q"}, WorkerID: "runner",
				MaxConcurrent: 1, LeaseSeconds: 30,
			},
		},
	}, nil, runtimeBase{})
	if err := registry.EnableDaemon(filepath.Join(t.TempDir(), "daemon.db")); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	hosts := host.NewRegistry()
	host.RegisterBuiltins(hosts)
	registry.wireWorkQueueWorker(
		&sessionRuntime{HostRegistry: hosts}, "worker-app",
		filepath.Join(t.TempDir(), "crafted", "app.yaml"),
	)
	result, err := hosts.Invoke(
		context.Background(), "host.work_queue_worker.claim",
		map[string]any{"queue": "q"},
	)
	if err != nil || !strings.Contains(result.Error, "unavailable outside configured daemon mode") {
		t.Fatalf("impersonated worker = %#v, %v", result, err)
	}
}
