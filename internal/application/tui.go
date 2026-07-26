package application

import (
	"encoding/json"
	"fmt"
	"strings"

	"kitsoki/internal/app"
)

const TUIActionIntentPrefix = "application-action:"

// TUIProjection is the terminal-native projection of an application frame.
// View uses the existing typed element renderer; Targets preserve canonical
// semantic identity without encoding terminal coordinates into the frame.
type TUIProjection struct {
	View    app.View            `json:"view"`
	Targets []TUISemanticTarget `json:"targets,omitempty"`
	Actions []TUIAction         `json:"actions,omitempty"`
}

type TUISemanticTarget struct {
	Ref          string       `json:"ref"`
	Kind         SemanticKind `json:"kind"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	ProgramNode  string       `json:"program_node,omitempty"`
	Story        string       `json:"story"`
	ElementStart int          `json:"element_start"`
	ElementEnd   int          `json:"element_end"`
}

type TUIAction struct {
	ID          string         `json:"id"`
	Intent      string         `json:"intent,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	SemanticRef string         `json:"semantic_ref"`
	Enabled     bool           `json:"enabled"`
	Envelope    ActionEnvelope `json:"envelope"`
}

func ProjectTUI(frame Frame) (TUIProjection, error) {
	if err := frame.Validate(); err != nil {
		return TUIProjection{}, err
	}
	projection := TUIProjection{}
	add := func(node SemanticNode, elements ...app.ViewElement) {
		start := len(projection.View.Elements)
		projection.View.Elements = append(projection.View.Elements, elements...)
		projection.Targets = append(projection.Targets, TUISemanticTarget{
			Ref: node.Ref, Kind: node.Kind, Name: node.Name, Description: node.Description,
			ProgramNode: node.Source.ProgramNode, Story: node.Source.Story,
			ElementStart: start, ElementEnd: len(projection.View.Elements),
		})
	}

	add(frame.Semantic,
		app.ViewElement{Kind: "heading", Source: frame.Semantic.Name},
		app.ViewElement{Kind: "prose", Source: frame.Semantic.Description},
	)
	add(frame.PageSemantic,
		app.ViewElement{Kind: "heading", Source: frame.PageSemantic.Name},
		app.ViewElement{Kind: "prose", Source: frame.PageSemantic.Description},
	)
	for _, region := range frame.Regions {
		if !nodeVisible(region.State) {
			continue
		}
		add(region.Semantic,
			app.ViewElement{Kind: "heading", Source: region.Semantic.Name},
			app.ViewElement{Kind: "prose", Source: region.Semantic.Description},
		)
		for _, card := range region.Cards {
			if !nodeVisible(card.State) {
				continue
			}
			add(card.Semantic,
				app.ViewElement{Kind: "heading", Source: card.Semantic.Name},
				app.ViewElement{Kind: "prose", Source: card.Semantic.Description},
			)
			for _, element := range card.Body {
				projected, err := projectTUIElement(frame, element)
				if err != nil {
					return TUIProjection{}, fmt.Errorf("application: project card %q: %w", card.ID, err)
				}
				if element.Semantic != nil && element.Semantic.Ref != "" {
					add(*element.Semantic, projected...)
				} else {
					projection.View.Elements = append(projection.View.Elements, projected...)
				}
			}
			var actionItems []app.ChoiceItem
			for _, action := range card.Actions {
				if actionEnabled(action) {
					actionItems = append(actionItems, app.ChoiceItem{
						Label: action.Semantic.Name, Hint: action.Semantic.Description,
						Intent: TUIActionIntentPrefix + action.ID,
					})
				}
			}
			if len(actionItems) > 0 {
				projection.View.Elements = append(projection.View.Elements, app.ViewElement{
					Kind: "choice", ChoiceMode: "single",
					ChoicePrompt: "Actions", ChoiceItems: actionItems,
				})
			}
			for _, action := range card.Actions {
				projection.Actions = append(projection.Actions, tuiAction(frame, action))
			}
		}
	}
	return projection, nil
}

func actionEnabled(action Action) bool {
	return action.Enabled && (action.State.Enabled == nil || *action.State.Enabled)
}

func projectTUIElement(frame Frame, element Element) ([]app.ViewElement, error) {
	if !nodeVisible(element.State) {
		return nil, nil
	}
	if element.Component == "" && len(element.Props) > 0 {
		var typed app.ViewElement
		if err := json.Unmarshal(element.Props, &typed); err != nil {
			return nil, fmt.Errorf("%s typed element payload: %w", element.Kind, err)
		}
		if typed.Kind != element.Kind {
			return nil, fmt.Errorf("typed element kind %q does not match payload kind %q", element.Kind, typed.Kind)
		}
		bindTUIElementActions(frame, &typed)
		return []app.ViewElement{typed}, nil
	}
	switch element.Kind {
	case "prose", "heading", "code", "template":
		var source string
		if len(element.Value) > 0 {
			if err := json.Unmarshal(element.Value, &source); err != nil {
				return nil, fmt.Errorf("%s value must be a string: %w", element.Kind, err)
			}
		}
		return []app.ViewElement{{Kind: element.Kind, Source: source}}, nil
	case "list":
		var values []any
		if len(element.Value) > 0 {
			if err := json.Unmarshal(element.Value, &values); err != nil {
				return nil, fmt.Errorf("list value must be an array: %w", err)
			}
		}
		items := make([]app.ListItem, 0, len(values))
		for _, value := range values {
			items = append(items, app.ListItem{Label: fmt.Sprint(value)})
		}
		return []app.ViewElement{{Kind: "list", Items: items}}, nil
	case "component":
		fallback := componentFallback(frame, element.Component)
		if fallback == "" {
			return nil, fmt.Errorf("component %q has no TUI fallback", element.Component)
		}
		projected := element
		projected.Kind = fallback
		projected.Component = ""
		if len(projected.Value) == 0 {
			projected.Value = projected.Props
		}
		if fallback != "form" {
			projected.Props = nil
		}
		return projectTUIElement(frame, projected)
	case "status":
		return []app.ViewElement{{Kind: "prose", Source: displayTUIValue(element.Value)}}, nil
	case "table":
		return []app.ViewElement{{Kind: "code", Source: displayTUIValue(element.Value)}}, nil
	case "artifact":
		var artifact struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(element.Value, &artifact); err != nil || artifact.Handle == "" {
			if err := json.Unmarshal(element.Value, &artifact.Handle); err != nil || artifact.Handle == "" {
				return nil, fmt.Errorf("artifact value must contain a handle")
			}
		}
		return []app.ViewElement{{
			Kind: "media", MediaHandle: artifact.Handle, MediaCaption: artifact.Name,
		}}, nil
	case "form":
		var typed app.ViewElement
		if err := json.Unmarshal(element.Props, &typed); err != nil ||
			typed.Kind != "choice" ||
			typed.ChoiceMode != "form" {
			return nil, fmt.Errorf("form fallback requires typed form props")
		}
		bindTUIElementActions(frame, &typed)
		return []app.ViewElement{typed}, nil
	default:
		if len(element.Items) == 0 {
			return nil, fmt.Errorf("element kind %q has no TUI projection", element.Kind)
		}
		var projected []app.ViewElement
		for _, child := range element.Items {
			items, err := projectTUIElement(frame, child)
			if err != nil {
				return nil, err
			}
			projected = append(projected, items...)
		}
		return projected, nil
	}
}

func displayTUIValue(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	var normalized any
	if json.Unmarshal(value, &normalized) == nil {
		if raw, err := json.MarshalIndent(normalized, "", "  "); err == nil {
			return string(raw)
		}
	}
	return string(value)
}

func bindTUIElementActions(frame Frame, element *app.ViewElement) {
	if element == nil || element.Kind != "choice" {
		return
	}
	bind := func(id string) string {
		if id == "" || strings.HasPrefix(id, TUIActionIntentPrefix) {
			return id
		}
		if _, ok := frame.FindOfferedAction(id); ok {
			return TUIActionIntentPrefix + id
		}
		return id
	}
	element.ChoiceIntent = bind(element.ChoiceIntent)
	for i := range element.ChoiceItems {
		element.ChoiceItems[i].Intent = bind(element.ChoiceItems[i].Intent)
	}
}

func componentFallback(frame Frame, id string) string {
	for _, component := range frame.Components {
		if component.ID == id {
			return component.Fallback
		}
	}
	return ""
}

func nodeVisible(state NodeState) bool {
	return state.Visible == nil || *state.Visible
}

func tuiAction(frame Frame, action Action) TUIAction {
	return TUIAction{
		ID: action.ID, Intent: action.Intent,
		Name: action.Semantic.Name, Description: action.Semantic.Description,
		SemanticRef: action.Semantic.Ref, Enabled: action.Enabled,
		Envelope: ActionEnvelope{
			Action: action.ID, SessionID: frame.SessionID, FrameRevision: frame.Revision,
			RoutingMode: action.RoutingMode,
		},
	}
}
