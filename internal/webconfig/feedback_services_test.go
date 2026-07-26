package webconfig

import (
	"strings"
	"testing"
)

func TestLoadFeedbackServiceBindingsCarryOnlyOpaqueAuthority(t *testing.T) {
	cfg, err := loadConfigText(t, `feedback_intake:
  source.app:
    source: application-feedback
    max_records: 25
feedback_federation:
  source.app:
    target_application: target.app
    target_handler: target.feedback.apply
    target_action: target.feedback.apply.action
    max_records: 10
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.FeedbackIntake["source.app"]; got.Source != "application-feedback" ||
		got.MaxRecords != 25 {
		t.Fatalf("intake binding = %#v", got)
	}
	if got := cfg.FeedbackFederation["source.app"]; got.TargetApplication != "target.app" ||
		got.TargetHandler != "target.feedback.apply" ||
		got.TargetAction != "target.feedback.apply.action" ||
		got.MaxRecords != 10 {
		t.Fatalf("federation binding = %#v", got)
	}
}

func TestLoadFeedbackServiceBindingsRejectPathCommandAndUnboundedValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "intake path",
			yaml: "feedback_intake:\n  source.app:\n    source: ../feedback.jsonl\n",
			want: "source",
		},
		{
			name: "intake unbounded",
			yaml: "feedback_intake:\n  source.app:\n    source: application-feedback\n    max_records: 201\n",
			want: "max_records",
		},
		{
			name: "federation command",
			yaml: `feedback_federation:
  source.app:
    target_application: target.app
    target_handler: "target.apply; shell"
    target_action: target.apply.action
`,
			want: "target_handler",
		},
		{
			name: "federation path",
			yaml: `feedback_federation:
  source.app:
    target_application: ../sibling
    target_handler: target.apply
    target_action: target.apply.action
`,
			want: "target_application",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadConfigText(t, test.yaml)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
