package hygiene

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildPlanPrunesOldCIRunSidecarsByRetention(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".capsules", "ci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunBundle(t, dir, "old", time.Unix(10, 0))
	writeRunBundle(t, dir, "new", time.Unix(20, 0))

	plan, err := BuildPlan(context.Background(), Options{ProjectRoot: root, KeepRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 3 {
		t.Fatalf("candidates %#v", plan.Candidates)
	}
	for _, c := range plan.Candidates {
		if c.Kind != "ci-run" || !c.Safe || filepath.Base(c.Path)[:3] != "old" {
			t.Fatalf("candidate %#v", c)
		}
	}
}

func TestBuildPlanReturnsEmptyCandidateSlice(t *testing.T) {
	plan, err := BuildPlan(context.Background(), Options{ProjectRoot: t.TempDir(), KeepRuns: 20})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Candidates == nil || len(plan.Candidates) != 0 {
		t.Fatalf("candidates %#v", plan.Candidates)
	}
}

func TestApplyRemovesOnlyPlannedProjectRunFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".capsules", "ci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunBundle(t, dir, "old", time.Unix(10, 0))
	writeRunBundle(t, dir, "new", time.Unix(20, 0))

	result, err := Apply(context.Background(), Options{ProjectRoot: root, KeepRuns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 3 {
		t.Fatalf("removed %#v", result.Removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.run.json")); !os.IsNotExist(err) {
		t.Fatalf("old run still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.run.json")); err != nil {
		t.Fatalf("new run was removed: %v", err)
	}
}

func TestProjectCacheAndGoCacheRequireExplicitInclusion(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, ".capsules", "cache", "runstatus")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "blob"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	goCache := filepath.Join(t.TempDir(), "gocache")
	if err := os.MkdirAll(goCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goCache, "blob"), []byte("go-cache"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(context.Background(), Options{ProjectRoot: root, GoCachePath: goCache})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("implicit cache candidates %#v", plan.Candidates)
	}
	plan, err = BuildPlan(context.Background(), Options{ProjectRoot: root, IncludeCapsuleCache: true, IncludeGoBuildCache: true, GoCachePath: goCache})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("explicit cache candidates %#v", plan.Candidates)
	}
}

func TestApplyUsesInjectedGoCacheCleaner(t *testing.T) {
	root := t.TempDir()
	goCache := filepath.Join(t.TempDir(), "gocache")
	if err := os.MkdirAll(goCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goCache, "blob"), []byte("go-cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cleaned bool
	result, err := Apply(context.Background(), Options{ProjectRoot: root, IncludeGoBuildCache: true, GoCachePath: goCache, CleanGoCache: func(context.Context) error {
		cleaned = true
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !cleaned || len(result.Removed) != 1 || result.Removed[0].Kind != "go-build-cache" {
		t.Fatalf("result=%#v cleaned=%v", result, cleaned)
	}
}

func writeRunBundle(t *testing.T, dir, id string, mod time.Time) {
	t.Helper()
	for _, suffix := range []string{".run.json", ".receipt.json", ".trace.json"} {
		path := filepath.Join(dir, id+suffix)
		if err := os.WriteFile(path, []byte(id+suffix), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}
