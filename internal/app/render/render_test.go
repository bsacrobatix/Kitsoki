package render_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/app/render"
)

func TestMarkdown_NilAppDefReturnsError(t *testing.T) {
	if _, err := render.Markdown(nil); err == nil {
		t.Fatal("expected error for nil AppDef, got nil")
	}
}

// TestMarkdown_CloakGolden locks the rendered Markdown for the canonical
// cloak example against testdata/apps/cloak/APP.md. The committed APP.md is
// the source of truth; regenerate it with `kitsoki render` when the format
// changes intentionally.
func TestMarkdown_CloakGolden(t *testing.T) {
	repoRoot := repoRoot(t)
	appPath := filepath.Join(repoRoot, "testdata", "apps", "cloak", "app.yaml")
	goldenPath := filepath.Join(repoRoot, "testdata", "apps", "cloak", "APP.md")

	def, err := app.Load(appPath)
	if err != nil {
		t.Fatalf("load %s: %v", appPath, err)
	}
	got, err := render.Markdown(def)
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("rendered cloak does not match %s.\n"+
			"To regenerate the golden file: kitsoki render %s -o %s",
			goldenPath, appPath, goldenPath)
	}
}

// TestMarkdown_SmokeAllTestdataApps ensures every example app renders without
// error and produces a non-empty document containing the structural section
// headers Markdown() is expected to emit.
func TestMarkdown_SmokeAllTestdataApps(t *testing.T) {
	repoRoot := repoRoot(t)
	apps := []string{"cloak", "dev-story", "background_jobs", "proposal_smoke"}
	for _, name := range apps {
		name := name
		t.Run(name, func(t *testing.T) {
			appPath := filepath.Join(repoRoot, "testdata", "apps", name, "app.yaml")
			def, err := app.Load(appPath)
			if err != nil {
				t.Fatalf("load %s: %v", appPath, err)
			}
			out, err := render.Markdown(def)
			if err != nil {
				t.Fatalf("Markdown: %v", err)
			}
			if len(out) == 0 {
				t.Fatal("Markdown returned empty output")
			}
			body := string(out)
			for _, section := range []string{"## Overview", "## State Diagram", "## Intents"} {
				if !strings.Contains(body, section) {
					t.Errorf("output missing section %q", section)
				}
			}
		})
	}
}

// TestMarkdown_MinimalAppDef exercises the renderer on a hand-built fixture
// to lock specific format choices that are easy to regress and not obvious
// from the cloak golden alone.
func TestMarkdown_MinimalAppDef(t *testing.T) {
	def := &app.AppDef{
		App: app.AppMeta{
			ID:      "tiny",
			Title:   "Tiny",
			Version: "0.1.0",
		},
		Root: "start",
		States: map[string]*app.State{
			"start": {
				On: map[string][]app.Transition{
					"go": {{Target: "end"}},
				},
			},
			"end": {},
		},
		Intents: map[string]app.Intent{
			"go": {Title: "Move forward"},
		},
		World: map[string]app.VarDef{
			"count": {Type: "int", Default: 0},
		},
	}

	out, err := render.Markdown(def)
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	body := string(out)

	wantSubstrings := []string{
		"# Tiny",
		"**Version** 0.1.0",
		"App ID: `tiny`",
		"```mermaid",
		"## World Variables",
		"| `count` | `int` |",
		"## Intents",
		"`go` — Move forward",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(body, s) {
			t.Errorf("output missing %q", s)
		}
	}
}

// repoRoot walks up from the test's CWD to find the kitsoki module root.
// Tests run from the package directory, so we step up two levels.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/app/render → walk up to repo root.
	root := filepath.Clean(filepath.Join(cwd, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("could not locate repo root from %s: %v", cwd, err)
	}
	return root
}
