package application

import (
	"encoding/json"
	"fmt"

	"kitsoki/internal/app"
)

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
			cardElements := []app.ViewElement{
				{Kind: "heading", Source: card.Semantic.Name},
				{Kind: "prose", Source: card.Semantic.Description},
			}
			for _, element := range card.Body {
				projected, err := projectTUIElement(frame, element)
				if err != nil {
					return TUIProjection{}, fmt.Errorf("application: project card %q: %w", card.ID, err)
				}
				cardElements = append(cardElements, projected...)
			}
			add(card.Semantic, cardElements...)
			for _, action := range card.Actions {
				projection.Actions = append(projection.Actions, tuiAction(frame, action))
			}
		}
	}
	return projection, nil
}

func projectTUIElement(frame Frame, element Element) ([]app.ViewElement, error) {
	if !nodeVisible(element.State) {
		return nil, nil
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
		return []app.ViewElement{{
			Kind:   "prose",
			Source: fmt.Sprintf("%s (%s projection)", element.Component, fallback),
		}}, nil
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
		ID: action.ID, Name: action.Semantic.Name, Description: action.Semantic.Description,
		SemanticRef: action.Semantic.Ref, Enabled: action.Enabled,
		Envelope: ActionEnvelope{
			Action: action.ID, SessionID: frame.SessionID, FrameRevision: frame.Revision,
			RoutingMode: action.RoutingMode,
		},
	}
}
