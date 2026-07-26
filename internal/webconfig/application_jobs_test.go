package webconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStoryApplicationJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
	if err := os.WriteFile(path, []byte(validApplicationJobConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	template := cfg.StoryApplicationJobs["caller"]["publish"]
	if template.ApplicationID != "producer" || template.Event != "producer.publish" ||
		template.PrimaryOutput != "report_ref" ||
		template.Bounds.MaxInputBytes != 4096 ||
		template.Bounds.MaxRuntimeSeconds != 60 {
		t.Fatalf("template = %#v", template)
	}
}

func TestLoadStoryApplicationJobsRejectsAuthorityAndMissingBounds(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
		want string
	}{
		{
			name: "application path",
			edit: func(raw string) string {
				return strings.Replace(raw, "application_id: producer", "application_id: ../producer", 1)
			},
			want: "application_id is empty, malformed, or unbounded",
		},
		{
			name: "event URL",
			edit: func(raw string) string {
				return strings.Replace(raw, "event: producer.publish", "event: https://example.test/event", 1)
			},
			want: "event is empty, malformed, or unbounded",
		},
		{
			name: "primary not output",
			edit: func(raw string) string {
				return strings.Replace(raw, "primary_output: report_ref", "primary_output: other_ref", 1)
			},
			want: "primary_output must name an artifact output",
		},
		{
			name: "missing input bound",
			edit: func(raw string) string {
				return strings.Replace(raw, "max_input_bytes: 4096", "max_input_bytes: 0", 1)
			},
			want: "max_input_bytes must be within",
		},
		{
			name: "missing runtime bound",
			edit: func(raw string) string {
				return strings.Replace(raw, "max_runtime_seconds: 60", "max_runtime_seconds: 0", 1)
			},
			want: "max_runtime_seconds must be within",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
			if err := os.WriteFile(path, []byte(tc.edit(validApplicationJobConfig)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}

const validApplicationJobConfig = `
story_application_jobs:
  caller:
    publish:
      application_id: producer
      event: producer.publish
      artifact_outputs: [report_ref]
      primary_output: report_ref
      bounds:
        max_input_bytes: 4096
        max_runtime_seconds: 60
`
