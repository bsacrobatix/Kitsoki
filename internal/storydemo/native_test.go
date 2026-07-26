package storydemo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePlatformCreateFailsClosedWithoutTypedArtifactExecutor(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, ".artifacts", "mockup")
	scenario := json.RawMessage(`{"title":"Native mockup","tagline":"No subprocesses","states":{"start":{},"done":{}}}`)
	platform := NativePlatform{}
	_, err := platform.Create(context.Background(), root, MockupManifest{
		Scenario: scenario, WorkDir: workDir, OutPath: filepath.Join(workDir, "mockup.html"),
	})
	if err == nil || !strings.Contains(err.Error(), "typed artifact executor") {
		t.Fatalf("create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "mockup.html")); !os.IsNotExist(statErr) {
		t.Fatalf("standalone mockup was emitted: %v", statErr)
	}
}

func TestNativePlatformRejectsEscapingAndInvalidRRWebArtifacts(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "capture.rrweb.json")
	if err := os.WriteFile(outside, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(workDir, "escape.demo.json")
	raw, _ := json.Marshal(nativeManifest{
		Schema:    nativeManifestSchema,
		Artifacts: []nativeArtifact{{Kind: "rrweb", Path: filepath.Join("..", "..", filepath.Base(outsideDir), filepath.Base(outside))}},
	})
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if checked, err := (NativePlatform{}).Check(context.Background(), root, Manifest{Path: manifestPath}); err != nil || checked.OK {
		t.Fatalf("expected escaping artifact to fail closed: %#v, %v", checked, err)
	}

	invalid := filepath.Join(workDir, "capture.rrweb.json")
	if err := os.WriteFile(invalid, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(nativeManifest{
		Schema:    nativeManifestSchema,
		Artifacts: []nativeArtifact{{Kind: "rrweb", Path: filepath.Base(invalid)}},
	})
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if checked, err := (NativePlatform{}).Check(context.Background(), root, Manifest{Path: manifestPath}); err != nil || checked.OK {
		t.Fatalf("expected invalid rrweb to fail closed: %#v, %v", checked, err)
	}
}

func TestNativePlatformProjectsLegacyManifestWithoutExecutingLaunchFields(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".context")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"mockup.html":        `<html></html>`,
		"deck.slidey.json":   `{}`,
		"capture.rrweb.json": `[]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifestPath := filepath.Join(dir, "legacy.demo.json")
	manifest := `{
	  "version": 1,
	  "mockup": "mockup.html",
	  "deck": "deck.slidey.json",
	  "target": {"launch": "rm -rf /", "addr": "127.0.0.1:1"},
	  "tours": [{"out": "capture.rrweb.json"}]
	}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	checked, err := (NativePlatform{}).Check(context.Background(), root, Manifest{Path: manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	if !checked.OK || checked.Report["artifact_count"] != 3 {
		t.Fatalf("legacy doctor = %#v", checked)
	}
}

func TestTypedProviderProductionFilesContainNoProcessExecution(t *testing.T) {
	for _, name := range []string{"provider.go", "resolver.go", "native.go", "capture.go", "store.go", "types.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"exec.Command", `"bash"`, `"node"`, ".mjs", "task.Command"} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("%s contains forbidden production execution token %q", name, forbidden)
			}
		}
	}
}
