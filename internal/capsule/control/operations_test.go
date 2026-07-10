package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOperationsRequireFreshHandleAndDeclaredCommand(t *testing.T) {
	root := t.TempDir()
	provider := &provider{name: "synthetic"}
	manager := &Manager{Definitions: defs{"d": {ID: "d", Schema: DefinitionSchema, Source: Source{Kind: SourceSynthetic, SyntheticSpec: "x"}, Digest: "sha256:d", Policy: Policy{Commands: map[string]Command{"echo": {Argv: []string{"echo", "ok"}}}}}}, Instances: NewMemoryInstanceStore(), Providers: map[string]WorkspaceProvider{"synthetic": provider}, Grant: ScopeGrant{ProjectRoot: root, WorkspaceRoots: []string{filepath.Join(root, "capsules")}, Definitions: []string{"d"}, Executors: []string{"synthetic"}, Effects: []string{"exec"}}}
	ctx := context.Background()
	h, err := manager.Create(ctx, CreateRequest{ID: "x", DefinitionID: "d", Owner: "a"})
	if err != nil {
		t.Fatal(err)
	}
	// The fake provider only reports a path; create it to exercise operations.
	path, err := manager.WorkspacePath(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	next, err := manager.WriteFile(ctx, h, "file.txt", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReadFile(ctx, h, "file.txt"); err == nil {
		t.Fatal("stale read succeeded")
	}
	raw, err := manager.ReadFile(ctx, next, "file.txt")
	if err != nil || string(raw) != "hello" {
		t.Fatalf("read=%q %v", raw, err)
	}
	result, err := manager.RunCommand(ctx, next, "echo", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Output != "ok\n" {
		t.Fatalf("run=%#v", result)
	}
	if _, err := manager.RunCommand(ctx, result.Handle, "", []string{"echo", "no"}, 0); err == nil {
		t.Fatal("raw argv succeeded")
	}
}
