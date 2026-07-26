package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/kitgit"
	"kitsoki/internal/kitlock"
)

func TestLoadApplicationPackageThroughKitLockLifecycle(t *testing.T) {
	project := t.TempDir()
	storyDir := filepath.Join(project, "stories")
	if err := os.MkdirAll(storyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	storyPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(storyPath, []byte(`
app: {id: package-consumer, version: 0.1.0}
hosts: [host.run]
application:
  schema: application/v1
  name: Package consumer
  description: Exercises selected package members.
  semantic_ref: package-consumer.application
  shell: {entry: home}
  packages:
    - package: kitsoki-test.synthetic
      select:
        - components.panel
        - schemas.panel-input
        - tokens.base
        - room_templates.review
        - intents.approve
        - agents.reviewer
        - toolboxes.review
        - providers.local
        - host_interfaces.catalog
  pages:
    home:
      name: Home
      description: Package-backed home page.
      semantic_ref: package-consumer.page.home
      regions:
        main:
          name: Main
          description: Main package content.
          semantic_ref: package-consumer.region.main
          items:
            - card:
                id: panel
                name: Panel
                description: Selected package panel.
                semantic_ref: package-consumer.card.panel
                component: kitsoki-test.synthetic.panel
  surfaces:
    web: {presentation: custom}
    tui: {presentation: default}
root: idle
states:
  idle: {terminal: true}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureDir, err := filepath.Abs(filepath.Join("testdata", "kits", "synthetic-kit", "stories", "greeter"))
	if err != nil {
		t.Fatal(err)
	}
	treeHash, err := kitgit.DirTreeHash(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	lock := kitlock.New()
	lock.Kits["kitsoki-test.synthetic"] = &kitlock.Entry{
		Source: "@kitsoki/synthetic", Version: "0.1.0",
		TreeHash: treeHash, Constraint: "^0.1.0",
	}
	if err := kitlock.Save(kitlock.Path(project), lock); err != nil {
		t.Fatal(err)
	}
	resolver := func(name, _ string, _ bool) (string, error) {
		if name == "synthetic" {
			return filepath.Join(fixtureDir, "app.yaml"), nil
		}
		return "", nil
	}
	def, err := LoadWithResolver(storyPath, nil, resolver)
	if err != nil {
		t.Fatalf("LoadWithResolver: %v", err)
	}

	const owner = "kitsoki-test.synthetic"
	if def.Application.Components[owner+".panel"] == nil ||
		def.Application.Schemas[owner+".panel-input"] == "" ||
		def.Application.Tokens[owner+".base"] == "" {
		t.Fatalf("application package resources were not selected: %#v", def.Application)
	}
	if def.PhaseTemplates[owner+".review"] == nil ||
		def.Intents[owner+".approve"].Description == "" ||
		def.Toolboxes[owner+".review"] == nil ||
		def.Providers[owner+".local"] == nil ||
		def.HostInterfaces[owner+".catalog"] == nil {
		t.Fatalf("story package resources were not selected")
	}
	agent := def.Agents[owner+".reviewer"]
	if agent == nil || agent.Toolbox != owner+".review" || agent.Provider != owner+".local" {
		t.Fatalf("selected agent cross references = %#v", agent)
	}
	if _, importedRoomGraph := def.States["ready"]; importedRoomGraph {
		t.Fatal("component package selection imported a room graph")
	}
	if !filepath.IsAbs(def.Application.Components[owner+".panel"].Web.Module) {
		t.Fatalf("component module path was not rebased: %q", def.Application.Components[owner+".panel"].Web.Module)
	}
	if len(def.ApplicationPackageRoots) != 1 || def.ApplicationPackageRoots[0] != fixtureDir {
		t.Fatalf("verified package roots = %#v", def.ApplicationPackageRoots)
	}
}

func TestApplicationPackageRequiresFileBackedLockVerification(t *testing.T) {
	_, err := LoadBytes([]byte(`
app: {id: bytes-app, version: 0.1.0}
application:
  schema: application/v1
  name: Bytes
  description: Bytes-only package consumer.
  semantic_ref: bytes-app.application
  packages:
    - package: kitsoki-test.synthetic
      select: [components.panel]
root: idle
states:
  idle: {terminal: true}
`))
	if err == nil || !strings.Contains(err.Error(), "require file-backed Load") {
		t.Fatalf("LoadBytes error = %v", err)
	}
}
