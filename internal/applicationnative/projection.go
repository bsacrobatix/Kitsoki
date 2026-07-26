// Package applicationnative projects the canonical application frame onto
// editor- and terminal-native declarations without acquiring business logic.
package applicationnative

import (
	"fmt"

	"kitsoki/internal/app"
	"kitsoki/internal/application"
	"kitsoki/internal/applicationfeedback"
)

type VSCodeProjection struct {
	Reuse    string          `json:"reuse,omitempty"`
	Commands []VSCodeCommand `json:"commands,omitempty"`
}

type VSCodeCommand struct {
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	SemanticRef string                     `json:"semantic_ref"`
	Envelope    application.ActionEnvelope `json:"envelope"`
}

type TUIProjection struct {
	Projection string                    `json:"projection,omitempty"`
	Frame      application.TUIProjection `json:"frame"`
	Canonical  application.Frame         `json:"-"`
}

func ProjectVSCode(def *app.AppDef, frame application.Frame) (VSCodeProjection, error) {
	if def == nil || def.Application == nil {
		return VSCodeProjection{}, fmt.Errorf("application native: application contract is required")
	}
	if err := frame.Validate(); err != nil {
		return VSCodeProjection{}, err
	}
	surface := def.Application.Surfaces["vscode"]
	if surface == nil {
		return VSCodeProjection{}, nil
	}
	projection := VSCodeProjection{Reuse: surface.Reuse}
	if surface.Native == nil {
		return projection, nil
	}
	actions := make(map[string]application.Action, len(frame.Actions))
	for _, action := range frame.Actions {
		actions[action.ID] = action
	}
	for _, id := range surface.Native.Commands {
		action, ok := actions[id]
		if !ok {
			return VSCodeProjection{}, fmt.Errorf("application native: VS Code command %q has no frame action", id)
		}
		projection.Commands = append(projection.Commands, VSCodeCommand{
			ID: id, Name: action.Semantic.Name, Description: action.Semantic.Description,
			SemanticRef: action.Semantic.Ref,
			Envelope: application.ActionEnvelope{
				Action: action.ID, SessionID: frame.SessionID, FrameRevision: frame.Revision,
				RoutingMode: action.RoutingMode,
			},
		})
	}
	return projection, nil
}

func ProjectTUI(def *app.AppDef, frame application.Frame) (TUIProjection, error) {
	if def == nil || def.Application == nil {
		return TUIProjection{}, fmt.Errorf("application native: application contract is required")
	}
	projected, err := application.ProjectTUI(frame)
	if err != nil {
		return TUIProjection{}, err
	}
	var projection string
	if surface := def.Application.Surfaces["tui"]; surface != nil {
		projection = surface.Projection
	}
	return TUIProjection{Projection: projection, Frame: projected, Canonical: frame}, nil
}

// ProjectFeedback is the native-surface attachment boundary shared by the TUI
// and editor-native contributions. It cannot inspect renderer values.
func ProjectFeedback(
	def *app.AppDef,
	frame application.Frame,
	request applicationfeedback.ReportRequest,
) (applicationfeedback.Report, error) {
	if def == nil || def.Application == nil {
		return applicationfeedback.Report{}, fmt.Errorf("application native: application contract is required")
	}
	return applicationfeedback.BuildReport(frame, def.Application.Feedback, request)
}
