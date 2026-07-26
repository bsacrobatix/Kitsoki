package webconfig

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/applicationconversation"
)

// ApplicationConversationBinding is an operator-owned capability binding for
// one caller application. Stories supply only chat_id and question.
type ApplicationConversationBinding struct {
	Application string                         `yaml:"application"`
	Role        string                         `yaml:"role"`
	Graph       ApplicationConversationGraph   `yaml:"graph"`
	Provider    string                         `yaml:"provider"`
	Profile     string                         `yaml:"profile"`
	Bounds      applicationconversation.Bounds `yaml:"bounds,omitempty"`
}

// ApplicationConversationGraph fixes the projection exposed to the bound role.
type ApplicationConversationGraph struct {
	Catalog  string   `yaml:"catalog"`
	Audience string   `yaml:"audience"`
	Fields   []string `yaml:"fields,omitempty"`
	MaxNodes int      `yaml:"max_nodes"`
}

func (cfg *WebConfig) resolveApplicationConversations() error {
	callers := make([]string, 0, len(cfg.ApplicationConversations))
	for caller := range cfg.ApplicationConversations {
		callers = append(callers, caller)
	}
	sort.Strings(callers)
	for _, caller := range callers {
		binding := cfg.ApplicationConversations[caller]
		prefix := "application_conversations." + caller
		switch {
		case !validApplicationConversationID(caller):
			return fmt.Errorf("application_conversations caller application id must be opaque")
		case !validApplicationConversationID(binding.Application):
			return fmt.Errorf("%s.application is required and must be opaque", prefix)
		case !validApplicationConversationID(binding.Role):
			return fmt.Errorf("%s.role is required and must be opaque", prefix)
		case !validApplicationConversationID(binding.Provider):
			return fmt.Errorf("%s.provider is required and must be opaque", prefix)
		case !validApplicationConversationID(binding.Profile):
			return fmt.Errorf("%s.profile is required and must be opaque", prefix)
		}
		if _, ok := cfg.HarnessProfiles[binding.Profile]; !ok {
			return fmt.Errorf("%s.profile %q is not a declared harness profile", prefix, binding.Profile)
		}
		catalog := strings.TrimSpace(binding.Graph.Catalog)
		if catalog == "" {
			return fmt.Errorf("%s.graph.catalog is required", prefix)
		}
		if !strings.HasPrefix(catalog, "pg:") {
			if filepath.IsAbs(catalog) || filepath.Clean(catalog) != catalog ||
				catalog == ".." || strings.HasPrefix(catalog, "../") ||
				strings.Contains(catalog, "://") {
				return fmt.Errorf("%s.graph.catalog must be a repository-relative path or pg: ref", prefix)
			}
		} else if !validApplicationConversationID(strings.TrimPrefix(catalog, "pg:")) {
			return fmt.Errorf("%s.graph.catalog has an invalid pg: ref", prefix)
		}
		if binding.Graph.Audience != "public" && binding.Graph.Audience != "internal" {
			return fmt.Errorf("%s.graph.audience must be public or internal", prefix)
		}
		if binding.Graph.MaxNodes < 1 {
			return fmt.Errorf("%s.graph.max_nodes must be positive", prefix)
		}
		seen := map[string]bool{}
		for _, field := range binding.Graph.Fields {
			if !validApplicationConversationID(field) || seen[field] {
				return fmt.Errorf("%s.graph.fields must contain unique opaque field ids", prefix)
			}
			seen[field] = true
		}
		bounds := binding.Bounds
		defaults := applicationconversation.DefaultBounds()
		if bounds.MaxQuestionBytes == 0 {
			bounds.MaxQuestionBytes = defaults.MaxQuestionBytes
		}
		if bounds.MaxAnswerBytes == 0 {
			bounds.MaxAnswerBytes = defaults.MaxAnswerBytes
		}
		if bounds.MaxHistoryBytes == 0 {
			bounds.MaxHistoryBytes = defaults.MaxHistoryBytes
		}
		if bounds.MaxHistoryEntries == 0 {
			bounds.MaxHistoryEntries = defaults.MaxHistoryEntries
		}
		if bounds.MaxGraphBytes == 0 {
			bounds.MaxGraphBytes = defaults.MaxGraphBytes
		}
		if err := bounds.Validate(); err != nil {
			return fmt.Errorf("%s.bounds: %w", prefix, err)
		}
		binding.Bounds = bounds
		cfg.ApplicationConversations[caller] = binding
	}
	return nil
}

func validApplicationConversationID(value string) bool {
	return value == strings.TrimSpace(value) && validReviewedFeedbackID(value)
}
