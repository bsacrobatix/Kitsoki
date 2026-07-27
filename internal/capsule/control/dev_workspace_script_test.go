package control

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevWorkspaceScriptProviderMapsProtectedLifecycle(t *testing.T) {
	project := t.TempDir()
	runControlGit(t, project, "init", "-b", "main")
	runControlGit(t, project, "config", "user.name", "test")
	runControlGit(t, project, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, project, "add", "README.md")
	runControlGit(t, project, "commit", "-m", "base")
	runControlGit(t, project, "branch", "staging/local")

	var calls [][]string
	runner := ScriptRunnerFunc(func(ctx context.Context, _ string, _ string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "create":
			path := args[argValue(t, args, "--id")+1]
			root := args[argValue(t, args, "--root")+1]
			path = filepath.Join(root, path)
			branch := args[argValue(t, args, "--branch")+1]
			base := args[argValue(t, args, "--base")+1]
			cmd := exec.CommandContext(ctx, "git", "clone", "--local", "--origin", "source", project, path)
			if out, err := cmd.CombinedOutput(); err != nil {
				return out, err
			}
			cmd = exec.CommandContext(ctx, "git", "switch", "-c", branch, "source/"+base)
			cmd.Dir = path
			if out, err := cmd.CombinedOutput(); err != nil {
				return out, err
			}
			return nil, os.WriteFile(filepath.Join(path, instanceSentinel), []byte("managed\n"), 0o644)
		case "teardown":
			workspace := filepath.Join(args[argValue(t, args, "--root")+1], args[len(args)-1])
			quarantine := filepath.Join(filepath.Dir(workspace), "closed-"+filepath.Base(workspace)+"-20260725T180622Z-42-0")
			headCommand := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
			headCommand.Dir = workspace
			headOutput, err := headCommand.CombinedOutput()
			if err != nil {
				return headOutput, err
			}
			head := strings.TrimSpace(string(headOutput))
			runControlGit(t, project, "update-ref", "refs/kitsoki/workspace-teardown-recovery/"+head, head)
			if err := os.Rename(workspace, quarantine); err != nil {
				return nil, err
			}
			return []byte("removed from managed workspaces: " + workspace + "\nquarantined: " + quarantine + "\n"), nil
		default:
			return nil, nil
		}
	})
	provider := DevWorkspaceScriptProvider{ProjectRoot: project, Runner: runner}
	path := filepath.Join(project, ".capsules", "workspaces", "one")
	definition := Definition{ID: "development", Source: Source{Kind: SourceDevWorkspaceScript, Development: DevelopmentSource{Base: "staging/local", Target: "staging/local", BranchPrefix: "agent/", Bootstrap: true}}}
	materialized, err := provider.Create(context.Background(), definition, Instance{ID: "one", Path: path, Lease: Lease{Owner: "owner-one"}})
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Path != path || materialized.Branch != "agent/one" || materialized.Head == "" {
		t.Fatalf("materialized %#v", materialized)
	}
	if !containsArgs(calls[0], "--bootstrap") || !containsArgs(calls[0], "--target", "staging/local") || !containsArgs(calls[0], "--session-id", "owner-one") {
		t.Fatalf("create args %q", calls[0])
	}
	if err := provider.Integrate(context.Background(), definition, Instance{ID: "one", Path: path}, "go test ./internal/capsule"); err != nil {
		t.Fatal(err)
	}
	if !containsArgs(calls[1], "--gate", "go test ./internal/capsule") {
		t.Fatalf("integrate args %q", calls[1])
	}
	closed, err := provider.CloseWithResult(context.Background(), Instance{ID: "one", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	wantClosed, err := filepath.EvalSymlinks(filepath.Join(project, ".capsules", "workspaces", "closed-one-20260725T180622Z-42-0"))
	if err != nil {
		t.Fatal(err)
	}
	if closed.Path != wantClosed ||
		closed.Head == "" || closed.RecoveryRef != "refs/kitsoki/workspace-teardown-recovery/"+closed.Head {
		t.Fatalf("closed=%#v", closed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("workspace remains after close: %v", err)
	}
}

func TestDevWorkspaceScriptProviderRefusesMissingConfiguredBase(t *testing.T) {
	project := t.TempDir()
	runControlGit(t, project, "init", "-b", "main")
	runControlGit(t, project, "config", "user.name", "test")
	runControlGit(t, project, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, project, "add", "README.md")
	runControlGit(t, project, "commit", "-m", "base")
	provider := DevWorkspaceScriptProvider{ProjectRoot: project, Runner: ScriptRunnerFunc(func(context.Context, string, string, ...string) ([]byte, error) {
		t.Fatal("runner must not run for an unavailable base")
		return nil, nil
	})}
	_, err := provider.Create(context.Background(), Definition{ID: "development", Source: Source{Kind: SourceDevWorkspaceScript, Development: DevelopmentSource{Base: "staging/local", Target: "staging/local"}}}, Instance{ID: "one", Path: filepath.Join(project, ".capsules", "one")})
	if err == nil || !strings.Contains(err.Error(), "configured base") {
		t.Fatalf("missing-base error %v", err)
	}
}

func TestCreateDevWorkspaceScriptRefusesLegacyBranchMismatchWithoutRegisteringIt(t *testing.T) {
	project := t.TempDir()
	root := filepath.Join(project, ".capsules", "workspaces")
	path := filepath.Join(root, "legacy")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, path, "init", "-b", "agent/current")
	if err := os.WriteFile(filepath.Join(path, ".kitsoki-dev-workspace.json"), []byte(`{"branch":"agent/original"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		Definitions: defs{"development": {ID: "development", Source: Source{Kind: SourceDevWorkspaceScript}}},
		Instances:   NewMemoryInstanceStore(),
		Providers:   map[string]WorkspaceProvider{string(SourceDevWorkspaceScript): DevWorkspaceScriptProvider{}},
		Grant:       ScopeGrant{ProjectRoot: project, WorkspaceRoots: []string{root}, Definitions: []string{"development"}, Executors: []string{string(SourceDevWorkspaceScript)}},
	}
	_, err := manager.CreateDevWorkspaceScript(context.Background(), CreateRequest{ID: "legacy", DefinitionID: "development", Owner: "test"})
	if err == nil || !strings.Contains(err.Error(), "remains unregistered") || !strings.Contains(err.Error(), "agent/original") || !strings.Contains(err.Error(), "agent/current") {
		t.Fatalf("legacy mismatch error = %v", err)
	}
	if _, err := manager.Instances.Get(context.Background(), "legacy"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy workspace was registered: %v", err)
	}
}

func TestCreateDevWorkspaceScriptNamesMatchingLegacyAsResumeOnly(t *testing.T) {
	project := t.TempDir()
	root := filepath.Join(project, ".capsules", "workspaces")
	path := filepath.Join(root, "legacy")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, path, "init", "-b", "agent/legacy")
	if err := os.WriteFile(filepath.Join(path, ".kitsoki-dev-workspace.json"), []byte(`{"branch":"agent/legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		Definitions: defs{"development": {ID: "development", Source: Source{Kind: SourceDevWorkspaceScript}}},
		Instances:   NewMemoryInstanceStore(),
		Providers:   map[string]WorkspaceProvider{string(SourceDevWorkspaceScript): DevWorkspaceScriptProvider{}},
		Grant:       ScopeGrant{ProjectRoot: project, WorkspaceRoots: []string{root}, Definitions: []string{"development"}, Executors: []string{string(SourceDevWorkspaceScript)}},
	}
	_, err := manager.CreateDevWorkspaceScript(context.Background(), CreateRequest{ID: "legacy", DefinitionID: "development", Owner: "test"})
	if err == nil || !strings.Contains(err.Error(), "resume it through the legacy script lifecycle") {
		t.Fatalf("matching legacy error = %v", err)
	}
}

func TestVerifyDevWorkspaceScriptInstanceRejectsBranchDrift(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".capsules", "workspaces", "legacy")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, path, "init", "-b", "agent/current")
	if err := os.WriteFile(filepath.Join(path, ".kitsoki-dev-workspace.json"), []byte(`{"id":"legacy","branch":"agent/original"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{}
	err := manager.VerifyDevWorkspaceScriptInstance(context.Background(), Instance{ID: "legacy", Provider: string(SourceDevWorkspaceScript), Path: path, Branch: "agent/current"})
	if err == nil || !strings.Contains(err.Error(), "unregistered legacy state for CI") || !strings.Contains(err.Error(), "agent/original") {
		t.Fatalf("branch drift error = %v", err)
	}
}

func argValue(t *testing.T, args []string, want string) int {
	t.Helper()
	for i, value := range args {
		if value == want && i+1 < len(args) {
			return i
		}
	}
	t.Fatalf("%q not present in %q", want, args)
	return -1
}

func containsArgs(args []string, want ...string) bool {
	return strings.Contains("\x00"+strings.Join(args, "\x00")+"\x00", "\x00"+strings.Join(want, "\x00")+"\x00")
}

func runControlGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
