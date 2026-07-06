package host

import (
	"context"
	"testing"
)

// TestStarlarkRunHandler_ScenarioQARecordLegResult_VscodePostDriveGate proves
// the real stories/scenario-qa/scripts/record_leg_result.star script (not a
// stub) enforces the vscode post-drive re-capture rule: a vscode leg with
// only a preflight capture (no post_drive_evidence_ref) must be scored
// degraded-evidence even when the judge itself rendered a clean pass, and a
// vscode leg that DID capture a post-drive frame is free to keep the judge's
// verdict. This is the regression guard for the gap where a real
// scenario-qa run (20260705T232948Z-gears-rust-core-maintainer-scenario-qa)
// only ever captured a preflight vscode frame bound to session "s1" and
// never re-captured the bridge after driving the live session to "s2".
func TestStarlarkRunHandler_ScenarioQARecordLegResult_VscodePostDriveGate(t *testing.T) {
	const script = "../../stories/scenario-qa/scripts/record_leg_result.star"

	run := func(t *testing.T, drive map[string]any) map[string]any {
		t.Helper()
		leg := map[string]any{
			"leg_id":    "prd-design::vscode",
			"scenario":  "prd-design",
			"transport": "vscode",
		}
		judge := map[string]any{
			"verdict": "pass",
			"summary": "vscode bridge shows the driven-forward state.",
		}
		res, err := StarlarkRunHandler(context.Background(), map[string]any{
			"script": script,
			"inputs": map[string]any{
				"leg":          leg,
				"drive_result": drive,
				"judge_result": judge,
			},
		})
		if err != nil {
			t.Fatalf("StarlarkRunHandler: %v", err)
		}
		if res.Error != "" {
			t.Fatalf("unexpected domain error: %s", res.Error)
		}
		return res.Data
	}

	t.Run("preflight only is degraded-evidence even with a pass verdict", func(t *testing.T) {
		data := run(t, map[string]any{
			"status":        "captured",
			"evidence_refs": []any{"evidence/prd-design/vscode/01-preflight-vscode-observe.json"},
			// no post_drive_evidence_ref
		})
		results, _ := data["leg_results"].(map[string]any)
		items, _ := results["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected 1 leg result, got %d", len(items))
		}
		record, _ := items[0].(map[string]any)
		if got := record["verdict"]; got != "degraded-evidence" {
			t.Fatalf("verdict = %v, want degraded-evidence (preflight-only vscode leg)", got)
		}
		if got := data["degraded_count"]; got != int64(1) && got != 1 {
			t.Fatalf("degraded_count = %v, want 1", got)
		}
	})

	t.Run("preflight plus post-drive capture keeps the judge's pass", func(t *testing.T) {
		data := run(t, map[string]any{
			"status": "captured",
			"evidence_refs": []any{
				"evidence/prd-design/vscode/01-preflight-vscode-observe.json",
				"evidence/prd-design/vscode/02-postdrive-vscode-observe.json",
			},
			"post_drive_evidence_ref":   "evidence/prd-design/vscode/02-postdrive-vscode-observe.json",
			"post_drive_session_handle": "s2",
		})
		results, _ := data["leg_results"].(map[string]any)
		items, _ := results["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected 1 leg result, got %d", len(items))
		}
		record, _ := items[0].(map[string]any)
		if got := record["verdict"]; got != "pass" {
			t.Fatalf("verdict = %v, want pass (post-drive capture present)", got)
		}
		if got := data["pass_count"]; got != int64(1) && got != 1 {
			t.Fatalf("pass_count = %v, want 1", got)
		}
	})
}
