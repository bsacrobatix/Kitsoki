package webconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// minimalStory is the smallest manifest app.Load accepts: app id/version, a
// root, and one state. No agents/host/oracle references so the load is pure.
const minimalStory = `app:
  id: %s
  version: 0.1.0
  title: %q

root: idle

states:
  idle:
    description: "Idle"
    view: "Idle."
`

const malformedStory = `app:
  id: broken
  version: 0.1.0

root: nowhere

states:
  idle:
    description: "Idle"
    view: "Idle."
`

func writeStory(t *testing.T, dir, id, title string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "app.yaml")
	content := []byte(fmt.Sprintf(minimalStory, id, title))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs %s: %v", path, err)
	}
	return abs
}

func TestLoad_MissingFileIsNotError(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("missing config should not error: %v", err)
	}
	if len(cfg.StoryDirs) != 0 {
		t.Fatalf("missing config should yield empty StoryDirs, got %v", cfg.StoryDirs)
	}
}

func TestLoad_ReadsStoryDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultConfigFile)
	if err := os.WriteFile(path, []byte("story_dirs:\n  - ./a\n  - ./b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(cfg.StoryDirs, []string{"./a", "./b"}) {
		t.Fatalf("got StoryDirs %v", cfg.StoryDirs)
	}
}

func TestResolve_Precedence(t *testing.T) {
	tests := []struct {
		name     string
		flagDirs []string
		cfg      WebConfig
		want     []string
	}{
		{
			name:     "flags win over config and default",
			flagDirs: []string{"/flag/one", "/flag/two"},
			cfg:      WebConfig{StoryDirs: []string{"/cfg"}},
			want:     []string{"/flag/one", "/flag/two"},
		},
		{
			name: "config wins over default when no flags",
			cfg:  WebConfig{StoryDirs: []string{"/cfg"}},
			want: []string{"/cfg"},
		},
		{
			name: "default when neither flags nor config",
			want: []string{"./stories"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.flagDirs, tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Resolve = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolve_ReturnsCopy(t *testing.T) {
	// The default path must not hand out the package-level slice.
	got := Resolve(nil, WebConfig{})
	got[0] = "mutated"
	again := Resolve(nil, WebConfig{})
	if again[0] != "./stories" {
		t.Fatalf("Resolve leaked the default slice: got %v", again)
	}
}

func TestDiscoverStories_NestedValidAndMalformed(t *testing.T) {
	root := t.TempDir()

	// Two valid stories at different nesting depths.
	absAlpha := writeStory(t, filepath.Join(root, "alpha"), "alpha", "Alpha")
	absBeta := writeStory(t, filepath.Join(root, "nested", "beta"), "beta", "Beta")

	// One malformed story (root state references a nonexistent state) — it must
	// be skipped without aborting the walk or hiding its valid siblings.
	badDir := filepath.Join(root, "broken")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "app.yaml"), []byte(malformedStory), 0o644); err != nil {
		t.Fatal(err)
	}

	// A non-app.yaml file must be ignored.
	if err := os.WriteFile(filepath.Join(root, "alpha", "notes.yaml"), []byte("foo: bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	metas, err := DiscoverStories([]string{root})
	if err != nil {
		t.Fatalf("DiscoverStories: %v", err)
	}

	gotPaths := make([]string, 0, len(metas))
	for _, m := range metas {
		gotPaths = append(gotPaths, m.Path)
		if !filepath.IsAbs(m.Path) {
			t.Errorf("Path %q is not absolute", m.Path)
		}
		if m.Def == nil {
			t.Errorf("StoryMeta for %s has nil Def", m.Path)
		}
	}
	sort.Strings(gotPaths)

	want := []string{absAlpha, absBeta}
	sort.Strings(want)
	if !reflect.DeepEqual(gotPaths, want) {
		t.Fatalf("discovered %v, want %v (malformed must be skipped)", gotPaths, want)
	}

	// Spot-check the loaded def is the real thing, not a placeholder.
	for _, m := range metas {
		if m.Def.App.ID == "" {
			t.Errorf("loaded Def for %s has empty App.ID", m.Path)
		}
	}
}

func TestDiscoverStories_UnreadableRootIsError(t *testing.T) {
	_, err := DiscoverStories([]string{filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("expected an error for a nonexistent root dir")
	}
}
