package tui

import (
	"context"
	"strings"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationnative"
	"kitsoki/internal/orchestrator"
)

// ApplicationActionResult carries both the canonical service outcome and the
// settled story view. The receipt stays available to callers and tests while
// RootModel can reuse its established turn-rendering path.
type ApplicationActionResult struct {
	Outcome appplatform.OutcomeEnvelope
	View    *orchestrator.TurnOutcome
}

// ApplicationActionDispatcher is the transport adapter injected by the
// command composition root. Implementations must dispatch through the shared
// application service rather than invoking an intent or handler directly.
type ApplicationActionDispatcher func(context.Context, appplatform.ActionEnvelope) (ApplicationActionResult, error)

func projectApplicationTUI(
	def *app.AppDef,
	sessionID app.SessionID,
	revision uint64,
	state app.StatePath,
	allowed []string,
) (*applicationnative.TUIProjection, error) {
	frame, err := appplatform.CompileFrame(def, string(sessionID), revision, "", appplatform.Workflow{
		State: string(state), AllowedIntents: append([]string(nil), allowed...),
	})
	if err != nil {
		return nil, err
	}
	projection, err := applicationnative.ProjectTUI(def, frame)
	if err != nil {
		return nil, err
	}
	return &projection, nil
}

func (m *RootModel) captureApplicationChoiceFocus() {
	if m == nil || m.applicationProjection == nil || !m.choice.IsActive() {
		return
	}
	if m.applicationChoiceRef == "" {
		m.applicationChoiceRef = applicationChoiceSemanticRef(m.applicationProjection)
	}
	m.applicationFocusedRef = m.applicationChoiceRef
	if m.choice.mode != "single" || len(m.choice.items) == 0 {
		return
	}
	cursor := m.choice.cursor
	if cursor < 0 || cursor >= len(m.choice.items) {
		return
	}
	intent := m.choice.items[cursor].intent
	if !strings.HasPrefix(intent, appplatform.TUIActionIntentPrefix) {
		return
	}
	actionID := strings.TrimPrefix(intent, appplatform.TUIActionIntentPrefix)
	if action, ok := m.applicationProjection.Canonical.FindOfferedAction(actionID); ok {
		m.applicationFocusedRef = action.Semantic.Ref
	}
}

func applicationChoiceSemanticRef(projection *applicationnative.TUIProjection) string {
	if projection == nil {
		return ""
	}
	choiceIndex := -1
	for index, element := range projection.Frame.View.Elements {
		if element.Kind == "choice" {
			choiceIndex = index
			break
		}
	}
	if choiceIndex < 0 {
		return ""
	}
	bestRef := ""
	bestWidth := int(^uint(0) >> 1)
	for _, target := range projection.Frame.Targets {
		if choiceIndex < target.ElementStart || choiceIndex >= target.ElementEnd {
			continue
		}
		if width := target.ElementEnd - target.ElementStart; width < bestWidth {
			bestRef, bestWidth = target.Ref, width
		}
	}
	return bestRef
}

func (m RootModel) currentApplicationSemanticRef() string {
	if m.applicationProjection == nil {
		return ""
	}
	if ref := strings.TrimSpace(m.applicationFocusedRef); ref != "" {
		if _, ok, _ := m.applicationProjection.Canonical.Inspect(ref, 0); ok {
			return ref
		}
	}
	return m.applicationProjection.Canonical.PageSemantic.Ref
}
