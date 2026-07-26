// Package application defines the presentation-free application frame and the
// handler boundary shared by Kitsoki's interactive and headless surfaces.
package application

import (
	"encoding/json"
	"errors"
	"fmt"
)

const FrameSchema = "application-frame/v1"

type SemanticKind string

const (
	SemanticApplication SemanticKind = "application"
	SemanticNavigation  SemanticKind = "navigation"
	SemanticPage        SemanticKind = "page"
	SemanticRegion      SemanticKind = "region"
	SemanticCard        SemanticKind = "card"
	SemanticComponent   SemanticKind = "component"
	SemanticField       SemanticKind = "field"
	SemanticAction      SemanticKind = "action"
	SemanticStatus      SemanticKind = "status"
	SemanticArtifact    SemanticKind = "artifact"
	SemanticHandler     SemanticKind = "handler"
)

var semanticKinds = map[SemanticKind]struct{}{
	SemanticApplication: {},
	SemanticNavigation:  {},
	SemanticPage:        {},
	SemanticRegion:      {},
	SemanticCard:        {},
	SemanticComponent:   {},
	SemanticField:       {},
	SemanticAction:      {},
	SemanticStatus:      {},
	SemanticArtifact:    {},
	SemanticHandler:     {},
}

type Provenance struct {
	Story       string `json:"story"`
	Member      string `json:"member"`
	ProgramNode string `json:"program_node,omitempty"`
	Generated   bool   `json:"generated,omitempty"`
}

type Relationship struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type SemanticNode struct {
	Ref           string         `json:"ref"`
	Kind          SemanticKind   `json:"kind"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Source        Provenance     `json:"source"`
	Relationships []Relationship `json:"relationships,omitempty"`
}

type NodeState struct {
	Visible    *bool             `json:"visible,omitempty"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Selected   *bool             `json:"selected,omitempty"`
	Validation string            `json:"validation,omitempty"`
	Values     map[string]string `json:"values,omitempty"`
}

type Workflow struct {
	State          string   `json:"state"`
	AllowedIntents []string `json:"allowed_intents,omitempty"`
	BudgetState    string   `json:"budget_state,omitempty"`
	Degradation    string   `json:"degradation,omitempty"`
}

type NavigationItem struct {
	ID       string       `json:"id"`
	Page     string       `json:"page"`
	Semantic SemanticNode `json:"semantic"`
	State    NodeState    `json:"state,omitempty"`
}

type PageDescriptor struct {
	ID       string       `json:"id"`
	Semantic SemanticNode `json:"semantic"`
	Current  bool         `json:"current,omitempty"`
}

type ComponentDescriptor struct {
	ID       string       `json:"id"`
	Semantic SemanticNode `json:"semantic"`
	Fallback string       `json:"fallback,omitempty"`
}

type HandlerDescriptor struct {
	ID       string       `json:"id"`
	Semantic SemanticNode `json:"semantic"`
}

// Element is the finite, serializable content vocabulary carried by a frame.
// Props and Value remain JSON so a renderer can consume story-defined schemas
// without this package acquiring presentation or product dependencies.
type Element struct {
	ID        string          `json:"id,omitempty"`
	Kind      string          `json:"kind"`
	Component string          `json:"component,omitempty"`
	Props     json.RawMessage `json:"props,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Semantic  *SemanticNode   `json:"semantic,omitempty"`
	State     NodeState       `json:"state,omitempty"`
	Items     []Element       `json:"items,omitempty"`
	Actions   []Action        `json:"actions,omitempty"`
}

type Action struct {
	ID             string          `json:"id"`
	Handler        string          `json:"handler,omitempty"`
	Intent         string          `json:"intent,omitempty"`
	TargetState    string          `json:"target_state,omitempty"`
	RoomInterface  string          `json:"room_interface,omitempty"`
	RoutingMode    RoutingMode     `json:"routing_mode,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema,omitempty"`
	InputSchemaRef string          `json:"input_schema_ref,omitempty"`
	Enabled        bool            `json:"enabled"`
	Semantic       SemanticNode    `json:"semantic"`
	State          NodeState       `json:"state,omitempty"`
}

type Card struct {
	ID       string       `json:"id"`
	Semantic SemanticNode `json:"semantic"`
	Body     []Element    `json:"body,omitempty"`
	Actions  []Action     `json:"actions,omitempty"`
	State    NodeState    `json:"state,omitempty"`
}

type Region struct {
	ID       string       `json:"id"`
	Semantic SemanticNode `json:"semantic"`
	Cards    []Card       `json:"cards,omitempty"`
	State    NodeState    `json:"state,omitempty"`
}

type FrameError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	SemanticRef string `json:"semantic_ref,omitempty"`
}

type Capabilities struct {
	Presentation []string `json:"presentation,omitempty"`
	Actions      []string `json:"actions,omitempty"`
}

type Frame struct {
	Schema        string                `json:"schema"`
	ApplicationID string                `json:"application_id"`
	SessionID     string                `json:"session_id"`
	Revision      uint64                `json:"revision"`
	Page          string                `json:"page"`
	PageSemantic  SemanticNode          `json:"page_semantic"`
	Semantic      SemanticNode          `json:"semantic"`
	Workflow      Workflow              `json:"workflow"`
	Navigation    []NavigationItem      `json:"navigation,omitempty"`
	Pages         []PageDescriptor      `json:"pages,omitempty"`
	Components    []ComponentDescriptor `json:"components,omitempty"`
	Handlers      []HandlerDescriptor   `json:"handlers,omitempty"`
	Actions       []Action              `json:"actions,omitempty"`
	Regions       []Region              `json:"regions,omitempty"`
	Errors        []FrameError          `json:"errors,omitempty"`
	Capabilities  Capabilities          `json:"capabilities,omitempty"`
}

var (
	ErrStaleFrame      = errors.New("application: stale frame")
	ErrSessionMismatch = errors.New("application: action session does not match frame")
	ErrActionNotFound  = errors.New("application: action not found")
	ErrActionDisabled  = errors.New("application: action disabled")
)

type ActionEnvelope struct {
	Action         string          `json:"action"`
	Input          json.RawMessage `json:"input,omitempty"`
	SessionID      string          `json:"session_id"`
	FrameRevision  uint64          `json:"frame_revision"`
	Actor          string          `json:"actor,omitempty"`
	RoutingMode    RoutingMode     `json:"routing_mode,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type StaleFrameError struct {
	SessionID string
	Got       uint64
	Current   uint64
}

func (e *StaleFrameError) Error() string {
	return fmt.Sprintf("%v: session %q revision %d, current revision %d", ErrStaleFrame, e.SessionID, e.Got, e.Current)
}

func (e *StaleFrameError) Unwrap() error { return ErrStaleFrame }

func (f Frame) FindAction(id string) (Action, bool) {
	if action, ok := findAction(f.Actions, id); ok {
		return action, true
	}
	for _, region := range f.Regions {
		for _, card := range region.Cards {
			if action, ok := findAction(card.Actions, id); ok {
				return action, true
			}
			for _, element := range card.Body {
				if action, ok := findElementAction(element, id); ok {
					return action, true
				}
			}
		}
	}
	return Action{}, false
}

// FindOfferedAction resolves only actions exposed by visible content in the
// current frame. The top-level Actions collection is semantic/catalog metadata
// and must not make an action from another page invokable.
func (f Frame) FindOfferedAction(id string) (Action, bool) {
	for _, region := range f.Regions {
		if !nodeVisible(region.State) {
			continue
		}
		for _, card := range region.Cards {
			if !nodeVisible(card.State) {
				continue
			}
			if action, ok := findAction(card.Actions, id); ok {
				return action, true
			}
			for _, element := range card.Body {
				if action, ok := findVisibleElementAction(element, id); ok {
					return action, true
				}
			}
		}
	}
	return Action{}, false
}

func findVisibleElementAction(element Element, id string) (Action, bool) {
	if !nodeVisible(element.State) {
		return Action{}, false
	}
	if action, ok := findAction(element.Actions, id); ok {
		return action, true
	}
	for _, item := range element.Items {
		if action, ok := findVisibleElementAction(item, id); ok {
			return action, true
		}
	}
	return Action{}, false
}

func findElementAction(element Element, id string) (Action, bool) {
	if action, ok := findAction(element.Actions, id); ok {
		return action, true
	}
	for _, item := range element.Items {
		if action, ok := findElementAction(item, id); ok {
			return action, true
		}
	}
	return Action{}, false
}

func findAction(actions []Action, id string) (Action, bool) {
	for _, action := range actions {
		if action.ID == id {
			return action, true
		}
	}
	return Action{}, false
}

func ValidateActionEnvelope(frame Frame, envelope ActionEnvelope) (Action, error) {
	if envelope.SessionID != frame.SessionID {
		return Action{}, fmt.Errorf("%w: got %q, want %q", ErrSessionMismatch, envelope.SessionID, frame.SessionID)
	}
	if envelope.FrameRevision != frame.Revision {
		return Action{}, &StaleFrameError{SessionID: frame.SessionID, Got: envelope.FrameRevision, Current: frame.Revision}
	}
	action, ok := frame.FindOfferedAction(envelope.Action)
	if !ok {
		return Action{}, fmt.Errorf("%w: %q", ErrActionNotFound, envelope.Action)
	}
	if !action.Enabled || (action.State.Enabled != nil && !*action.State.Enabled) {
		return Action{}, fmt.Errorf("%w: %q", ErrActionDisabled, envelope.Action)
	}
	return action, nil
}
