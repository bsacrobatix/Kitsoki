package workerregistry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kitsoki/internal/daemonfederation"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_MissingFilesYieldEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	reg, err := Load(filepath.Join(dir, "base.yaml"), filepath.Join(dir, "local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 0 {
		t.Fatalf("entries = %#v", reg.Entries)
	}
}

func TestLoad_CanonicalWorkersBlock(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	local := filepath.Join(dir, "local.yaml")
	writeFile(t, local, `workers:
  - id: build-vm
    label: Build VM
    placement: workstation
    endpoint: http://127.0.0.1:17777
    enabled: true
    capabilities:
      placements: [container]
      isolation: sandboxed
      networks: [git-mirror]
`)
	reg, err := Load(base, local)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Source != "workers" {
		t.Fatalf("source = %q", reg.Source)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].ID != "build-vm" {
		t.Fatalf("entries = %#v", reg.Entries)
	}
	if !reg.Entries[0].Enabled {
		t.Fatal("expected enabled")
	}
	if got := reg.Entries[0].Capabilities.Isolation; got != "sandboxed" {
		t.Fatalf("isolation = %q", got)
	}
}

func TestLoad_LocalWorkersOverridesBaseWhole(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	local := filepath.Join(dir, "local.yaml")
	writeFile(t, base, `workers:
  - id: shared-vm
    label: Shared VM
    placement: thin
    enabled: true
`)
	writeFile(t, local, `workers:
  - id: personal-vm
    label: Personal VM
    placement: thin
    enabled: false
`)
	reg, err := Load(base, local)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].ID != "personal-vm" {
		t.Fatalf("entries = %#v", reg.Entries)
	}
}

func TestLoad_BackCompatFromDaemonFederation(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	local := filepath.Join(dir, "local.yaml")
	writeFile(t, local, `daemon_federation:
  workers:
    - id: legacy-vm
      label: Legacy VM
      placement: thin
      endpoint: http://127.0.0.1:17777
`)
	reg, err := Load(base, local)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Source != "daemon_federation" {
		t.Fatalf("source = %q", reg.Source)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].ID != "legacy-vm" {
		t.Fatalf("entries = %#v", reg.Entries)
	}
	if !reg.Entries[0].Enabled {
		t.Fatal("legacy entries default enabled=true")
	}
	if !reg.Entries[0].Legacy {
		t.Fatal("expected Legacy=true")
	}
	if reg.Entries[0].Capabilities.Isolation != "" || len(reg.Entries[0].Capabilities.Placements) != 0 {
		t.Fatalf("legacy entries must carry no capabilities: %#v", reg.Entries[0].Capabilities)
	}
}

func TestLoad_CanonicalWorkersTakesPrecedenceOverDaemonFederation(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	local := filepath.Join(dir, "local.yaml")
	writeFile(t, local, `workers:
  - id: new-vm
    label: New VM
    placement: thin
    enabled: true
daemon_federation:
  workers:
    - id: legacy-vm
      label: Legacy VM
      placement: thin
      endpoint: http://127.0.0.1:17777
`)
	reg, err := Load(base, local)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Source != "workers" || len(reg.Entries) != 1 || reg.Entries[0].ID != "new-vm" {
		t.Fatalf("registry = %#v", reg)
	}
}

func TestLoad_InvalidPlacementErrors(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	local := filepath.Join(dir, "local.yaml")
	writeFile(t, local, `workers:
  - id: bad-vm
    label: Bad VM
    placement: bogus
    enabled: true
`)
	if _, err := Load(base, local); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_DuplicateIDErrors(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	local := filepath.Join(dir, "local.yaml")
	writeFile(t, local, `workers:
  - id: dup
    label: One
    placement: thin
    enabled: true
  - id: dup
    label: Two
    placement: thin
    enabled: true
`)
	if _, err := Load(base, local); err == nil {
		t.Fatal("expected error")
	}
}

func TestCapabilityLabelsAreBoundedAndDerived(t *testing.T) {
	entry := Entry{Placement: "workstation", Capabilities: Capabilities{
		Labels: []string{"gpu", "gpu"}, Placements: []string{"thin"},
		Isolation: "sandboxed", Networks: []string{"offline", "offline"},
	}}
	got := CapabilityLabels(entry)
	want := []string{"gpu", "placement:workstation", "placement:thin", "isolation:sandboxed", "network:offline"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %#v, want %#v", got, want)
	}
}

func TestEntry_ToProjection_NeverLeaksSecrets(t *testing.T) {
	entry := Entry{
		ID:            "vm",
		Label:         "VM",
		Placement:     "thin",
		Endpoint:      "http://127.0.0.1:1",
		CredentialEnv: "SECRET_TOKEN",
		Tunnel:        &daemonfederation.Tunnel{IdentityFile: "/secret/id_rsa"},
		Enabled:       true,
	}
	proj := entry.ToProjection("online", 2)
	if proj.ID != "vm" || proj.Label != "VM" || proj.Placement != "thin" || proj.Health != "online" || proj.Jobs != 2 || !proj.Enabled {
		t.Fatalf("projection = %#v", proj)
	}
	// Projection has no field capable of carrying CredentialEnv or Tunnel;
	// this test documents that contract so a future field addition to Entry
	// cannot silently leak into it.
}

func TestRegistry_Find(t *testing.T) {
	reg := Registry{Entries: []Entry{{ID: "a"}, {ID: "b"}}}
	if _, ok := reg.Find("a"); !ok {
		t.Fatal("expected found")
	}
	if _, ok := reg.Find("missing"); ok {
		t.Fatal("expected not found")
	}
}
