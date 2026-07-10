package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJourneyPackRejectsUnsafeOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".kitsoki", "stories", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".kitsoki", "stories", "demo", "app.yaml"), []byte("app: {id: demo}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journey.yaml")
	body := "schema: kitsoki/journey-pack/v1\nid: demo/onboarding\ntitle: Demo\nstory: {app: .kitsoki/stories/demo/app.yaml}\nmatrix: {personas: [author], scenarios: [onboarding], transports: [web]}\noutputs: {tutorial: {publish: /tmp/not-portable}}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, root, _, err := loadJourney(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJourney(p, root); err == nil {
		t.Fatal("expected absolute output path to fail")
	}
}
