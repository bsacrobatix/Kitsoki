package app

import (
	"strings"

	"kitsoki/internal/agents"
	"kitsoki/internal/storyauthoring"
)

const storyAuthoringPrompt = `Apply this story-authoring proposal to the current Kitsoki story.

Story root: the task working directory
Return state: {{ world.story_authoring_return_state }}

Proposal:
{{ world.story_authoring_request }}

You may update app.yaml, rooms/*.yaml, prompts/*.md, scripts/*.star and their sidecars, schemas, or flow fixtures when the change calls for it. Keep edits scoped to the proposal, preserve existing story style, and add or update no-LLM validation fixtures when behavior changes.

When done, call submit() with a concise JSON note:
- summary: one sentence describing what changed
- details: important implementation or validation context
- files_changed: paths you changed
- validation: validation you ran, or why it was not run
- remaining_work: any follow-up that remains`

// injectBuiltinStoryAuthoringRoom gives every loaded story an explicit on-path
// room for editing the story itself. It is intentionally additive: story
// declarations with the same names win, and the builtin fills only missing
// world keys, intents, state, and entry arcs.
func injectBuiltinStoryAuthoringRoom(def *AppDef, file, baseDir string) {
	if def == nil {
		return
	}

	ensureStoryAuthoringWorld(def)
	ensureStoryAuthoringIntents(def)
	if len(def.Hosts) > 0 {
		def.Hosts = appendUnique(def.Hosts, "host.agent.task")
	}

	if def.States == nil {
		def.States = map[string]*State{}
	}
	if _, exists := def.States[storyauthoring.RoomState]; !exists {
		def.States[storyauthoring.RoomState] = storyAuthoringState(baseDir)
	}
	injectStoryAuthoringEntryArcs("", def.States)
}

func ensureStoryAuthoringWorld(def *AppDef) {
	if def.World == nil {
		def.World = map[string]VarDef{}
	}
	if _, exists := def.World[storyauthoring.RequestWorld]; !exists {
		def.World[storyauthoring.RequestWorld] = VarDef{Type: "string", Default: ""}
	}
	if _, exists := def.World[storyauthoring.NoteWorld]; !exists {
		def.World[storyauthoring.NoteWorld] = VarDef{Type: "object", Default: map[string]any{}}
	}
	if _, exists := def.World[storyauthoring.ReturnStateWorld]; !exists {
		def.World[storyauthoring.ReturnStateWorld] = VarDef{Type: "string", Default: ""}
	}
}

func ensureStoryAuthoringIntents(def *AppDef) {
	if def.Intents == nil {
		def.Intents = map[string]Intent{}
	}
	if _, exists := def.Intents[storyauthoring.EnterIntent]; !exists {
		def.Intents[storyauthoring.EnterIntent] = Intent{
			Title:       "Author story",
			Description: "Open the story-authoring room with a proposed story edit.",
			Hidden:      true,
			Slots: map[string]Slot{
				"proposal": {
					Type:        "string",
					Description: "The story change to make.",
				},
			},
		}
	}
	if _, exists := def.Intents[storyauthoring.CaptureIntent]; !exists {
		def.Intents[storyauthoring.CaptureIntent] = Intent{
			Title:       "Submit proposal",
			Description: "Send a story-authoring proposal to the story author.",
			Slots: map[string]Slot{
				"request": {
					Type:        "string",
					Required:    true,
					Description: "The story change to make.",
				},
			},
		}
	}
	if _, exists := def.Intents[storyauthoring.ReturnIntent]; !exists {
		def.Intents[storyauthoring.ReturnIntent] = Intent{
			Title:       "Return to story",
			Description: "Leave story authoring and return to the previous room.",
		}
	}
	if _, exists := def.Intents[storyauthoring.ClearIntent]; !exists {
		def.Intents[storyauthoring.ClearIntent] = Intent{
			Title:       "Clear proposal",
			Description: "Clear the current story-authoring proposal and result.",
		}
	}
}

func storyAuthoringState(baseDir string) *State {
	workingDir := strings.TrimSpace(baseDir)
	if workingDir == "" {
		workingDir = "."
	}

	return &State{
		Description:   "Story authoring",
		WriteMode:     WriteModeReadOnly,
		Transcript:    "persistent",
		DefaultIntent: storyauthoring.CaptureIntent,
		RelevantWorld: []string{
			storyauthoring.RequestWorld,
			storyauthoring.ReturnStateWorld,
		},
		Menu: []string{
			storyauthoring.CaptureIntent,
			storyauthoring.ReturnIntent,
			storyauthoring.ClearIntent,
		},
		AgentOffRamp: &OffRampDef{Agent: agents.NameStoryExplainer, enabled: true},
		View: View{
			Elements: []ViewElement{
				{Kind: "heading", Source: "Story authoring"},
				{Kind: "prose", Source: `Current proposal: {{ world.story_authoring_request|default:"(none)" }}`},
				{Kind: "prose", Source: `Last result: {{ world.story_authoring_note.summary|default:"(none)" }}`},
				{Kind: "prose", Source: `Return: {{ world.story_authoring_return_state|default:"(story root)" }}`},
			},
		},
		OnEnter: []Effect{{
			When:   "world.story_authoring_request != ''",
			Invoke: "host.agent.task",
			Once:   true,
			With: map[string]any{
				"agent":       agents.NameStoryAuthor,
				"working_dir": workingDir,
				"acceptance": map[string]any{
					"schema": storyauthoring.SchemaRef,
				},
				"context": map[string]any{
					"prompt": storyAuthoringPrompt,
				},
			},
			Bind: map[string]string{
				storyauthoring.NoteWorld: "submitted",
			},
			OnError: storyauthoring.RoomState,
		}},
		On: map[string][]Transition{
			storyauthoring.CaptureIntent: {{
				Target: storyauthoring.RoomState,
				Effects: []Effect{{
					Set: map[string]any{
						storyauthoring.RequestWorld: "{{ slots.request }}",
						storyauthoring.NoteWorld:    map[string]any{},
					},
				}},
			}},
			storyauthoring.ClearIntent: {{
				Target: storyauthoring.RoomState,
				Effects: []Effect{{
					Set: map[string]any{
						storyauthoring.RequestWorld: "",
						storyauthoring.NoteWorld:    map[string]any{},
					},
				}},
			}},
			storyauthoring.ReturnIntent: {{
				When:   "world.story_authoring_return_state != ''",
				Target: "{{ world.story_authoring_return_state }}",
			}, {
				Default: true,
				Target:  ".",
			}},
		},
	}
}

func injectStoryAuthoringEntryArcs(prefix string, states map[string]*State) {
	for _, name := range sortedStateNames(states) {
		s := states[name]
		if s == nil {
			continue
		}
		statePath := joinPath(prefix, name)
		if statePath != storyauthoring.RoomState && !s.Terminal {
			if s.On == nil {
				s.On = map[string][]Transition{}
			}
			if _, exists := s.On[storyauthoring.EnterIntent]; !exists {
				pushHistory := false
				s.On[storyauthoring.EnterIntent] = []Transition{{
					Target:      storyauthoring.RoomState,
					PushHistory: &pushHistory,
					Effects: []Effect{{
						Set: map[string]any{
							storyauthoring.RequestWorld:     "{{ slots.proposal ?? '' }}",
							storyauthoring.NoteWorld:        map[string]any{},
							storyauthoring.ReturnStateWorld: statePath,
						},
					}},
				}}
			}
		}
		if len(s.States) > 0 {
			injectStoryAuthoringEntryArcs(statePath, s.States)
		}
	}
}
