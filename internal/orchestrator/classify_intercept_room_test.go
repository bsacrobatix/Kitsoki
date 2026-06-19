// De-risks the MATCHING quality of git-ops's `intercept` command hub
// (stories/git-ops/rooms/intercept.yaml): the no-LLM router must resolve natural
// phrasings of the common git commands to the right intent and pass unrelated
// prose through — all via Classify, which is a pure read (no git executed, no
// LLM). This is exactly what the pre-LLM intercept gate relies on when bound to
// `room: intercept` (docs/architecture/prompt-intercept.md).
package orchestrator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

func TestClassify_GitOpsInterceptRoom(t *testing.T) {
	t.Parallel()

	def, err := app.Load("../../stories/git-ops/app.yaml")
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// nil harness: the intercept gate never routes via the LLM.
	orch := orchestrator.New(def, m, s, nil)

	ctx := context.Background()
	state := app.StatePath("intercept")
	w := orch.InitialWorld()

	// Natural phrasings of the grouped commands must resolve to the right intent.
	matches := []struct{ in, intent string }{
		{"rebase this onto main", "rebase"},
		{"bring in the latest from main", "rebase"},
		{"squash my history", "squash"},
		{"ship it", "merge_into_main"},
		{"pull from upstream", "pull"},
		{"undo last commit", "undo"},
		{"list worktrees", "worktree_list"},
	}
	for _, tc := range matches {
		verdict, matched, cErr := orch.Classify(ctx, state, w, tc.in)
		require.NoError(t, cErr, "Classify(%q)", tc.in)
		require.True(t, matched, "expected %q to match a no-LLM tier", tc.in)
		require.Equal(t, tc.intent, verdict.Intent, "input %q resolved to the wrong intent", tc.in)
	}

	// Unrelated prose must pass through — the gate sends it to the agent's model.
	for _, in := range []string{"explain the borrow checker", "what does this function do?"} {
		_, matched, cErr := orch.Classify(ctx, state, w, in)
		require.NoError(t, cErr, "Classify(%q)", in)
		require.False(t, matched, "expected %q to pass through (no match)", in)
	}
}
