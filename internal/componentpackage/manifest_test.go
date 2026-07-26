package componentpackage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/kitlock"
)

func TestLoadDirAndVerifyLock(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ui/card.js"), "export default {}")
	write(t, filepath.Join(dir, "schemas/card.json"), "{}")
	write(t, filepath.Join(dir, "tokens/base.json"), "{}")
	write(t, filepath.Join(dir, FileName), `
schema: application-component-package/v1
namespace: kitsoki
name: wizard-ui
version: 1.2.3
components:
  card:
    name: Wizard card
    description: Present one guided step.
    semantic_ref: kitsoki.wizard-ui.component.card
    props_schema: schemas/card.json
    web: {module: ui/card.js, export: default}
    fallback: {element: prose}
schemas:
  card-input: schemas/card.json
room_templates:
  review:
    states:
      ready: {terminal: true}
intents:
  approve:
    title: Approve
host_interfaces:
  catalog:
    operations:
      read: {}
tokens: {base: tokens/base.json}
dependencies:
  - {package: kitsoki.core-ui, constraint: "^1.0.0"}
`)
	manifest, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if manifest.Identity() != "kitsoki.wizard-ui" {
		t.Fatalf("identity = %q", manifest.Identity())
	}
	lock := kitlock.New()
	lock.Kits[manifest.Identity()] = &kitlock.Entry{
		Source: "@kitsoki/wizard-ui", Version: "1.2.3",
		TreeHash: "abc123", Constraint: "^1.0.0",
	}
	lock.Kits["kitsoki.core-ui"] = &kitlock.Entry{
		Source: "@kitsoki/core-ui", Version: "1.4.0", TreeHash: "def456",
	}
	if err := manifest.VerifyLock(lock); err != nil {
		t.Fatalf("VerifyLock: %v", err)
	}
	selected, err := manifest.Select([]string{"components.card", "schemas.card-input", "tokens.base", "room_templates.review", "intents.approve", "host_interfaces.catalog"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selected.Components["kitsoki.wizard-ui.card"] == nil ||
		selected.RoomTemplates["kitsoki.wizard-ui.review"] == nil ||
		selected.HostInterfaces["kitsoki.wizard-ui.catalog"] == nil {
		t.Fatalf("selection = %#v", selected)
	}
}

func TestSelectSupportsAllPackageMemberKindsWithoutRoomGraph(t *testing.T) {
	manifest := &Manifest{
		Namespace: "kitsoki", Name: "delivery",
		Agents:    map[string]any{"reviewer": map[string]any{"system_prompt": "Review."}},
		Toolboxes: map[string]any{"review": map[string]any{"tools": []any{"Read"}}},
		Providers: map[string]any{"local": map[string]any{"model": "test"}},
	}
	selected, err := manifest.Select([]string{"agents.reviewer", "toolboxes.review", "providers.local"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selected.Agents["kitsoki.delivery.reviewer"] == nil ||
		selected.Toolboxes["kitsoki.delivery.review"] == nil ||
		selected.Providers["kitsoki.delivery.local"] == nil {
		t.Fatalf("selection = %#v", selected)
	}
}

func TestManifestRejectsEscapesAndUnpinnedPackage(t *testing.T) {
	manifest := &Manifest{
		Schema: SchemaV1, Namespace: "kitsoki", Name: "wizard-ui", Version: "1.0.0",
		Components: map[string]*Component{
			"card": {
				SemanticRef: "other.component.card",
				Web:         &WebComponent{Module: "../card.js"},
			},
		},
	}
	err := manifest.Validate(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "path escapes package root") ||
		!strings.Contains(err.Error(), "semantic_ref must be") {
		t.Fatalf("Validate error = %v", err)
	}
	if err := manifest.VerifyLock(kitlock.New()); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("VerifyLock error = %v", err)
	}
}

func TestManifestRejectsFileSymlinkOutsidePackageRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.js")
	write(t, outside, "export default {}")
	if err := os.Symlink(outside, filepath.Join(root, "panel.js")); err != nil {
		t.Fatal(err)
	}
	manifest := &Manifest{
		Schema: SchemaV1, Namespace: "kitsoki", Name: "unsafe", Version: "1.0.0",
		Components: map[string]*Component{
			"panel": {
				SemanticRef: "kitsoki.unsafe.component.panel",
				Web:         &WebComponent{Module: "panel.js"},
			},
		},
	}
	err := manifest.Validate(root)
	if err == nil || !strings.Contains(err.Error(), "symlink escapes package root") {
		t.Fatalf("Validate error = %v", err)
	}
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
