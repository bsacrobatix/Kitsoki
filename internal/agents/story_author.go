package agents

import _ "embed"

//go:embed story_author.md
var storyAuthorPrompt string

// storyAuthor returns the builtin story-author Agent: a conversational
// YAML/story editor that drives the host.authoring.* tool surface.
func storyAuthor() Agent {
	return Agent{
		Name:         "story-author",
		SystemPrompt: storyAuthorPrompt,
		Model:        "",
		Tools: []string{
			"host.authoring.propose",
			"host.authoring.apply",
			"host.authoring.discard",
		},
		DefaultCwd: "",
	}
}
