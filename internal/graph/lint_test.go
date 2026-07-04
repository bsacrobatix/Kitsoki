package graph

import "testing"

func TestLint_GoodFixturesClean(t *testing.T) {
	for _, path := range []string{"testdata/good/minimal.yaml", "testdata/good/bundle-min"} {
		t.Run(path, func(t *testing.T) {
			cat, err := LoadCatalog(path)
			if err != nil {
				t.Fatalf("LoadCatalog: %v", err)
			}
			if issues := Lint(cat); len(issues) != 0 {
				t.Errorf("Lint(%s) = %v, want no issues", path, issues)
			}
		})
	}
}

func TestLint_SeedCorpusClean(t *testing.T) {
	cat, err := LoadCatalog("../../docs/proposals/project-object-graph/seed-objects.yaml")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if issues := Lint(cat); len(issues) != 0 {
		t.Errorf("Lint(seed-objects.yaml) found %d issue(s), want 0: %v", len(issues), issues)
	}
}

func TestLint_BadFixtures(t *testing.T) {
	cases := []struct {
		file     string
		wantKind string
	}{
		{"testdata/lint/dangling-ref.yaml", "dangling-ref"},
		{"testdata/lint/type-mismatch.yaml", "type-mismatch"},
		{"testdata/lint/cycle.yaml", "cycle"},
		{"testdata/lint/visibility-leak.yaml", "visibility-leak"},
	}
	for _, tc := range cases {
		t.Run(tc.wantKind, func(t *testing.T) {
			cat, err := LoadCatalog(tc.file)
			if err != nil {
				t.Fatalf("LoadCatalog(%s): %v", tc.file, err)
			}
			issues := Lint(cat)
			if len(issues) == 0 {
				t.Fatalf("Lint(%s) found no issues, want at least one %q", tc.file, tc.wantKind)
			}
			found := false
			for _, iss := range issues {
				if iss.Kind == tc.wantKind {
					found = true
				}
			}
			if !found {
				t.Errorf("Lint(%s) = %v, want an issue of kind %q", tc.file, issues, tc.wantKind)
			}
		})
	}
}

func TestLint_CycleReportsBothNodes(t *testing.T) {
	cat, err := LoadCatalog("testdata/lint/cycle.yaml")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	issues := Lint(cat)
	seen := map[NodeID]bool{}
	for _, iss := range issues {
		if iss.Kind == "cycle" {
			seen[iss.Node] = true
		}
	}
	if !seen["change-a"] || !seen["change-b"] {
		t.Errorf("expected cycle issues for both change-a and change-b, got %v", issues)
	}
}
