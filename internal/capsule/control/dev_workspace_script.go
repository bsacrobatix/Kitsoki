package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ScriptRunner is injected so the compatibility provider can be tested
// without creating a real repository or executing project hooks.
type ScriptRunner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type ScriptRunnerFunc func(context.Context, string, string, ...string) ([]byte, error)

func (f ScriptRunnerFunc) Run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	return f(ctx, dir, program, args...)
}

type execScriptRunner struct{}

func (execScriptRunner) Run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// DevWorkspaceScriptProvider is the compatibility adapter for the existing
// Kitsoki protected clone workflow. It is deliberately not a generic source:
// a project opts in by declaring source.kind dev-workspace-script.
type DevWorkspaceScriptProvider struct {
	ProjectRoot string
	Runner      ScriptRunner
}

// CreateDevWorkspaceScript creates the durable instance record before asking
// the compatibility script to create its checkout. The script is therefore a
// materializer, never an identity-adoption path.
func (m *Manager) CreateDevWorkspaceScript(ctx context.Context, req CreateRequest) (Handle, error) {
	if strings.TrimSpace(req.DefinitionID) == "" {
		req.DefinitionID = "development"
	}
	def, err := m.Definition(ctx, req.DefinitionID)
	if err != nil {
		return Handle{}, err
	}
	if def.Source.Kind != SourceDevWorkspaceScript {
		return Handle{}, fmt.Errorf("capsule control: definition %q is not a dev-workspace-script definition", def.ID)
	}
	if _, err := m.Instances.Get(ctx, req.ID); err == nil {
		return m.Create(ctx, req)
	} else if !errors.Is(err, ErrNotFound) {
		return Handle{}, err
	}
	root, err := m.workspaceRoot(def)
	if err != nil {
		return Handle{}, err
	}
	path, err := ResolveWorkspacePath(root, req.ID, false)
	if err != nil {
		return Handle{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return Handle{}, describeLegacyDevWorkspace(ctx, path, req.ID)
	} else if !os.IsNotExist(err) {
		return Handle{}, fmt.Errorf("capsule control: inspect workspace %q: %w", req.ID, err)
	}
	return m.Create(ctx, req)
}

// describeLegacyDevWorkspace refuses to backfill an instance for an existing
// checkout. A manifest is immutable provenance, so a branch rewrite cannot be
// repaired into a native CI identity after the fact.
func describeLegacyDevWorkspace(ctx context.Context, path, id string) error {
	raw, err := os.ReadFile(filepath.Join(path, ".kitsoki-dev-workspace.json"))
	if err != nil {
		return fmt.Errorf("capsule control: existing workspace %q is unregistered legacy state; create a fresh registered Capsule with a new id and replay its work: %w", id, err)
	}
	var manifest struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("capsule control: existing workspace %q has unreadable legacy manifest: %w", id, err)
	}
	branch, err := runGit(ctx, path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("capsule control: inspect legacy workspace %q branch: %w", id, err)
	}
	branch = strings.TrimSpace(branch)
	if branch == manifest.Branch && branch != "" {
		return fmt.Errorf("capsule control: legacy workspace %q is unregistered but its Git branch %q matches its immutable manifest; resume it through the legacy script lifecycle without Capsule CI identity", id, branch)
	}
	return fmt.Errorf("capsule control: legacy workspace %q remains unregistered: immutable manifest branch %q differs from Git branch %q; create a fresh registered Capsule with a new id for the current branch and move or replay the work there", id, manifest.Branch, branch)
}

// VerifyDevWorkspaceScriptInstance is the CI/merge-queue admission check for
// a registered compatibility workspace. It rejects any checkout whose
// immutable script manifest no longer names the live Git branch. Older native
// records may exist, but they cannot turn a rewritten legacy clone into valid
// provenance.
func (m *Manager) VerifyDevWorkspaceScriptInstance(ctx context.Context, in Instance) error {
	if in.Provider != string(SourceDevWorkspaceScript) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(in.Path, ".kitsoki-dev-workspace.json"))
	if err != nil {
		return fmt.Errorf("capsule control: registered workspace %q is missing its immutable dev-workspace manifest: %w", in.ID, err)
	}
	var manifest struct {
		ID     string `json:"id"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("capsule control: registered workspace %q has unreadable dev-workspace manifest: %w", in.ID, err)
	}
	branch, err := runGit(ctx, in.Path, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("capsule control: inspect registered workspace %q branch: %w", in.ID, err)
	}
	branch = strings.TrimSpace(branch)
	if manifest.ID != in.ID || manifest.Branch == "" || branch == "" || manifest.Branch != branch || in.Branch != branch {
		return fmt.Errorf("capsule control: workspace %q is unregistered legacy state for CI: immutable manifest branch %q, Git branch %q, recorded branch %q; create a fresh registered Capsule and replay the work", in.ID, manifest.Branch, branch, in.Branch)
	}
	return nil
}

func (DevWorkspaceScriptProvider) Name() string { return string(SourceDevWorkspaceScript) }

func (p DevWorkspaceScriptProvider) Create(ctx context.Context, def Definition, in Instance) (MaterializedWorkspace, error) {
	if def.Source.Kind != SourceDevWorkspaceScript {
		return MaterializedWorkspace{}, fmt.Errorf("dev workspace script: definition %q has source %q", def.ID, def.Source.Kind)
	}
	root, err := projectRoot(p.ProjectRoot)
	if err != nil {
		return MaterializedWorkspace{}, err
	}
	development := def.Source.Development
	base := development.Base
	if base == "" {
		base = "staging/local"
	}
	branchPrefix := development.BranchPrefix
	if branchPrefix == "" {
		branchPrefix = "agent/"
	}
	branch := branchPrefix + in.ID
	if _, err := runGit(ctx, root, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		// POG fix: on a leased pool worker the materialized source is a git
		// bundle clone checked out at a detached HEAD (the sealed source
		// commit) with no local branch refs, so the configured base (e.g.
		// "main") does not resolve. The detached HEAD IS the base to branch
		// from there, so fall back to it rather than failing the whole story.
		if _, headErr := runGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}"); headErr == nil {
			base = "HEAD"
		} else {
			return MaterializedWorkspace{}, fmt.Errorf("dev workspace script: configured base %q is unavailable in this checkout; run from the protected project checkout or refresh its local branch: %w", base, err)
		}
	}
	args := []string{"create", "--repo", root, "--root", filepath.Dir(in.Path), "--id", in.ID, "--branch", branch, "--base", base, "--target", development.Target}
	if development.Bootstrap {
		args = append(args, "--bootstrap")
	}
	if output, err := p.runner().Run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), args...); err != nil {
		return MaterializedWorkspace{}, scriptError("create", err, output)
	}
	if _, err := os.Stat(filepath.Join(in.Path, instanceSentinel)); err != nil {
		return MaterializedWorkspace{}, fmt.Errorf("dev workspace script: missing capsule sentinel: %w", err)
	}
	head, err := runGit(ctx, in.Path, "rev-parse", "HEAD")
	if err != nil {
		return MaterializedWorkspace{}, err
	}
	currentBranch, err := runGit(ctx, in.Path, "branch", "--show-current")
	if err != nil {
		return MaterializedWorkspace{}, err
	}
	return MaterializedWorkspace{Path: in.Path, SourceRef: base, Head: strings.TrimSpace(head), Branch: strings.TrimSpace(currentBranch)}, nil
}

func (p DevWorkspaceScriptProvider) Integrate(ctx context.Context, _ Definition, in Instance, gate string) error {
	root, err := projectRoot(p.ProjectRoot)
	if err != nil {
		return err
	}
	args := []string{"merge", "--repo", root, "--root", filepath.Dir(in.Path), in.ID}
	if strings.TrimSpace(gate) != "" {
		args = append(args, "--gate", gate)
	}
	if output, err := p.runner().Run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), args...); err != nil {
		return scriptError("integrate", err, output)
	}
	return nil
}

func (p DevWorkspaceScriptProvider) Close(ctx context.Context, in Instance) error {
	root, err := projectRoot(p.ProjectRoot)
	if err != nil {
		return err
	}
	if output, err := p.runner().Run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), "teardown", "--repo", root, "--root", filepath.Dir(in.Path), in.ID); err != nil {
		return scriptError("close", err, output)
	}
	return nil
}

func scriptError(operation string, err error, output []byte) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("dev workspace script: %s: %w", operation, err)
	}
	return fmt.Errorf("dev workspace script: %s: %w: %s", operation, err, message)
}

func (p DevWorkspaceScriptProvider) runner() ScriptRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return execScriptRunner{}
}

var _ WorkspaceProvider = DevWorkspaceScriptProvider{}
var _ WorkspaceIntegrator = DevWorkspaceScriptProvider{}
