package webconfig

import (
	"strings"
	"testing"
)

func TestLoadReviewedFeedbackBindings(t *testing.T) {
	cfg, err := loadConfigText(t, `reviewed_feedback:
  source-app:
    target_application: target-app
    target_handler: target.feedback.handle
    target_action: target.feedback.dispatch
`)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cfg.ReviewedFeedback["source-app"]
	if !ok || got.TargetApplication != "target-app" ||
		got.TargetHandler != "target.feedback.handle" ||
		got.TargetAction != "target.feedback.dispatch" {
		t.Fatalf("reviewed feedback binding = %#v", cfg.ReviewedFeedback)
	}
}

func TestLoadReviewedFeedbackRejectsMissingOrPathShapedAuthority(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing target",
			yaml: `reviewed_feedback:
  source-app:
    target_handler: target.handle
    target_action: target.dispatch
`,
			want: "target_application",
		},
		{
			name: "path target",
			yaml: `reviewed_feedback:
  source-app:
    target_application: ../other/repo
    target_handler: target.handle
    target_action: target.dispatch
`,
			want: "target_application",
		},
		{
			name: "command handler",
			yaml: `reviewed_feedback:
  source-app:
    target_application: target-app
    target_handler: "target.handle; bash"
    target_action: target.dispatch
`,
			want: "target_handler",
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
