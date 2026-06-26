package starlark_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	starlarkhost "kitsoki/internal/host/starlark"
)

func TestDogfoodMarathonBuildDeckScript(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	scriptPath := filepath.Join(repoRoot, "stories", "dogfood-marathon", "scripts", "build_deck.star")
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read build_deck.star: %v", err)
	}

	res, err := starlarkhost.Run(context.Background(), starlarkhost.Params{
		Script: "build_deck.star",
		Source: src,
		Inputs: map[string]any{
			"results": map[string]any{"items": []any{
				map[string]any{
					"case_id":       "case-1",
					"exit":          "shipped",
					"verify_status": "solved",
					"trace":         ".artifacts/traces/case-1.jsonl",
					"worktree":      ".worktrees/case-1",
				},
			}},
			"rollup": map[string]any{
				"counts":   map[string]any{"processed": 1, "solved": 1, "partial": 0, "failed": 0, "skipped": 0, "needs_human": 0},
				"totals":   map[string]any{"cost_usd": 1.25, "tokens": 1200, "wall_s": 33},
				"worked":   []any{"independent verify passed"},
				"didnt":    []any{},
				"headline": "Processed 1 case: 1 solved.",
			},
			"findings": map[string]any{"items": []any{}},
		},
	})
	if err != nil {
		t.Fatalf("Run build_deck.star: %v", err)
	}
	deckSpec := res.Outputs["deck_spec"].(map[string]any)
	body := deckSpec["body"].(string)
	if !strings.HasPrefix(deckSpec["artifact_thread"].(string), "dogfood-marathon/run-") {
		t.Fatalf("artifact_thread should be content-suffixed under dogfood-marathon: %#v", deckSpec["artifact_thread"])
	}
	if !strings.HasSuffix(deckSpec["artifact_thread"].(string), "/deck.slidey.json") {
		t.Fatalf("artifact_thread should name deck.slidey.json: %#v", deckSpec["artifact_thread"])
	}

	var deck map[string]any
	if err := json.Unmarshal([]byte(body), &deck); err != nil {
		t.Fatalf("deck body is not valid JSON: %v\n%s", err, body)
	}
	if deck["theme"] != "kitsoki-report" {
		t.Fatalf("unexpected theme: %#v", deck["theme"])
	}
	scenes := deck["scenes"].([]any)
	if len(scenes) < 5 {
		t.Fatalf("expected report scenes, got %d: %#v", len(scenes), scenes)
	}
	if scenes[3].(map[string]any)["title"] != "Case outcomes" {
		t.Fatalf("case outcomes scene missing at expected position: %#v", scenes[3])
	}
}
