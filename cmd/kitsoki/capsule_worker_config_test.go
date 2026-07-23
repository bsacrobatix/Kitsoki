package main

import (
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/capsule/bucketsource"
	"kitsoki/internal/capsule/vmpool"
)

func TestLoadWorkerEnvConfigParsesAndExports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	body := "# worker boot contract\n" +
		"KITSOKI_WORKER_TOKEN=secret-token-value\n" +
		"KITSOKI_WORKER_LISTEN=0.0.0.0:7443\n" +
		"\n" +
		"KITSOKI_WORKER_NETWORKS=live, replay\n" +
		"EXTRA_VALUE=with=equals=signs\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITSOKI_WORKER_TOKEN", "")
	values, err := loadWorkerEnvConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["KITSOKI_WORKER_LISTEN"] != "0.0.0.0:7443" || values["EXTRA_VALUE"] != "with=equals=signs" {
		t.Fatalf("values = %+v", values)
	}
	if os.Getenv("KITSOKI_WORKER_TOKEN") != "secret-token-value" {
		t.Fatal("config values not exported to process env")
	}
}

func TestLoadWorkerEnvConfigRejectsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte("NOT A KEY VALUE LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkerEnvConfig(path); err == nil {
		t.Fatal("expected error for malformed line")
	}
}

func TestApplyWorkerEnvConfigRespectsExplicitFlags(t *testing.T) {
	values := map[string]string{
		"KITSOKI_WORKER_TOKEN":    "tok",
		"KITSOKI_WORKER_LISTEN":   "0.0.0.0:9000",
		"KITSOKI_WORKER_NETWORKS": "live",
	}
	opts := workerServeOptions{listen: "127.0.0.1:7443", tokenEnv: "OLD_TOKEN_ENV"}
	changed := map[string]bool{"listen": true}
	applyWorkerEnvConfig(values, func(name string) bool { return changed[name] }, &opts)
	if opts.listen != "127.0.0.1:7443" {
		t.Fatalf("explicit --listen overridden: %s", opts.listen)
	}
	if opts.tokenEnv != workerEnvToken {
		t.Fatalf("tokenEnv = %s, want %s", opts.tokenEnv, workerEnvToken)
	}
	if len(opts.networks) != 1 || opts.networks[0] != "live" {
		t.Fatalf("networks = %v", opts.networks)
	}
	if opts.root != "/var/lib/kitsoki-worker" || opts.certFile != "/etc/kitsoki-worker/server.crt" || opts.keyFile != "/etc/kitsoki-worker/server.key" {
		t.Fatalf("VM defaults not applied: %+v", opts)
	}
}

func TestWorkerOutputsFromEnv(t *testing.T) {
	if sink, err := workerOutputsFromEnv(map[string]string{}); err != nil || sink != nil {
		t.Fatalf("no bucket configured: sink=%v err=%v", sink, err)
	}
	if _, err := workerOutputsFromEnv(map[string]string{vmpool.WorkerEnvOutputsURL: "https://kitsoki-test.sgp1.digitaloceanspaces.com"}); err == nil {
		t.Fatal("expected error when key/secret env names missing")
	}
	t.Setenv("TEST_OUT_KEY", "key-id")
	t.Setenv("TEST_OUT_SECRET", "secret")
	sink, err := workerOutputsFromEnv(map[string]string{
		vmpool.WorkerEnvOutputsURL:    "https://kitsoki-test.sgp1.digitaloceanspaces.com",
		vmpool.WorkerEnvOutputsKeyEnv: "TEST_OUT_KEY",
		vmpool.WorkerEnvOutputsSecEnv: "TEST_OUT_SECRET",
		workerEnvOutputsPfx:           "runs",
	})
	if err != nil {
		t.Fatal(err)
	}
	mirror, ok := sink.(bucketsource.OutputMirror)
	if !ok || mirror.Store == nil || mirror.Prefix != "runs" {
		t.Fatalf("sink = %#v", sink)
	}
}
