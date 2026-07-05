package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kitsoki/internal/app"
)

func TestBugfixAgentsCarryExternalWritePolicy(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	def, err := app.Load(filepath.Join(repoRoot, "stories", "bugfix", "app.yaml"))
	if err != nil {
		t.Fatalf("load bugfix story: %v", err)
	}

	wantAgents := []string{
		"reproducer",
		"triager",
		"proposer",
		"implementer",
		"test_author",
		"validator",
		"judge",
	}
	wantPhrases := []string{
		"External-write policy",
		"do not create, comment on, close, transition, or otherwise mutate external tickets",
		"do not use raw gh or other GitHub CLIs",
		"Return artifacts only",
		"story done-room owns ticket comments and close-out through native host.gh.ticket/kitsoki gitops orchestration",
	}

	for _, name := range wantAgents {
		agent, ok := def.Agents[name]
		if !ok {
			t.Fatalf("agent %q missing from bugfix story", name)
		}
		for _, phrase := range wantPhrases {
			if !strings.Contains(agent.SystemPrompt, phrase) {
				t.Errorf("agent %q system prompt missing policy phrase %q", name, phrase)
			}
		}
	}
}
