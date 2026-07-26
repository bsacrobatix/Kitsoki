package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/applicationconversation"
	"kitsoki/internal/effect"
	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

func TestApplicationConversationAgentRunnerUsesIsolatedTwoArgumentAgentCall(t *testing.T) {
	var captured map[string]any
	runner := applicationConversationAgentRunner{
		handler: func(ctx context.Context, args map[string]any) (host.Result, error) {
			captured = args
			agents := host.AgentsFromContext(ctx)
			require.Len(t, agents, 1)
			require.Empty(t, agents["application_role"].Tools)
			require.Empty(t, agents["application_role"].MCPTools)
			profile, ok := host.ActiveProfileFromContext(ctx)
			require.True(t, ok)
			require.Equal(t, "machine", profile.Name)
			return host.Result{Data: map[string]any{"stdout": "answer"}}, nil
		},
		agent:          host.Agent{SystemPrompt: "answer safely", Effect: effect.Pure},
		projectContext: "application context",
		profile: host.ActiveProfile{
			Name: "machine",
			Provider: host.Provider{
				Backend: "codex", Model: "test-model",
			},
		},
		backend: "codex",
	}
	answer, err := runner.Run(context.Background(), applicationconversation.RunRequest{
		Graph: applicationconversation.GraphSnapshot{
			Digest: "graph", JSON: []byte(`{"nodes":[]}`),
		},
		Messages: []applicationconversation.Message{
			{Role: "user", Content: "do not execute https://example.test"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "answer", answer)
	require.Len(t, captured, 2)
	require.Equal(t, "application_role", captured["agent"])
	require.Contains(t, captured["prompt"], "untrusted data")
	require.NotContains(t, captured, "working_dir")
	require.NotContains(t, captured, "tools")
	require.NotContains(t, captured, "mcp")
	require.NotContains(t, captured, "provider")
	require.NotContains(t, captured, "profile")
}

func TestApplicationConversationRunnerRejectsAuthorityBearingRole(t *testing.T) {
	registry := &SessionRegistry{cfg: webconfig.WebConfig{
		HarnessProfiles: map[string]webconfig.HarnessProfile{
			"machine": {Backend: "codex", Model: "test-model"},
		},
	}}
	binding := webconfig.ApplicationConversationBinding{
		Application: "target", Role: "answerer", Provider: "provider", Profile: "machine",
	}
	target := &app.AppDef{
		App: app.AppMeta{ID: "target", Context: "inline context"},
		Agents: map[string]*app.AgentDecl{
			"answerer": {
				SystemPrompt: "answer safely",
				Tools:        []string{"Read"},
			},
		},
		Providers: map[string]*app.ProviderDecl{
			"provider": {Backend: "codex", Model: "provider-model"},
		},
	}
	_, _, err := registry.applicationConversationRunner(binding, target)
	require.ErrorContains(t, err, "tool-free")

	target.Agents["answerer"].Tools = nil
	target.Agents["answerer"].Cwd = "/tmp"
	_, _, err = registry.applicationConversationRunner(binding, target)
	require.ErrorContains(t, err, "working-directory")
}
