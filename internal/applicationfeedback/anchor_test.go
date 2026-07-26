package applicationfeedback

import (
	"encoding/json"
	"strings"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/application"
)

func TestAnchorUsesCanonicalIdentityAndAllowlistedContext(t *testing.T) {
	frame := feedbackFrame()
	policy := &app.ApplicationFeedbackPolicy{Context: map[string]*app.ApplicationFeedbackContext{
		"workflow_state": {
			Source: "frame.workflow.state", Sensitivity: "internal", Policy: "include",
		},
		"degradation": {
			Source: "frame.workflow.degradation", Sensitivity: "sensitive", Policy: "redact",
		},
		"page_hash": {
			Source: "frame.page", Sensitivity: "internal", Policy: "hash",
		},
		"excluded": {
			Source: "frame.application_id", Sensitivity: "public", Policy: "exclude",
		},
	}}

	anchor, err := Anchor(frame, "demo.card.review", policy)
	if err != nil {
		t.Fatal(err)
	}
	target := anchor.SemanticElement
	if target == nil || target.Plugin != Plugin || target.Ref != "demo.card.review" ||
		target.SemanticKind != "card" || target.Label != "Review" {
		t.Fatalf("semantic target = %#v", target)
	}
	context, ok := target.Data["context"].(map[string]any)
	if !ok {
		t.Fatalf("context = %#v", target.Data["context"])
	}
	if _, exists := context["excluded"]; exists {
		t.Fatalf("excluded context leaked: %#v", context)
	}
	workflow := context["workflow_state"].(map[string]any)
	if workflow["value"] != "review.ready" || workflow["sensitivity"] != "internal" {
		t.Fatalf("workflow context = %#v", workflow)
	}
	degradation := context["degradation"].(map[string]any)
	if degradation["value"] != "[redacted]" {
		t.Fatalf("redacted context = %#v", degradation)
	}
	pageHash := context["page_hash"].(map[string]any)["value"].(string)
	if !strings.HasPrefix(pageHash, "sha256:") || strings.Contains(pageHash, frame.Page) {
		t.Fatalf("hashed context = %q", pageHash)
	}

	raw, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"secret-prop", "private-field-value", "/Users/operator/project", "api-token",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("anchor leaked %q: %s", secret, raw)
		}
	}
}

func TestAnchorRejectsUnknownRefsAndNonAllowlistedSources(t *testing.T) {
	frame := feedbackFrame()
	if _, err := Anchor(frame, "demo.missing", nil); err == nil {
		t.Fatal("expected missing semantic ref to fail")
	}
	_, err := Anchor(frame, "demo.card.review", &app.ApplicationFeedbackPolicy{
		Context: map[string]*app.ApplicationFeedbackContext{
			"world": {
				Source: "world.credentials", Sensitivity: "secret", Policy: "hash",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not an allowlisted frame field") {
		t.Fatalf("source error = %v", err)
	}
}

func TestBuildReportUsesReviewedFeedbackContractWithoutPrivateFrameData(t *testing.T) {
	frame := feedbackFrame()
	report, err := BuildReport(frame, &app.ApplicationFeedbackPolicy{
		Context: map[string]*app.ApplicationFeedbackContext{
			"state": {
				Source: "frame.workflow.state", Sensitivity: "internal", Policy: "include",
			},
		},
	}, ReportRequest{
		Ref: "demo.card.review", Instruction: "The review summary is unclear.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != ReportSchema || !report.Reviewed ||
		report.Producer != Plugin || report.IdempotencyKey == "" {
		t.Fatalf("report contract = %#v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		"secret-prop", "private-field-value", "/Users/operator/project", "api-token",
	} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("report leaked %q: %s", private, raw)
		}
	}
}

func feedbackFrame() application.Frame {
	semantic := func(kind application.SemanticKind, ref, name string) application.SemanticNode {
		return application.SemanticNode{
			Ref: ref, Kind: kind, Name: name, Description: name + " description",
			Source: application.Provenance{
				Story: "demo", Member: ref, ProgramNode: "program." + ref,
			},
		}
	}
	actionNode := semantic(application.SemanticAction, "demo.action.submit", "Submit")
	action := application.Action{
		ID: "demo.submit", Handler: "demo.submit", Enabled: true, Semantic: actionNode,
	}
	return application.Frame{
		Schema: application.FrameSchema, ApplicationID: "demo", SessionID: "session-1",
		Revision: 4, Page: "review", Workflow: application.Workflow{
			State: "review.ready", Degradation: "private-field-value",
		},
		Semantic:     semantic(application.SemanticApplication, "demo.application", "Demo"),
		PageSemantic: semantic(application.SemanticPage, "demo.page.review", "Review page"),
		Actions:      []application.Action{action},
		Regions: []application.Region{{
			ID: "main", Semantic: semantic(application.SemanticRegion, "demo.region.main", "Main"),
			Cards: []application.Card{{
				ID: "review", Semantic: semantic(application.SemanticCard, "demo.card.review", "Review"),
				Body: []application.Element{{
					Kind: "component", Component: "demo.form",
					Props: json.RawMessage(`{
						"token":"api-token",
						"path":"/Users/operator/project",
						"value":"secret-prop"
					}`),
				}},
				Actions: []application.Action{action},
			}},
		}},
	}
}
