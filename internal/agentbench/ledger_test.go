package agentbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLedgerKeepsFailedRetryAndRejectsDuplicateTerminal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trace.jsonl")
	lines := []map[string]any{
		{"ts": "2026-07-13T00:00:00Z", "kind": "agent.call.start", "call_id": "a1", "state_path": "generate", "payload": map[string]any{"provider": "openai", "model": "gpt-test", "effort": "low"}},
		{"ts": "2026-07-13T00:00:01Z", "kind": "agent.call.error", "call_id": "a1", "payload": map[string]any{"error": "429"}},
		{"ts": "2026-07-13T00:00:02Z", "kind": "agent.call.start", "call_id": "a2", "state_path": "generate", "payload": map[string]any{"provider": "openai", "model": "gpt-test", "effort": "low"}},
		{"ts": "2026-07-13T00:00:03Z", "kind": "agent.call.complete", "call_id": "a2", "payload": map[string]any{"meta": map[string]any{"usage": map[string]any{"input_tokens": float64(7), "output_tokens": float64(2)}, "cost_decimal": "0.000123", "monetary_basis": "provider_estimate"}}},
	}
	var b []byte
	for _, l := range lines {
		x, _ := json.Marshal(l)
		b = append(b, append(x, '\n')...)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := BuildProviderRequestLedger(p, RequestIdentity{RunID: "run", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Status != "failed" || rows[1].Money.Amount != "0.000123" {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestFrictionUnknownIsNotZero(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trace.jsonl")
	data := "{\"ts\":\"2026-07-13T00:00:00Z\",\"kind\":\"agent.stream\",\"payload\":{\"tool\":\"read_file\",\"text\":\"ok\"}}\n"
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := AnalyzeFriction(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.CrawlTokens.Available || r.CrawlTokens.Reason == "" {
		t.Fatalf("crawl metric=%+v; want unavailable explanation", r.CrawlTokens)
	}
}
