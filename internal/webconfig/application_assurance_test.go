package webconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validApplicationAssuranceConfig = `
story_application_assurance:
  pog-application:
    catalog: pog/catalog.yaml
    compliance:
      max_checks: 32
      max_resolved_bytes: 65536
      max_evidence_bytes: 131072
    flow_evidence:
      suites:
        - id: pog-application
          app: stories/pog-application/app.yaml
          flows: stories/pog-application/flows/*.yaml
          version: v1
      max_suites: 4
      max_runs: 100
      max_suite_bytes: 1048576
      max_evidence_bytes: 524288
`

func TestStoryApplicationAssuranceStrictConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
	if err := os.WriteFile(path, []byte(validApplicationAssuranceConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	binding := cfg.StoryApplicationAssurance["pog-application"]
	if binding.Catalog != "pog/catalog.yaml" ||
		binding.Compliance.MaxChecks != 32 ||
		binding.FlowEvidence.Suites[0].ID != "pog-application" {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestStoryApplicationAssuranceRejectsUnknownAuthorityAndBounds(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown authority",
			body: strings.Replace(
				validApplicationAssuranceConfig,
				"    catalog: pog/catalog.yaml",
				"    catalog: pog/catalog.yaml\n    command: make test",
				1,
			),
			want: `unknown field "command"`,
		},
		{
			name: "nested suite provider",
			body: strings.Replace(
				validApplicationAssuranceConfig,
				"          version: v1",
				"          version: v1\n          provider: local",
				1,
			),
			want: `unknown field "provider"`,
		},
		{
			name: "absolute catalog",
			body: strings.Replace(
				validApplicationAssuranceConfig,
				"catalog: pog/catalog.yaml",
				"catalog: /tmp/catalog.yaml",
				1,
			),
			want: "repository-relative",
		},
		{
			name: "missing explicit bound",
			body: strings.Replace(
				validApplicationAssuranceConfig,
				"      max_runs: 100",
				"      max_runs: 0",
				1,
			),
			want: "max_runs must be between",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}
