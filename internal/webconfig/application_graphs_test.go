package webconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadApplicationGraphs(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
	if err := os.WriteFile(path, []byte(`
application_graphs:
  pog:
    project_root: .
    catalog: pog/catalog.yaml
    overlay: pog/overlay.yaml
    max_nodes: 1200
    max_bytes: 524288
    write_policy: propose
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ApplicationGraphs["pog"]
	if got.ProjectRoot != "." || got.Catalog != "pog/catalog.yaml" ||
		got.Overlay != "pog/overlay.yaml" || got.MaxNodes != 1200 ||
		got.MaxBytes != 524288 || got.WritePolicy != "propose" {
		t.Fatalf("binding = %#v", got)
	}
}

func TestLoadApplicationGraphsRejectsAuthorityAndUnboundedConfig(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"absolute root", "project_root: /tmp", "repository-relative"},
		{"traversal", "catalog: ../catalog.yaml", "repository-relative"},
		{"URL", "catalog: https://example.test/catalog", "repository-relative"},
		{"unbounded nodes", "max_nodes: 0", "max_nodes"},
		{"unbounded bytes", "max_bytes: 0", "max_bytes"},
		{"write policy", "write_policy: direct", "write_policy"},
		{"unknown command", "command: graph serve", "unknown field"},
		{"unknown provider", "provider: pog", "unknown field"},
	}
	const valid = `project_root: .
    catalog: pog/catalog.yaml
    max_nodes: 100
    max_bytes: 65536
    write_policy: read`
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			body := valid
			key := strings.SplitN(test.body, ":", 2)[0] + ":"
			if strings.Contains(body, key) {
				for _, line := range strings.Split(body, "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), key) {
						body = strings.Replace(body, strings.TrimSpace(line), test.body, 1)
						break
					}
				}
			} else {
				body += "\n    " + test.body
			}
			path := filepath.Join(t.TempDir(), ".kitsoki.yaml")
			raw := "application_graphs:\n  pog:\n    " + body + "\n"
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
