package testrunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDeterministicFlowFixtureRejectsRealHostBindings(t *testing.T) {
	err := validateDeterministicFlowFixture("flow.yaml", &FlowFixture{
		HostBindings: map[string]string{"runner": "host.run"},
	})
	if err == nil || !strings.Contains(err.Error(), "forbids host_bindings") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDeterministicFlowFixtureRejectsCassetteRecording(t *testing.T) {
	dir := t.TempDir()
	cassette := filepath.Join(dir, "host-cassette.yaml")
	if err := os.WriteFile(cassette, []byte(`
kind: host_cassette
app_id: test
record_mode: new_episodes
episodes: []
`), 0o644); err != nil {
		t.Fatalf("write cassette: %v", err)
	}
	err := validateDeterministicFlowFixture(
		filepath.Join(dir, "flow.yaml"),
		&FlowFixture{HostCassette: filepath.Base(cassette)},
	)
	if err == nil || !strings.Contains(err.Error(), "forbids host cassette record mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateDeterministicFlowFixtureRejectsHTTPRecordingOverride(t *testing.T) {
	dir := t.TempDir()
	cassette := filepath.Join(dir, "http-cassette.yaml")
	if err := os.WriteFile(cassette, []byte(`
kind: http_cassette
record_mode: none
episodes: []
`), 0o644); err != nil {
		t.Fatalf("write cassette: %v", err)
	}
	t.Setenv("KITSOKI_HTTP_CASSETTE_RECORD", "new_episodes")
	err := validateDeterministicFlowFixture(
		filepath.Join(dir, "flow.yaml"),
		&FlowFixture{StarlarkHTTPCassette: filepath.Base(cassette)},
	)
	if err == nil || !strings.Contains(err.Error(), "forbids HTTP cassette record mode") {
		t.Fatalf("error = %v", err)
	}
}
