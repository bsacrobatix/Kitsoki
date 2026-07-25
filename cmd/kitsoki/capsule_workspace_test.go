package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/hygiene"
)

type capsuleWorkspaceTestDefinitions map[string]control.Definition

func (d capsuleWorkspaceTestDefinitions) Get(_ context.Context, id string) (control.Definition, error) {
	definition, ok := d[id]
	if !ok {
		return control.Definition{}, control.ErrNotFound
	}
	return definition, nil
}

func (d capsuleWorkspaceTestDefinitions) List(context.Context) ([]control.Definition, error) {
	return nil, nil
}

type capsuleWorkspaceTestProvider struct {
	closeErr       error
	closeCalls     int
	integrateCalls int
}

type capsuleWorkspaceCloseReporter struct {
	quarantine string
	head       string
}

func (p *capsuleWorkspaceCloseReporter) Name() string {
	return string(control.SourceDevWorkspaceScript)
}
func (p *capsuleWorkspaceCloseReporter) Create(_ context.Context, _ control.Definition, in control.Instance) (control.MaterializedWorkspace, error) {
	return control.MaterializedWorkspace{Path: in.Path}, nil
}
func (p *capsuleWorkspaceCloseReporter) Close(ctx context.Context, in control.Instance) error {
	_, err := p.CloseWithResult(ctx, in)
	return err
}
func (p *capsuleWorkspaceCloseReporter) CloseWithResult(_ context.Context, in control.Instance) (control.ClosedWorkspace, error) {
	if err := os.Rename(in.Path, p.quarantine); err != nil {
		return control.ClosedWorkspace{}, err
	}
	return control.ClosedWorkspace{
		Path:        p.quarantine,
		Head:        p.head,
		RecoveryRef: "refs/kitsoki/workspace-teardown-recovery/" + p.head,
	}, nil
}

func (p *capsuleWorkspaceTestProvider) Name() string { return "test" }

func (p *capsuleWorkspaceTestProvider) Create(_ context.Context, _ control.Definition, in control.Instance) (control.MaterializedWorkspace, error) {
	return control.MaterializedWorkspace{Path: in.Path}, nil
}

func (p *capsuleWorkspaceTestProvider) Integrate(context.Context, control.Definition, control.Instance, string) error {
	p.integrateCalls++
	return nil
}

func (p *capsuleWorkspaceTestProvider) Close(context.Context, control.Instance) error {
	p.closeCalls++
	return p.closeErr
}

func TestProjectRelativeWorkspacePathConfinesAndRedactsMachinePath(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, ".capsules", "workspaces", "one")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := projectRelativeWorkspacePath(root, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got != ".capsules/workspaces/one" || strings.Contains(got, root) {
		t.Fatalf("path=%q root=%q", got, root)
	}
	if _, err := projectRelativeWorkspacePath(root, t.TempDir()); err == nil {
		t.Fatal("outside workspace path was accepted")
	}
}

func TestProjectRelativeWorkspacePathUsesStableCapsuleSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	stable := t.TempDir()
	workspace := filepath.Join(stable, "workspaces", "release-alias")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stable, filepath.Join(root, ".capsules")); err != nil {
		t.Fatal(err)
	}

	got, err := projectRelativeWorkspacePath(root, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got != ".capsules/workspaces/release-alias" {
		t.Fatalf("path=%q, want managed project alias", got)
	}

	outside := filepath.Join(t.TempDir(), "release-alias")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := projectRelativeWorkspacePath(root, outside); err == nil {
		t.Fatal("unrelated external path was accepted through capsule alias")
	}
}

func TestCapsuleWorkspaceExecuteRequiresOwnedFreshHandleAndDeclaredCommand(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, "cell")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := control.NewMemoryInstanceStore()
	in, err := store.Create(context.Background(), control.Instance{
		ID:           "cell",
		DefinitionID: "cell-definition",
		Provider:     "test",
		Path:         workspace,
		State:        control.StateReady,
		Lease:        control.Lease{Owner: "bakeoff-cell"},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := &control.Manager{
		Definitions: capsuleWorkspaceTestDefinitions{"cell-definition": {
			ID: "cell-definition",
			Policy: control.Policy{Commands: map[string]control.Command{
				"prepare_branch": {Argv: []string{"sh", "-c", "printf prepared"}},
			}},
		}},
		Instances: store,
		Grant: control.ScopeGrant{
			ProjectRoot:    root,
			WorkspaceRoots: []string{workspaceRoot},
			Effects:        []string{"exec"},
		},
	}
	ctx := context.Background()
	if _, err := capsuleWorkspaceExecute(ctx, manager, control.Handle{ID: in.ID, Generation: in.Generation + 1}, "bakeoff-cell", "prepare_branch", time.Second); !errors.Is(err, control.ErrStale) {
		t.Fatalf("stale generation error=%v", err)
	}
	if _, err := capsuleWorkspaceExecute(ctx, manager, control.Handle{ID: in.ID, Generation: in.Generation}, "another-owner", "prepare_branch", time.Second); !errors.Is(err, control.ErrDenied) {
		t.Fatalf("foreign owner error=%v", err)
	}
	result, err := capsuleWorkspaceExecute(ctx, manager, control.Handle{ID: in.ID, Generation: in.Generation}, "bakeoff-cell", "prepare_branch", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Output != "prepared" || result.Handle.Generation == in.Generation {
		t.Fatalf("result=%#v", result)
	}
	if _, err := capsuleWorkspaceExecute(ctx, manager, result.Handle, "bakeoff-cell", "", time.Second); err == nil {
		t.Fatal("empty command id was accepted")
	}
	if _, err := capsuleWorkspaceExecute(ctx, manager, result.Handle, "bakeoff-cell", "missing", time.Second); !errors.Is(err, control.ErrDenied) {
		t.Fatalf("undeclared command error=%v", err)
	}
}

func TestCapsuleWorkspaceIntegrateKeepsLandingSuccessWhenTeardownFails(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, "one")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := control.NewMemoryInstanceStore()
	in, err := store.Create(context.Background(), control.Instance{
		ID:           "one",
		DefinitionID: "development",
		Provider:     "test",
		Path:         workspace,
		State:        control.StateCommitted,
		Lease:        control.Lease{Owner: "stored-owner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &capsuleWorkspaceTestProvider{closeErr: errors.New("workspace busy")}
	manager := &control.Manager{
		Definitions: capsuleWorkspaceTestDefinitions{"development": {ID: "development"}},
		Instances:   store,
		Providers:   map[string]control.WorkspaceProvider{"test": provider},
		Grant: control.ScopeGrant{
			ProjectRoot:    root,
			WorkspaceRoots: []string{workspaceRoot},
			Effects:        []string{"local_reconcile"},
		},
	}

	result, err := capsuleWorkspaceIntegrate(context.Background(), manager, in, "go test ./...", true, "")
	if err != nil {
		t.Fatalf("successful landing was reported as failed: %v", err)
	}
	if !result.Integrated || result.Closed || result.Owner != "stored-owner" || !strings.Contains(result.CleanupError, "workspace busy") {
		t.Fatalf("result=%#v", result)
	}
	if provider.integrateCalls != 1 || provider.closeCalls != 1 {
		t.Fatalf("integrate calls=%d close calls=%d", provider.integrateCalls, provider.closeCalls)
	}
	updated, err := store.Get(context.Background(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != control.StateIntegrated {
		t.Fatalf("state=%s", updated.State)
	}
}

func TestCapsuleWorkspaceIntegrateReturnsFinalClosedGeneration(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, "closed")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := control.NewMemoryInstanceStore()
	in, err := store.Create(context.Background(), control.Instance{
		ID:           "closed",
		DefinitionID: "development",
		Provider:     "test",
		Path:         workspace,
		State:        control.StateCommitted,
		Lease:        control.Lease{Owner: "stored-owner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &capsuleWorkspaceTestProvider{}
	manager := &control.Manager{
		Definitions: capsuleWorkspaceTestDefinitions{"development": {ID: "development"}},
		Instances:   store,
		Providers:   map[string]control.WorkspaceProvider{"test": provider},
		Grant: control.ScopeGrant{
			ProjectRoot:    root,
			WorkspaceRoots: []string{workspaceRoot},
			Effects:        []string{"local_reconcile"},
		},
	}

	result, err := capsuleWorkspaceIntegrate(context.Background(), manager, in, "", true, "")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := store.Get(context.Background(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Integrated || !result.Closed || result.CleanupError != "" || result.Generation != closed.Generation || closed.State != control.StateClosed {
		t.Fatalf("result=%#v instance=%#v", result, closed)
	}
}

func TestCapsuleWorkspaceCloseRequiresExpectedGenerationAndReturnsReceipt(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, "evaluated")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := control.NewMemoryInstanceStore()
	in, err := store.Create(context.Background(), control.Instance{
		ID:           "evaluated",
		DefinitionID: "pinned",
		Provider:     "test",
		Path:         workspace,
		State:        control.StateDirty,
		Lease:        control.Lease{Owner: "evaluation-adapter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &capsuleWorkspaceTestProvider{}
	manager := &control.Manager{
		Definitions: capsuleWorkspaceTestDefinitions{"pinned": {ID: "pinned"}},
		Instances:   store,
		Providers:   map[string]control.WorkspaceProvider{"test": provider},
		Grant: control.ScopeGrant{
			ProjectRoot:    root,
			WorkspaceRoots: []string{workspaceRoot},
			Effects:        []string{"workspace_manage"},
		},
	}

	if _, err := capsuleWorkspaceClose(context.Background(), manager, in, in.Generation+1, "evaluation-adapter"); !errors.Is(err, control.ErrStale) {
		t.Fatalf("stale close error=%v", err)
	}
	if provider.closeCalls != 0 {
		t.Fatalf("provider close calls=%d after stale handle", provider.closeCalls)
	}
	receipt, err := capsuleWorkspaceClose(context.Background(), manager, in, in.Generation, "evaluation-adapter")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Schema != "capsule-workspace-close/v1" || !receipt.OK || receipt.Project != root || receipt.ID != in.ID || receipt.DefinitionID != "pinned" || receipt.Provider != "test" || receipt.Owner != "evaluation-adapter" || receipt.State != control.StateClosed || receipt.Generation <= in.Generation {
		t.Fatalf("receipt=%#v", receipt)
	}
	if provider.closeCalls != 1 {
		t.Fatalf("provider close calls=%d", provider.closeCalls)
	}
}

func TestCapsuleWorkspaceClosePersistsExactQuarantineRetentionAuthority(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, "terminal")
	quarantine := filepath.Join(workspaceRoot, "closed-terminal-20260725T180622Z-42-0")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := control.NewMemoryInstanceStore()
	in, err := store.Create(context.Background(), control.Instance{
		ID:           "terminal",
		DefinitionID: "development",
		Provider:     string(control.SourceDevWorkspaceScript),
		Path:         workspace,
		State:        control.StateCommitted,
		Lease:        control.Lease{Owner: "reaper"},
	})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	provider := &capsuleWorkspaceCloseReporter{quarantine: quarantine, head: head}
	manager := &control.Manager{
		Definitions: capsuleWorkspaceTestDefinitions{"development": {ID: "development"}},
		Instances:   store,
		Providers: map[string]control.WorkspaceProvider{
			string(control.SourceDevWorkspaceScript): provider,
		},
		Grant: control.ScopeGrant{
			ProjectRoot:    root,
			WorkspaceRoots: []string{workspaceRoot},
			Effects:        []string{"workspace_manage"},
		},
	}
	originalProbe := capsuleWorkspaceActivityProbe
	originalProcessProbe := capsuleWorkspaceProcessProbe
	originalNow := capsuleWorkspaceCloseNow
	t.Cleanup(func() {
		capsuleWorkspaceActivityProbe = originalProbe
		capsuleWorkspaceProcessProbe = originalProcessProbe
		capsuleWorkspaceCloseNow = originalNow
	})
	inactiveProbe := func(_ context.Context, paths []string) (hygiene.WorkspaceActivity, error) {
		return hygiene.WorkspaceActivity{Known: true, PIDsByPath: map[string][]int{paths[0]: {}}}, nil
	}
	capsuleWorkspaceActivityProbe = inactiveProbe
	capsuleWorkspaceProcessProbe = inactiveProbe
	probeTimes := []time.Time{
		time.Date(2026, 7, 25, 18, 6, 21, 0, time.UTC),
		time.Date(2026, 7, 25, 18, 6, 22, 0, time.UTC),
	}
	capsuleWorkspaceCloseNow = func() time.Time {
		next := probeTimes[0]
		probeTimes = probeTimes[1:]
		return next
	}

	result, err := capsuleWorkspaceClose(context.Background(), manager, in, in.Generation, "reaper")
	if err != nil {
		t.Fatal(err)
	}
	wantReceipt := ".capsules/retention/receipts/" + filepath.Base(quarantine) + ".json"
	if !result.OK || result.Quarantine != ".capsules/workspaces/"+filepath.Base(quarantine) ||
		result.RetentionReceipt != wantReceipt || result.RetentionError != "" {
		t.Fatalf("result=%#v", result)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(wantReceipt)))
	if err != nil {
		t.Fatal(err)
	}
	var receipt hygiene.RetentionReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := filepath.EvalSymlinks(filepath.Join(root, ".capsules"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.WorkspaceID != filepath.Base(quarantine) || receipt.WorkspacePath != result.Quarantine ||
		receipt.ProjectStateRoot != stateRoot || receipt.Head != head ||
		receipt.ProcessSnapshot.Kind == receipt.ActivityProbe.Kind ||
		!receipt.ProcessSnapshot.Safe || !receipt.ActivityProbe.Safe ||
		receipt.EligibleAfter.Sub(receipt.IssuedAt) != hygiene.DefaultWorkspaceRetentionAge {
		t.Fatalf("receipt=%#v", receipt)
	}
}
