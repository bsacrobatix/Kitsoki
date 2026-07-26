package webconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStoryApplicationArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
	if err := os.WriteFile(path, []byte(validArtifactConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	binding := cfg.StoryApplicationArtifacts["caller"]
	if binding.Catalog != "catalog/product.yaml" ||
		binding.CatalogRef != "product" ||
		binding.CreateMockup == nil ||
		binding.CreateMockup.ApplicationID != "producer" ||
		len(binding.Materialize) != 3 {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestLoadStoryApplicationArtifactsRejectsMissingAndAuthorityFields(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
		want string
	}{
		{
			name: "missing phase",
			edit: func(raw string) string {
				return strings.ReplaceAll(raw, "      verify: *materialize\n", "")
			},
			want: "dependencies, subject, and verify",
		},
		{
			name: "catalog URL",
			edit: func(raw string) string {
				return strings.Replace(raw, "catalog/product.yaml", "https://example.test/catalog", 1)
			},
			want: "repository-relative",
		},
		{
			name: "handler path",
			edit: func(raw string) string {
				return strings.Replace(raw, "artifact.produce", "../scripts/run.sh", 1)
			},
			want: "handler is empty, malformed, or unbounded",
		},
		{
			name: "missing bundle",
			edit: func(raw string) string {
				return strings.Replace(raw, "      bundle: true", "      bundle: false", 1)
			},
			want: "bundle: true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
			if err := os.WriteFile(path, []byte(tc.edit(validArtifactConfig)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

const validArtifactConfig = `
story_application_artifacts:
  caller:
    catalog: catalog/product.yaml
    catalog_ref: product
    create_mockup:
      application_id: producer
      bundle: true
      primary_output: mockup_ref
      phases:
        - id: produce
          handler: artifact.produce
          artifact_outputs: [mockup_ref]
    materialize:
      dependencies: &materialize
        application_id: producer
        phases:
          - id: materialize
            action: artifact.materialize
            artifact_outputs: [artifact_ref]
      subject: *materialize
      verify: *materialize
`
