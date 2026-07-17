package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
)

func runWorkerCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	root.SetArgs(args)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	err := root.Execute()
	return out.String(), err
}

func TestWorkerAddListRemove(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, webconfig.DefaultConfigFile)

	if _, err := runWorkerCmd(t, "worker", "add",
		"--config", config,
		"--id", "vm-a",
		"--label", "VM A",
		"--placement", "workstation",
		"--endpoint", "http://127.0.0.1:1", // unreachable on purpose; list must still work
		"--capability-placement", "container",
		"--capability-network", "git-mirror",
		"--isolation", "sandboxed",
		"--enabled=true",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, err := runWorkerCmd(t, "worker", "list", "--config", config, "--json", "--poll-timeout", "50ms")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var projections []workerregistry.Projection
	if err := json.Unmarshal([]byte(out), &projections); err != nil {
		t.Fatalf("parse json: %v\noutput: %s", err, out)
	}
	if len(projections) != 1 {
		t.Fatalf("projections = %#v", projections)
	}
	p := projections[0]
	if p.ID != "vm-a" || p.Label != "VM A" || p.Placement != "workstation" || !p.Enabled {
		t.Fatalf("projection = %#v", p)
	}
	if p.Capabilities.Isolation != "sandboxed" || len(p.Capabilities.Placements) != 1 || p.Capabilities.Placements[0] != "container" {
		t.Fatalf("capabilities = %#v", p.Capabilities)
	}
	// Unreachable endpoint must degrade to offline/unknown, not fail the command.
	if p.Health == "" {
		t.Fatalf("expected a health value, got empty")
	}

	if _, err := runWorkerCmd(t, "worker", "remove", "vm-a", "--config", config); err != nil {
		t.Fatalf("remove: %v", err)
	}
	out, err = runWorkerCmd(t, "worker", "list", "--config", config, "--json")
	if err != nil {
		t.Fatalf("list after remove: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &projections); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if len(projections) != 0 {
		t.Fatalf("expected empty registry after remove, got %#v", projections)
	}
}

func TestWorkerEnableDisableDrain(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, webconfig.DefaultConfigFile)

	if _, err := runWorkerCmd(t, "worker", "add",
		"--config", config,
		"--id", "vm-b",
		"--label", "VM B",
		"--placement", "thin",
		"--enabled=true",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := runWorkerCmd(t, "worker", "disable", "vm-b", "--config", config); err != nil {
		t.Fatalf("disable: %v", err)
	}
	local := webconfig.LocalConfigPath(config)
	reg, err := workerregistry.Load(config, local)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reg.Find("vm-b")
	if !ok || entry.Enabled {
		t.Fatalf("expected disabled entry, got %#v", entry)
	}

	if _, err := runWorkerCmd(t, "worker", "enable", "vm-b", "--config", config); err != nil {
		t.Fatalf("enable: %v", err)
	}
	reg, err = workerregistry.Load(config, local)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok = reg.Find("vm-b")
	if !ok || !entry.Enabled {
		t.Fatalf("expected enabled entry, got %#v", entry)
	}

	// drain with no endpoint: advisory disable + "could not be checked" message, no error.
	out, err := runWorkerCmd(t, "worker", "drain", "vm-b", "--config", config)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !containsAll(out, "enabled=false", "could not be checked") {
		t.Fatalf("drain output = %q", out)
	}
	reg, err = workerregistry.Load(config, local)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok = reg.Find("vm-b")
	if !ok || entry.Enabled {
		t.Fatalf("expected drain to disable, got %#v", entry)
	}
}

func TestWorkerAdd_DuplicateIDFails(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, webconfig.DefaultConfigFile)
	args := []string{"worker", "add", "--config", config, "--id", "dup", "--label", "Dup", "--placement", "thin"}
	if _, err := runWorkerCmd(t, args...); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := runWorkerCmd(t, args...); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestWorkerAdd_PreservesUnrelatedLocalConfigKeys(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, webconfig.DefaultConfigFile)
	local := webconfig.LocalConfigPath(config)
	if err := os.WriteFile(local, []byte("agent_launch_policy:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runWorkerCmd(t, "worker", "add", "--config", config, "--id", "vm-c", "--label", "VM C", "--placement", "thin"); err != nil {
		t.Fatalf("add: %v", err)
	}
	raw, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(raw), "agent_launch_policy:", "workers:") {
		t.Fatalf("local config lost unrelated keys:\n%s", raw)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
