package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRebaseWithMap_NestedTaskPaths pins that an imported child's
// host.agent.task paths rebase to the child story's directory:
//   - context.prompt / context.prompt_path (nested under with.context)
//   - acceptance.schema (nested under with.acceptance)
//
// Regression: acceptance.schema was NOT rebased, so the runtime joined the
// relative path against the PARENT app dir ($KITSOKI_APP_DIR) and failed with
// "schema ... not found" — silently swallowed by the room's on_error, leaving
// the brief unwritten. See rooms/proposal.yaml brief_distill.
func TestRebaseWithMap_NestedTaskPaths(t *testing.T) {
	childDir := "/repo/stories/dev-story"
	with := map[string]any{
		"agent":       "proposal_brief_writer",
		"working_dir": "{{ world.proposal_workspace }}", // template — left alone
		"acceptance": map[string]any{
			"schema": "schemas/brief-distill.json",
		},
		"context": map[string]any{
			"prompt": "prompts/proposal_brief_distill.md",
		},
	}

	rebaseWithMap(with, childDir)

	acc := with["acceptance"].(map[string]any)
	if got, want := acc["schema"].(string), filepath.Join(childDir, "schemas/brief-distill.json"); got != want {
		t.Errorf("acceptance.schema = %q, want %q", got, want)
	}
	ctx := with["context"].(map[string]any)
	if got, want := ctx["prompt"].(string), filepath.Join(childDir, "prompts/proposal_brief_distill.md"); got != want {
		t.Errorf("context.prompt = %q, want %q", got, want)
	}
	// Templated working_dir must be left untouched.
	if got := with["working_dir"].(string); got != "{{ world.proposal_workspace }}" {
		t.Errorf("working_dir rewritten unexpectedly: %q", got)
	}
}

// TestImports_RebasesNestedEffectAssetPaths proves imported host-call asset
// paths are rebased even when the host call sits inside Effect.Effects. Those
// nested effects are used by on_complete target blocks, so leaving their
// prompt/schema paths relative makes the runtime resolve them against the
// importing app directory instead of the defining child story directory.
func TestImports_RebasesNestedEffectAssetPaths(t *testing.T) {
	root := t.TempDir()
	parentDir := filepath.Join(root, "parent")
	childDir := filepath.Join(root, "child")
	mustMkdirAll(t, filepath.Join(parentDir, "prompts"))
	mustMkdirAll(t, filepath.Join(parentDir, "schemas"))
	mustMkdirAll(t, filepath.Join(childDir, "prompts"))
	mustMkdirAll(t, filepath.Join(childDir, "schemas"))

	mustWriteFile(t, filepath.Join(parentDir, "prompts", "nested.md"), "parent prompt")
	mustWriteFile(t, filepath.Join(parentDir, "schemas", "nested.json"), `{"type":"object","properties":{"origin":{"const":"parent"}}}`)
	mustWriteFile(t, filepath.Join(childDir, "prompts", "nested.md"), "child prompt")
	mustWriteFile(t, filepath.Join(childDir, "schemas", "nested.json"), `{"type":"object","properties":{"origin":{"const":"child"}}}`)
	mustWriteFile(t, filepath.Join(parentDir, "app.yaml"), `
app: {id: parent, title: parent}
root: main
hosts: [host.agent.decide, host.run]
imports:
  sub:
    source: ../child
    entry: start
states:
  main:
    view: parent
`)
	mustWriteFile(t, filepath.Join(childDir, "app.yaml"), `
app: {id: child, title: child}
root: start
hosts: [host.agent.decide, host.run]
states:
  start:
    view: child
    on_enter:
      - invoke: host.run
        background: true
        on_complete:
          - target: done
            effects:
              - invoke: host.agent.decide
                with:
                  prompt_path: prompts/nested.md
                  schema: schemas/nested.json
  done:
    view: done
`)

	def, err := Load(filepath.Join(parentDir, "app.yaml"))
	if err != nil {
		t.Fatalf("Load imported app: %v", err)
	}
	start := def.States["sub"].States["start"]
	nested := start.OnEnter[0].OnComplete[0].Effects[0].With

	if got, want := nested["prompt_path"].(string), filepath.Join(childDir, "prompts", "nested.md"); got != want {
		t.Fatalf("nested prompt_path = %q, want child story path %q", got, want)
	}
	if got, want := nested["schema"].(string), filepath.Join(childDir, "schemas", "nested.json"); got != want {
		t.Fatalf("nested schema = %q, want child story path %q", got, want)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
