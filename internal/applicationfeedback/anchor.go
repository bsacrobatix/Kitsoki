// Package applicationfeedback projects canonical application semantics into
// the existing annotation anchor contract without exposing arbitrary frame data.
package applicationfeedback

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"kitsoki/internal/app"
	"kitsoki/internal/application"
	"kitsoki/internal/host"
)

const Plugin = "kitsoki.application"

// Anchor resolves ref through the canonical frame and evaluates only the finite
// feedback sources supported by the application contract. Component props,
// element values, world snapshots, credentials, and host paths are never read.
func Anchor(
	frame application.Frame,
	ref string,
	policy *app.ApplicationFeedbackPolicy,
) (host.AnnotationAnchor, error) {
	inspection, ok, err := frame.Inspect(ref, 0)
	if err != nil {
		return host.AnnotationAnchor{}, err
	}
	if !ok {
		return host.AnnotationAnchor{}, fmt.Errorf("application feedback: unknown semantic ref %q", ref)
	}

	node := inspection.Node
	data := map[string]any{
		"application_id": frame.ApplicationID,
		"frame_revision": frame.Revision,
		"story":          node.Source.Story,
		"member":         node.Source.Member,
	}
	if node.Source.ProgramNode != "" {
		data["program_node"] = node.Source.ProgramNode
	}
	if context, err := feedbackContext(frame, policy); err != nil {
		return host.AnnotationAnchor{}, err
	} else if len(context) > 0 {
		data["context"] = context
	}

	return host.AnnotationAnchor{
		Kind: host.AnchorSemanticElement,
		SemanticElement: &host.AnchorSemanticElementTarget{
			Plugin:       Plugin,
			Ref:          node.Ref,
			SemanticKind: string(node.Kind),
			Label:        node.Name,
			Description:  node.Description,
			Data:         data,
		},
	}, nil
}

func feedbackContext(
	frame application.Frame,
	policy *app.ApplicationFeedbackPolicy,
) (map[string]any, error) {
	if policy == nil {
		return nil, nil
	}
	context := make(map[string]any)
	for name, declaration := range policy.Context {
		if declaration == nil {
			return nil, fmt.Errorf("application feedback: context %q has no declaration", name)
		}
		if declaration.Policy == "exclude" {
			continue
		}
		value, err := feedbackSource(frame, declaration.Source)
		if err != nil {
			return nil, fmt.Errorf("application feedback: context %q: %w", name, err)
		}
		switch declaration.Policy {
		case "include":
			if declaration.Sensitivity == "sensitive" || declaration.Sensitivity == "secret" {
				return nil, fmt.Errorf(
					"application feedback: context %q cannot include %s data",
					name, declaration.Sensitivity,
				)
			}
			context[name] = map[string]any{
				"value": value, "sensitivity": declaration.Sensitivity,
			}
		case "redact":
			context[name] = map[string]any{
				"value": "[redacted]", "sensitivity": declaration.Sensitivity,
			}
		case "hash":
			sum := sha256.Sum256([]byte(value))
			context[name] = map[string]any{
				"value":       "sha256:" + hex.EncodeToString(sum[:]),
				"sensitivity": declaration.Sensitivity,
			}
		default:
			return nil, fmt.Errorf("application feedback: context %q has invalid policy %q", name, declaration.Policy)
		}
	}
	return context, nil
}

func feedbackSource(frame application.Frame, source string) (string, error) {
	switch source {
	case "frame.application_id":
		return frame.ApplicationID, nil
	case "frame.page":
		return frame.Page, nil
	case "frame.workflow.state":
		return frame.Workflow.State, nil
	case "frame.workflow.budget_state":
		return frame.Workflow.BudgetState, nil
	case "frame.workflow.degradation":
		return frame.Workflow.Degradation, nil
	default:
		return "", fmt.Errorf("source %q is not an allowlisted frame field", source)
	}
}
