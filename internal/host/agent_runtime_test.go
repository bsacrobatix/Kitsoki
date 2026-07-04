package host_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/host/agentruntime"
)

func TestAgentStreamerSandbox_EmitsRuntimeEventsAndParsesReply(t *testing.T) {
	t.Parallel()
	sink := &memSink{}
	ctx := agentCtxForTest(sink)
	ctx = host.WithCallID(ctx, "call-runtime-1")
	ctx = host.WithAgentRuntimeRegistry(ctx, agentruntime.NewRegistry(&agentruntime.Fake{
		Backend:  "fake-fs",
		Strength: agentruntime.StrengthFSConfined,
		LaunchResult: agentruntime.Result{
			Stdout: strings.Join([]string{
				`{"type":"system","subtype":"init","session_id":"sess-runtime"}`,
				`{"type":"result","subtype":"success","result":"sandboxed ok","session_id":"sess-runtime"}`,
			}, "\n") + "\n",
		},
	}))

	cr, sid, err := (host.AgentStreamer{
		Bin:        "fake-agent",
		CLIArgs:    []string{"-p"},
		Stdin:      "prompt",
		WorkingDir: ".",
		Sandbox: &host.AgentSandboxSpec{
			MinStrength: agentruntime.StrengthFSConfined,
			Repo:        agentruntime.RepoReadOnly,
			RW:          []string{".artifacts/goal"},
			Degrade:     agentruntime.DegradeFail,
		},
	}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cr.Stdout != "sandboxed ok" {
		t.Fatalf("stdout = %q", cr.Stdout)
	}
	if sid != "sess-runtime" {
		t.Fatalf("session id = %q", sid)
	}

	var sawStart, sawEnd bool
	for _, ev := range sink.events {
		switch string(ev.Kind) {
		case "agent.runtime.start":
			sawStart = true
			if ev.CallID != "call-runtime-1" {
				t.Fatalf("runtime start call_id = %q", ev.CallID)
			}
			var payload map[string]any
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("start payload: %v", err)
			}
			if payload["backend"] != "fake-fs" || payload["strength"] != "fs_confined" || payload["repo"] != "read_only" {
				t.Fatalf("unexpected start payload: %#v", payload)
			}
		case "agent.runtime.end":
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing runtime events: %#v", sink.events)
	}
}

func TestAgentStreamerSandbox_FailsClosedWhenMinStrengthUnmet(t *testing.T) {
	t.Parallel()
	ctx := host.WithCallID(context.Background(), "call-runtime-fail")
	ctx = host.WithAgentRuntimeRegistry(ctx, agentruntime.NewRegistry(agentruntime.NewFake(agentruntime.StrengthSupervised)))

	cr, _, err := (host.AgentStreamer{
		Bin:        "fake-agent",
		CLIArgs:    []string{"-p"},
		Stdin:      "prompt",
		WorkingDir: ".",
		Sandbox: &host.AgentSandboxSpec{
			MinStrength: agentruntime.StrengthFSConfined,
			Degrade:     agentruntime.DegradeFail,
		},
	}).Run(ctx)
	if err != nil {
		t.Fatalf("Run returned Go error: %v", err)
	}
	if cr.Infra == nil || !strings.Contains(cr.Infra.Error(), "no backend satisfies") {
		t.Fatalf("infra = %v, want no backend satisfies", cr.Infra)
	}
}
