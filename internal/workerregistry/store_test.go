package workerregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdd_CreatesFileAndEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kitsoki.local.yaml")
	entries, err := Add(path, Entry{ID: "vm-a", Label: "VM A", Placement: "thin", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "vm-a" {
		t.Fatalf("entries = %#v", entries)
	}
	reg, err := Load(filepath.Join(dir, "missing-base.yaml"), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].ID != "vm-a" {
		t.Fatalf("reloaded entries = %#v", reg.Entries)
	}
}

func TestAdd_DuplicateIDErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kitsoki.local.yaml")
	if _, err := Add(path, Entry{ID: "vm-a", Label: "VM A", Placement: "thin", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(path, Entry{ID: "vm-a", Label: "VM A dup", Placement: "thin", Enabled: true}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestAdd_PreservesUnrelatedTopLevelKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kitsoki.local.yaml")
	if err := os.WriteFile(path, []byte("story_dirs: [./stories]\nagent_launch_policy:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(path, Entry{ID: "vm-a", Label: "VM A", Placement: "thin", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "story_dirs:") || !strings.Contains(body, "agent_launch_policy:") || !strings.Contains(body, "workers:") {
		t.Fatalf("expected preserved keys plus workers:, got:\n%s", body)
	}
}

func TestRemove_DeletesEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kitsoki.local.yaml")
	if _, err := Add(path, Entry{ID: "vm-a", Label: "VM A", Placement: "thin", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := Remove(path, "vm-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestRemove_MissingIDErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kitsoki.local.yaml")
	if _, err := Remove(path, "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetEnabled_TogglesFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kitsoki.local.yaml")
	if _, err := Add(path, Entry{ID: "vm-a", Label: "VM A", Placement: "thin", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := SetEnabled(path, "vm-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Enabled {
		t.Fatal("expected disabled")
	}
	entries, err = SetEnabled(path, "vm-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if !entries[0].Enabled {
		t.Fatal("expected enabled")
	}
}

func TestMutate_InvalidResultDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".kitsoki.local.yaml")
	if _, err := Add(path, Entry{ID: "vm-a", Label: "VM A", Placement: "thin", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Mutate(path, func(entries []Entry) ([]Entry, error) {
		entries[0].Placement = "bogus"
		return entries, nil
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("file must not change on validation failure")
	}
}
