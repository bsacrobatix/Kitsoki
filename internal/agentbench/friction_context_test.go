package agentbench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeFrictionDistinguishesUnavailableAndEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	trace := `{"ts":"2026-07-13T00:00:00Z","kind":"agent.call.start","call_id":"a","payload":{}}
{"ts":"2026-07-13T00:00:01Z","kind":"agent.tool.error","call_id":"a","payload":{}}
{"ts":"2026-07-13T00:00:02Z","kind":"agent.call.complete","call_id":"a","payload":{"tool":"rg","state_changed":false,"usage":{"input_tokens":7,"output_tokens":3}}}` + "\n"
	if err := os.WriteFile(path, []byte(trace), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := AnalyzeFriction(path)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ToolErrors.Available || r.ToolErrors.Value != 1 {
		t.Fatalf("tool errors = %#v", r.ToolErrors)
	}
	if !r.NoStateChangeCalls.Available || r.NoStateChangeCalls.Value != 1 {
		t.Fatalf("state change = %#v", r.NoStateChangeCalls)
	}
	if r.WastedCallTokens.Value != 10 || r.CrawlTokens.Value != 10 {
		t.Fatalf("tokens = wasted %#v crawl %#v", r.WastedCallTokens, r.CrawlTokens)
	}
	if r.SchemaFailures.Available || r.SchemaFailures.Reason == "" {
		t.Fatalf("schema metric should be unavailable: %#v", r.SchemaFailures)
	}
	if r.TimeToFirstSuccessfulCall.Value != 2000 {
		t.Fatalf("first success = %#v", r.TimeToFirstSuccessfulCall)
	}
}

func TestCompileContextPacketStablePrefixAndHashes(t *testing.T) {
	p, err := CompileContextPacket([]ContextRequirement{{ID: "volatile", Source: "task", Content: "change X"}, {ID: "contract", Source: "schema", Content: "must validate", Stable: true}}, []ContextExemplar{{ID: "z", Content: "example"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Schema != ContextPacketV1 || p.StableHash == "" || p.TaskHash == "" {
		t.Fatalf("packet = %#v", p)
	}
	if want := "# Stable contract\n[contract] schema\nmust validate\n\n# Task context\n[volatile] task\nchange X\n[exemplar:z]\nexample\n"; p.Content != want {
		t.Fatalf("content = %q, want %q", p.Content, want)
	}
	p2, _ := CompileContextPacket([]ContextRequirement{{ID: "contract", Source: "schema", Content: "must validate", Stable: true}}, nil)
	if p2.StableHash != p.StableHash {
		t.Fatal("task changes altered stable hash")
	}
}
