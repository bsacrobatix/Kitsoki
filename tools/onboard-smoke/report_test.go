//go:build onboardsisters

package onboardsmoke

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOnboardSisterReportDeck(t *testing.T) {
	requireTool(t, "python3")
	kitsokiRoot := repoRoot(t)
	runID := "test-" + filepath.Base(t.TempDir())

	paths, err := writeOnboardSistersReport(kitsokiRoot, runID, []onboardSisterReportItem{
		{
			ProjectID:      "slidey",
			ProjectTitle:   "Slidey",
			Stack:          "node/vue/vite declarative deck engine",
			BaselineCommit: "1a018e5939ee662d37c392067ce496d9c94d1b68",
			ConfigPath:     ".kitsoki.yaml",
			ProfilePath:    ".kitsoki/project-profile.yaml",
			InstancePath:   ".kitsoki/stories/slidey-dev/app.yaml",
		},
	})
	if err != nil {
		t.Fatalf("write onboard sister report deck: %v", err)
	}
	for _, path := range []string{paths.Summary, paths.Report, paths.Deck} {
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		} else if info.Size() == 0 {
			t.Fatalf("%s is empty", path)
		}
	}
}
