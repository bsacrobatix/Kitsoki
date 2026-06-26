package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentBenchScoreCommand(t *testing.T) {
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.jsonl")
	if err := os.WriteFile(trace, []byte(`{"ts":"2026-06-26T01:00:00Z","kind":"agent.stream","state_path":"rooms/decompose","payload":{"tool":"mcp__validator__submit"}}
{"ts":"2026-06-26T01:00:01Z","kind":"agent.stream","state_path":"rooms/lint","payload":{"type":"result","input_tokens":100,"output_tokens":20,"total_cost_usd":0.001}}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "bench.yaml")
	if err := os.WriteFile(manifest, []byte(`version: agent_bench/v1
cases:
  - id: smoke
    trace: trace.jsonl
    budgets:
      max_input_tokens: 200
      max_output_tokens: 50
      max_cost_usd: 0.01
    expectations:
      require_submit: true
      final_state: rooms/lint
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execRoot(t, "agent-bench", "score", manifest)
	if err != nil {
		t.Fatalf("agent-bench score: %v\n%s", err, out)
	}
	if !strings.Contains(out, "PASS smoke") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestAgentBenchRunCommandRequiresLiveGate(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "bench.yaml")
	if err := os.WriteFile(manifest, []byte(`version: agent_bench/v1
cases:
  - id: live
    trace: trace.jsonl
    run:
      command: ["echo", "hi"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execRoot(t, "agent-bench", "run", manifest)
	if err == nil {
		t.Fatalf("expected live gate error, got output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "live-gated") {
		t.Fatalf("expected live-gated error, got %v", err)
	}
}
