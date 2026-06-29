package starlark_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	goyaml "github.com/goccy/go-yaml"

	starlarkhost "kitsoki/internal/host/starlark"
)

func TestPunchReportScript(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "stories/punch-list/scripts/punch_report.star"))
	if err != nil {
		t.Fatalf("read punch_report.star: %v", err)
	}
	sidecar, err := starlarkhost.LoadSidecar(filepath.Join(root, "stories/punch-list/scripts/punch_report.star.yaml"))
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	casBytes, err := os.ReadFile(filepath.Join(root, "stories/punch-list/cassettes/punch_report.inspect.yaml"))
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	var cas starlarkhost.InspectCassette
	if err := goyaml.Unmarshal(casBytes, &cas); err != nil {
		t.Fatalf("unmarshal cassette: %v", err)
	}

	ctx := starlarkhost.WithInspector(context.Background(), starlarkhost.NewReplayInspector(&cas))
	res, err := starlarkhost.Run(ctx, starlarkhost.Params{
		Script:  "punch_report.star",
		Source:  src,
		Sidecar: sidecar,
		Inputs: map[string]any{
			"state_path": ".artifacts/punch-list/report-fixture.state.json",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Outputs["report_path"]; got != ".artifacts/punch-list/report.md" {
		t.Fatalf("report_path = %#v, want report path", got)
	}
	if got := res.Outputs["summary"]; got != "1 passed, 0 partial, 1 failed, 0 skipped, 0 pending" {
		t.Fatalf("summary = %#v", got)
	}
	if len(res.Inspections) != 2 {
		t.Fatalf("inspections = %d, want read + write", len(res.Inspections))
	}
}

func TestPunchPolicyScript(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "stories/punch-list/scripts/punch_policy.star"))
	if err != nil {
		t.Fatalf("read punch_policy.star: %v", err)
	}
	sidecar, err := starlarkhost.LoadSidecar(filepath.Join(root, "stories/punch-list/scripts/punch_policy.star.yaml"))
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	casBytes, err := os.ReadFile(filepath.Join(root, "stories/punch-list/cassettes/punch_policy.inspect.yaml"))
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	var cas starlarkhost.InspectCassette
	if err := goyaml.Unmarshal(casBytes, &cas); err != nil {
		t.Fatalf("unmarshal cassette: %v", err)
	}

	ctx := starlarkhost.WithInspector(context.Background(), starlarkhost.NewReplayInspector(&cas))
	res, err := starlarkhost.Run(ctx, starlarkhost.Params{
		Script:  "punch_policy.star",
		Source:  src,
		Sidecar: sidecar,
		Inputs: map[string]any{
			"state_path": ".artifacts/punch-list/policy-fixture.state.json",
			"item_id":    "policy-demo",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	policy, ok := res.Outputs["policy_result"].(map[string]any)
	if !ok {
		t.Fatalf("policy_result = %#v, want object", res.Outputs["policy_result"])
	}
	if got := policy["status"]; got != "ok" {
		t.Fatalf("policy status = %#v, want ok", got)
	}
	if got := res.Outputs["error"]; got != "" {
		t.Fatalf("error = %#v, want empty", got)
	}
}

func TestPunchBoardScript(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "stories/punch-list/scripts/punch_board.star"))
	if err != nil {
		t.Fatalf("read punch_board.star: %v", err)
	}
	sidecar, err := starlarkhost.LoadSidecar(filepath.Join(root, "stories/punch-list/scripts/punch_board.star.yaml"))
	if err != nil {
		t.Fatalf("LoadSidecar: %v", err)
	}
	casBytes, err := os.ReadFile(filepath.Join(root, "stories/punch-list/cassettes/punch_board.inspect.yaml"))
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	var cas starlarkhost.InspectCassette
	if err := goyaml.Unmarshal(casBytes, &cas); err != nil {
		t.Fatalf("unmarshal cassette: %v", err)
	}

	ctx := starlarkhost.WithInspector(context.Background(), starlarkhost.NewReplayInspector(&cas))
	res, err := starlarkhost.Run(ctx, starlarkhost.Params{
		Script:  "punch_board.star",
		Source:  src,
		Sidecar: sidecar,
		Inputs: map[string]any{
			"state_path":  ".artifacts/punch-list/board-fixture.state.json",
			"mark_id":     "first",
			"mark_status": "passed",
			"mark_error":  "ok summary",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Outputs["route"]; got != "dispatch" {
		t.Fatalf("route = %#v, want dispatch", got)
	}
	if got := res.Outputs["processed_count"]; got != int64(1) {
		t.Fatalf("processed_count = %#v, want 1", got)
	}
	if got := res.Outputs["passed_count"]; got != int64(1) {
		t.Fatalf("passed_count = %#v, want 1", got)
	}
	if got := res.Outputs["pending_count"]; got != int64(1) {
		t.Fatalf("pending_count = %#v, want 1", got)
	}
	if got := res.Outputs["next_item_id"]; got != "second" {
		t.Fatalf("next_item_id = %#v, want second", got)
	}
	if len(res.Inspections) != 3 {
		t.Fatalf("inspections = %d, want exists + read + write", len(res.Inspections))
	}
}
