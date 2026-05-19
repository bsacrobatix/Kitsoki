package app_test

// Stories load gate — `app.Load` every live story under `stories/` and
// assert no error. Cheap (no host invocation, no LLM, no orchestrator),
// fast (<200ms), and catches the class of break that landed dd9fa65
// without anyone noticing: a sub-import (cypilot) gained a host
// (host.run) that flowed into dev-story via `hosts: inherit`, then
// kitsoki-dev's strict `hosts: declared` import demanded host.run in
// its own allow-list — but the allow-list wasn't updated. Symptom in
// production: TUI crashes at startup with
// `imports.core: hosts: declared but parent does not list child host
// "host.run"`. This test fails as soon as such drift is committed.
//
// Adding a new story under `stories/`? It auto-joins the gate. No
// configuration. If the story is intentionally not-yet-loadable,
// guard it under a build tag or move it to `testdata/`.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
)

func TestAllStoriesLoad(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	storiesDir := filepath.Clean(filepath.Join(cwd, "..", "..", "stories"))

	entries, err := os.ReadDir(storiesDir)
	require.NoError(t, err, "read %s", storiesDir)

	checked := 0
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		appYAML := filepath.Join(storiesDir, ent.Name(), "app.yaml")
		if _, statErr := os.Stat(appYAML); statErr != nil {
			continue // not an app dir
		}
		t.Run(ent.Name(), func(t *testing.T) {
			_, loadErr := app.Load(appYAML)
			require.NoError(t, loadErr,
				"app.Load(%s) must succeed — a child-import drift would surface here as the same error a user sees at TUI startup", appYAML)
		})
		checked++
	}
	require.Greater(t, checked, 0, "expected at least one story app.yaml under %s", storiesDir)
}
