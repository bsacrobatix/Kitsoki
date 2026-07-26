package main

import (
	"path/filepath"
	"testing"
)

func TestApplicationCommandIncludesManagedLifecycle(t *testing.T) {
	command := applicationCmd()
	for _, name := range []string{"build", "dev"} {
		found, _, err := command.Find([]string{name})
		if err != nil || found == command || found.Name() != name {
			t.Fatalf("application command %q missing: found=%v err=%v", name, found, err)
		}
	}
}

func TestApplicationBackendAddress(t *testing.T) {
	if got, err := applicationBackendAddress("http://127.0.0.1:7777"); err != nil || got != "127.0.0.1:7777" {
		t.Fatalf("address = %q, %v", got, err)
	}
	for _, invalid := range []string{"127.0.0.1:7777", "file:///tmp/socket"} {
		if _, err := applicationBackendAddress(invalid); err == nil {
			t.Fatalf("expected %q to fail", invalid)
		}
	}
}

func TestApplicationProjectRootUsesNearestMarker(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".kitsoki.yaml"), "")
	story := filepath.Join(root, "stories", "demo", "app.yaml")
	writeFile(t, story, "")
	got, err := applicationProjectRoot(story)
	if err != nil || got != root {
		t.Fatalf("root = %q, %v; want %q", got, err, root)
	}
}
