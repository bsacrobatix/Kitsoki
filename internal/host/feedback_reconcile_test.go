package host

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestFeedbackReconcileBuiltinsAreCommandFreeUnavailableAndClassified(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	tests := []struct {
		verb  string
		class effect.Effect
	}{
		{ReviewedFeedbackCampaignReconcileVerb, effect.External},
		{FeedbackIntakeReconcileVerb, effect.Write},
		{FeedbackFederationReconcileVerb, effect.External},
	}
	for _, test := range tests {
		t.Run(test.verb, func(t *testing.T) {
			result, err := registry.Invoke(context.Background(), test.verb, nil)
			if err != nil || !strings.Contains(result.Error, "unavailable") {
				t.Fatalf("unavailable result = %#v, %v", result, err)
			}
			class, deterministic := ClassifyDispatchedCall(test.verb, nil)
			if class != test.class || deterministic {
				t.Fatalf("classification = (%q, %v)", class, deterministic)
			}
		})
	}
}

func TestFeedbackReconcileHandlerRejectsEveryCallerArgument(t *testing.T) {
	called := false
	handler := NewFeedbackReconcileHandler(
		FeedbackIntakeReconcileVerb,
		func(context.Context) (Result, error) {
			called = true
			return Result{Data: map[string]any{"status": "empty"}}, nil
		},
	)
	for _, args := range []map[string]any{
		{"path": "/tmp/feedback"},
		{"url": "https://example.invalid"},
		{"command": "run"},
		{"provider": "live"},
		{"credential": "secret"},
		{"application_id": "other"},
		{"actor": "forged"},
		{"session": "forged"},
		{"transport": "http"},
	} {
		if _, err := handler(context.Background(), args); err == nil ||
			!strings.Contains(err.Error(), "accepts no caller authority") {
			t.Fatalf("args %#v error = %v", args, err)
		}
	}
	if called {
		t.Fatal("reconciler ran despite caller authority")
	}
}

func TestFeedbackReconcileOpschemasHaveNoInputs(t *testing.T) {
	schemas := opschema.Builtins()
	for _, entry := range []struct {
		namespace string
		op        string
		receipt   string
	}{
		{"host.reviewed_feedback_campaign", "reconcile", "receipts"},
		{"host.feedback_intake", "reconcile", "intake_receipt"},
		{"host.feedback_federation", "reconcile", "receipts"},
	} {
		spec, ok := schemas.Lookup(entry.namespace, entry.op)
		if !ok || len(spec.Input) != 0 ||
			spec.Output["status"].Type != "string" ||
			spec.Output[entry.receipt].Type == "" {
			t.Fatalf("%s.%s opschema = %#v, %v", entry.namespace, entry.op, spec, ok)
		}
	}
}
