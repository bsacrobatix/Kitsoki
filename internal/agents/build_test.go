package agents

import (
	"strings"
	"testing"
)

// TestBuildRegistry_NilReturnsBuiltins asserts that BuildRegistry(nil)
// hands back a registry pre-seeded with the bundled builtins (today:
// story-author).
func TestBuildRegistry_NilReturnsBuiltins(t *testing.T) {
	r, err := BuildRegistry(nil)
	if err != nil {
		t.Fatalf("BuildRegistry(nil) error = %v", err)
	}
	if _, ok := r.Get("story-author"); !ok {
		t.Error("expected builtin story-author to be registered")
	}
}

// TestBuildRegistry_RegistersCustom asserts that a new spec is added to
// the registry alongside the builtins.
func TestBuildRegistry_RegistersCustom(t *testing.T) {
	r, err := BuildRegistry([]BuildSpec{{
		Name:         "weather-bot",
		SystemPrompt: "be the weather",
		Tools:        []string{"host.weather.forecast"},
		DefaultCwd:   "/tmp/weather",
	}})
	if err != nil {
		t.Fatalf("BuildRegistry error = %v", err)
	}
	got, ok := r.Get("weather-bot")
	if !ok {
		t.Fatal("expected weather-bot in registry")
	}
	if got.SystemPrompt != "be the weather" {
		t.Errorf("SystemPrompt = %q, want be the weather", got.SystemPrompt)
	}
	if got.DefaultCwd != "/tmp/weather" {
		t.Errorf("DefaultCwd = %q, want /tmp/weather", got.DefaultCwd)
	}
	if _, ok := r.Get("story-author"); !ok {
		t.Error("builtins must remain after registering a custom agent")
	}
}

// TestBuildRegistry_OverridesBuiltin asserts that a spec named after a
// builtin replaces it in the returned registry.
func TestBuildRegistry_OverridesBuiltin(t *testing.T) {
	r, err := BuildRegistry([]BuildSpec{{
		Name:         "story-author",
		SystemPrompt: "house-style override",
		Tools:        []string{"host.authoring.propose"},
	}})
	if err != nil {
		t.Fatalf("BuildRegistry error = %v", err)
	}
	got, ok := r.Get("story-author")
	if !ok {
		t.Fatal("story-author missing after override")
	}
	if !strings.Contains(got.SystemPrompt, "house-style override") {
		t.Errorf("SystemPrompt = %q, expected house-style override", got.SystemPrompt)
	}
	if len(got.Tools) != 1 || got.Tools[0] != "host.authoring.propose" {
		t.Errorf("Tools = %v, want [host.authoring.propose]", got.Tools)
	}
}

// TestBuildRegistry_EmptyNameRejected covers the input-validation path.
func TestBuildRegistry_EmptyNameRejected(t *testing.T) {
	_, err := BuildRegistry([]BuildSpec{{
		Name:         "",
		SystemPrompt: "x",
	}})
	if err == nil {
		t.Fatal("expected error for empty Name")
	}
	if !strings.Contains(err.Error(), "empty Name") {
		t.Errorf("error %q does not mention empty Name", err)
	}
}

// TestBuildRegistry_EmptySystemPromptRejected covers the other half.
func TestBuildRegistry_EmptySystemPromptRejected(t *testing.T) {
	_, err := BuildRegistry([]BuildSpec{{
		Name:         "ghost",
		SystemPrompt: "",
	}})
	if err == nil {
		t.Fatal("expected error for empty SystemPrompt")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the agent", err)
	}
	if !strings.Contains(err.Error(), "SystemPrompt") {
		t.Errorf("error %q does not mention SystemPrompt", err)
	}
}
