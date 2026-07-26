package webconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveApplicationConversationsDefaultsAndValidatesBinding(t *testing.T) {
	cfg := WebConfig{
		HarnessProfiles: map[string]HarnessProfile{
			"production": {Backend: "codex", Model: "gpt-test"},
		},
		ApplicationConversations: map[string]ApplicationConversationBinding{
			"caller": {
				Application: "knowledge-app",
				Role:        "answerer",
				Provider:    "application-model",
				Profile:     "production",
				Graph: ApplicationConversationGraph{
					Catalog: "catalog/knowledge.yaml", Audience: "public",
					Fields: []string{"summary", "url"}, MaxNodes: 200,
				},
			},
		},
	}
	require.NoError(t, cfg.resolveApplicationConversations())
	require.Equal(
		t,
		8192,
		cfg.ApplicationConversations["caller"].Bounds.MaxQuestionBytes,
	)
	require.Equal(
		t,
		40,
		cfg.ApplicationConversations["caller"].Bounds.MaxHistoryEntries,
	)
}

func TestResolveApplicationConversationsRejectsAuthorityAndUnknownProfile(t *testing.T) {
	binding := ApplicationConversationBinding{
		Application: "knowledge-app",
		Role:        "answerer",
		Provider:    "application-model",
		Profile:     "missing",
		Graph: ApplicationConversationGraph{
			Catalog: "../outside.yaml", Audience: "public", MaxNodes: 10,
		},
	}
	cfg := WebConfig{
		HarnessProfiles:          map[string]HarnessProfile{},
		ApplicationConversations: map[string]ApplicationConversationBinding{"caller": binding},
	}
	require.ErrorContains(t, cfg.resolveApplicationConversations(), "not a declared harness profile")

	cfg.HarnessProfiles["missing"] = HarnessProfile{Model: "model"}
	require.ErrorContains(t, cfg.resolveApplicationConversations(), "repository-relative")

	binding.Graph.Catalog = "https://example.test/catalog"
	cfg.ApplicationConversations["caller"] = binding
	require.ErrorContains(t, cfg.resolveApplicationConversations(), "repository-relative")
}

func TestMergeConfigMergesApplicationConversationBindingsByCaller(t *testing.T) {
	base := WebConfig{ApplicationConversations: map[string]ApplicationConversationBinding{
		"a": {Application: "base-a"},
		"b": {Application: "base-b"},
	}}
	local := WebConfig{ApplicationConversations: map[string]ApplicationConversationBinding{
		"b": {Application: "local-b"},
		"c": {Application: "local-c"},
	}}
	merged := mergeConfig(base, local)
	require.Equal(t, "base-a", merged.ApplicationConversations["a"].Application)
	require.Equal(t, "local-b", merged.ApplicationConversations["b"].Application)
	require.Equal(t, "local-c", merged.ApplicationConversations["c"].Application)
}
