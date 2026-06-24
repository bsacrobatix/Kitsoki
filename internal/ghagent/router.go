package ghagent

import (
	"kitsoki/internal/host"
)

// StoryPRBeat is the sentinel story value for the minimal PR autopilot beat.
// The full stories/pr-autopilot does not exist in round 1; the Dispatcher's
// spawn branch recognises this sentinel and runs the single pr_status-read +
// status-comment beat instead of loading a story app.yaml.
const StoryPRBeat = "pr-beat"

// Route is one label->story mapping: the story app.yaml path (or the StoryPRBeat
// sentinel) plus world seed keys merged into the spawned session's
// initial_world.
type Route struct {
	Story string
	World map[string]any
}

// LabelStoryMap is the configured router (epic: "configured, not hard-coded").
type LabelStoryMap map[string]Route

// DefaultLabelStoryMap is the round-1 default table:
//
//	bug     -> stories/bugfix    (judge_mode: llm_then_human)
//	feature -> stories/dev-story  (ticket_type: feature)
//	pr      -> the minimal pr-autopilot beat
func DefaultLabelStoryMap() LabelStoryMap {
	return LabelStoryMap{
		"bug":     {Story: "stories/bugfix", World: map[string]any{"judge_mode": "llm_then_human"}},
		"feature": {Story: "stories/dev-story", World: map[string]any{"ticket_type": "feature"}},
		"pr":      {Story: StoryPRBeat, World: map[string]any{}},
	}
}

// Classify maps a mention to a Route. PR objects route to the "pr" entry
// regardless of label; issues classify via host.GHClassifyType (label-then-title
// logic). Returns (Route, true) on a confident match, (_, false) when the class
// has no configured route (the caller posts guidance and parks — deferred to
// the guidance arc).
func (m LabelStoryMap) Classify(mention Mention, labels []string) (Route, bool) {
	if mention.Item.Kind == "pr" {
		r, ok := m["pr"]
		return r, ok
	}
	class := host.GHClassifyType(map[string]any{
		"labels": labelsToAny(labels),
		"title":  mention.Item.Title,
	})
	r, ok := m[class]
	return r, ok
}

func labelsToAny(labels []string) []any {
	out := make([]any, 0, len(labels))
	for _, l := range labels {
		out = append(out, l)
	}
	return out
}
