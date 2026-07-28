package graphsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	yaml "github.com/goccy/go-yaml"

	"kitsoki/internal/graph/pgcatalog"
)

// Write routing decides WHERE a mutating graph.* call materializes: straight
// into the bound catalog's working tree ("direct", the historical behavior),
// or through the repo's managed clone-backed capsule workflow ("capsule"):
// the write lands in a scripts/dev-workspace.sh workspace and is committed
// with a DCO sign-off on a dedicated agent branch. The primary checkout
// (typically pinned to main) is never touched, and MCP proposal writes do not
// auto-merge into staging/local.
//
// Resolution precedence, per bound catalog:
//  1. the server-level --write-via flag (direct|capsule), when not "auto";
//  2. the catalog repo's checked-in `.kitsoki/project-profile.yaml`
//     `graph: {write_via: ...}` block;
//  3. "capsule" when the bound catalog is visibly protected (no write bits)
//     and the catalog repo carries the managed-workspace helper; this keeps a
//     protected primary checkout from turning a normal graph proposal into a
//     raw permission error;
//  4. "direct" — a repo with no .kitsoki profile (or no git repo at all)
//     just edits in the working directory.
const (
	WriteViaAuto    = "auto"
	WriteViaDirect  = "direct"
	WriteViaCapsule = "capsule"
)

// DefaultWriteVia is used when --write-via is omitted: consult the catalog
// repo's project profile, else write directly.
const DefaultWriteVia = WriteViaAuto

// ValidateWriteVia checks a --write-via flag value against the allow-list.
func ValidateWriteVia(via string) error {
	switch via {
	case WriteViaAuto, WriteViaDirect, WriteViaCapsule:
		return nil
	default:
		return fmt.Errorf("mcp-graph: --write-via must be one of %s, %s, %s (got %q)", WriteViaAuto, WriteViaDirect, WriteViaCapsule, via)
	}
}

// defaultCapsuleGate is kept for compatibility with existing project profiles.
// Capsule-routed MCP proposal writes now stop at a committed review branch
// instead of invoking the merge gate automatically.
const defaultCapsuleGate = "git diff --check"

// projectGraphConfig is the `graph:` block of a repo's checked-in
// `.kitsoki/project-profile.yaml` — the same file that carries the repo's
// other automation defaults (build/test commands, dev-story profile).
type projectGraphConfig struct {
	// WriteVia is direct|capsule (empty means unset → direct).
	WriteVia string `yaml:"write_via"`
	// Gate is retained for older project profiles. MCP proposal writes do not
	// automatically merge into staging/local, so this value is not used by the
	// capsule proposal route.
	Gate string `yaml:"gate"`
}

// readProjectGraphConfig best-effort peeks repoRoot's
// .kitsoki/project-profile.yaml `graph:` block. Any problem — no file, bad
// YAML, unknown write_via value — degrades to the zero config (direct):
// "if no .kitsoki then just try to edit in the working dir".
func readProjectGraphConfig(repoRoot string) projectGraphConfig {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".kitsoki", "project-profile.yaml"))
	if err != nil {
		return projectGraphConfig{}
	}
	var doc struct {
		Graph projectGraphConfig `yaml:"graph"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return projectGraphConfig{}
	}
	switch strings.TrimSpace(doc.Graph.WriteVia) {
	case WriteViaDirect, WriteViaCapsule:
		doc.Graph.WriteVia = strings.TrimSpace(doc.Graph.WriteVia)
	default:
		doc.Graph.WriteVia = ""
	}
	return doc.Graph
}

// catalogLooksProtected reports whether the catalog itself is deliberately
// read-only. It intentionally consults mode bits rather than attempting a
// write: an MCP server must not probe a protected primary checkout by
// modifying it, and tests often run as an owner that could bypass permission
// checks. An explicit --write-via direct still wins over this auto heuristic.
func catalogLooksProtected(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o222 == 0
}

// WorkspaceRunResult is one dev-workspace.sh invocation's outcome.
type WorkspaceRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// WorkspaceRunner is the only process seam the capsule write route uses.
// Production executes the repo's checked-in scripts/dev-workspace.sh; tests
// inject a deterministic fake and never spawn a real clone.
type WorkspaceRunner interface {
	Run(ctx context.Context, dir, program string, args ...string) (WorkspaceRunResult, error)
}

type execWorkspaceRunner struct{}

func (execWorkspaceRunner) Run(ctx context.Context, dir, program string, args ...string) (WorkspaceRunResult, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	stdout, err := cmd.Output()
	result := WorkspaceRunResult{Stdout: string(stdout)}
	if exit, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exit.ExitCode()
		result.Stderr = string(exit.Stderr)
		return result, nil
	}
	return result, err
}

// WriteRouter resolves and caches each bound catalog's write route and, for
// capsule-routed catalogs, owns the lazy workspace lifecycle: create on the
// first write, commit after every successful write, workspace kept alive for
// the server's lifetime so later reads and writes see same-session proposal
// changes without touching staging/local.
type WriteRouter struct {
	via    string // server-level override: auto|direct|capsule
	runner WorkspaceRunner

	mu     sync.Mutex
	routes map[string]*catalogRoute // keyed by primary (bound) catalog path
}

type catalogRoute struct {
	via      string // resolved: direct|capsule
	repoRoot string
	relPath  string // catalog path relative to repoRoot
	gate     string
	wsID     string
	wsPath   string // non-empty once the capsule workspace exists
}

// CapsuleWriteEvidence is returned from capsule-routed writes so MCP clients
// can hand off the dedicated branch for review/PR creation without inferring
// state from staging/local.
type CapsuleWriteEvidence struct {
	Via           string `json:"via"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspacePath string `json:"workspace_path"`
	Branch        string `json:"branch"`
	BaseBranch    string `json:"base_branch"`
	CommitSHA     string `json:"commit_sha"`
	PRReady       bool   `json:"pr_ready"`
}

// NewWriteRouter builds the router. via is the server-level --write-via
// value ("" is treated as auto); a nil runner uses the real script executor.
func NewWriteRouter(via string, runner WorkspaceRunner) *WriteRouter {
	if via == "" {
		via = WriteViaAuto
	}
	if runner == nil {
		runner = execWorkspaceRunner{}
	}
	return &WriteRouter{via: via, runner: runner, routes: map[string]*catalogRoute{}}
}

// route resolves (and caches) the write route for a bound catalog path.
// Resolution never fails: anything that prevents consulting a project
// profile (not a git repo, unreadable file) means direct.
func (r *WriteRouter) route(primaryPath string) *catalogRoute {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rt, ok := r.routes[primaryPath]; ok {
		return rt
	}
	rt := &catalogRoute{via: WriteViaDirect, gate: defaultCapsuleGate}
	// A pg-backed catalog has no working tree: capsule routing (a managed
	// clone/commit/merge of catalog FILES) is meaningless for it, so pg refs
	// are unconditionally direct — the store's own transactional commit is
	// the write protection. This deliberately overrides even an explicit
	// server-level --write-via capsule.
	if pgcatalog.IsRef(primaryPath) {
		r.routes[primaryPath] = rt
		return rt
	}
	root, err := repoRootFor(primaryPath)
	if err == nil {
		rt.repoRoot = root
		// git prints the canonical toplevel (symlinks resolved) while the
		// bound path may be the un-resolved spelling (macOS /var vs
		// /private/var); Rel between the two would escape the workspace.
		canon := primaryPath
		if real, symErr := filepath.EvalSymlinks(primaryPath); symErr == nil {
			canon = real
		}
		if rel, relErr := filepath.Rel(root, canon); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			rt.relPath = rel
		}
		cfg := readProjectGraphConfig(root)
		if cfg.WriteVia != "" {
			rt.via = cfg.WriteVia
		}
		if strings.TrimSpace(cfg.Gate) != "" {
			rt.gate = cfg.Gate
		}
		// A protected primary checkout is a capsule signal even for projects
		// that have not yet added a graph block to their profile. Only select
		// it when the repo actually supplies the lifecycle helper; otherwise
		// preserve the historical direct route and its native filesystem error.
		if cfg.WriteVia == "" && catalogLooksProtected(primaryPath) {
			script := filepath.Join(root, "scripts", "dev-workspace.sh")
			if info, statErr := os.Stat(script); statErr == nil && info.Mode()&0o111 != 0 {
				rt.via = WriteViaCapsule
			}
		}
	}
	if r.via != WriteViaAuto {
		rt.via = r.via
	}
	// A capsule route without a resolvable repo root can never materialize a
	// workspace; keep the requested via so writePath reports the real
	// problem instead of silently writing to the primary checkout.
	r.routes[primaryPath] = rt
	return rt
}

// readPath maps a bound catalog path to the path read tools should load.
// Once a capsule workspace exists for the catalog, reads come from it — a
// changeset proposed through the capsule route must be visible to the
// graph.get/changeset/apply calls that follow it. That same-session read
// projection is the workspace branch, not staging/local.
// Before the first write there is no workspace and reads use the bound path.
func (r *WriteRouter) readPath(primaryPath string) string {
	rt := r.route(primaryPath)
	r.mu.Lock()
	defer r.mu.Unlock()
	if rt.via == WriteViaCapsule && rt.wsPath != "" {
		return filepath.Join(rt.wsPath, rt.relPath)
	}
	return primaryPath
}

// writePath maps a bound catalog path to the path write tools should mutate,
// creating the capsule workspace on first use for a capsule-routed catalog.
func (r *WriteRouter) writePath(ctx context.Context, primaryPath string) (string, *ErrorPayload) {
	rt := r.route(primaryPath)
	if rt.via != WriteViaCapsule {
		return primaryPath, nil
	}
	if ep := r.ensureWorkspace(ctx, rt); ep != nil {
		return "", ep
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return filepath.Join(rt.wsPath, rt.relPath), nil
}

// capsuleWorkspaceID derives the workspace id for a capsule-routed catalog:
// stable per server process so every write in one session shares one
// workspace, distinct across concurrent servers.
func capsuleWorkspaceID() string {
	return fmt.Sprintf("graph-mcp-%d", os.Getpid())
}

func (r *WriteRouter) script(rt *catalogRoute) string {
	return filepath.Join(rt.repoRoot, "scripts", "dev-workspace.sh")
}

func (r *WriteRouter) ensureWorkspace(ctx context.Context, rt *catalogRoute) *ErrorPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rt.wsPath != "" {
		return nil
	}
	if rt.repoRoot == "" || rt.relPath == "" {
		return NewError(CodeCapsuleWorkflow,
			"capsule write routing: the bound catalog is not inside a git repository, so no capsule workspace can be created",
			"bind a catalog inside a git repo, or run the server with --write-via direct")
	}
	script := r.script(rt)
	if info, err := os.Stat(script); err != nil || info.Mode()&0o111 == 0 {
		return NewError(CodeCapsuleWorkflow,
			fmt.Sprintf("capsule write routing: %s is missing or not executable — this repo does not carry the managed workspace lifecycle", script),
			"add scripts/dev-workspace.sh to the repo, set graph.write_via: direct in .kitsoki/project-profile.yaml, or run the server with --write-via direct")
	}
	id := capsuleWorkspaceID()
	res, err := r.runner.Run(ctx, rt.repoRoot, script,
		"create", "--repo", rt.repoRoot, "--root", filepath.Join(rt.repoRoot, ".capsules", "workspaces"),
		"--id", id, "--branch", "agent/"+id, "--json")
	if err != nil {
		return NewError(CodeCapsuleWorkflow, "capsule write routing: create workspace: "+err.Error(), "")
	}
	if res.ExitCode != 0 {
		return NewError(CodeCapsuleWorkflow,
			fmt.Sprintf("capsule write routing: dev-workspace.sh create exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr)), "")
	}
	var created struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &created); err != nil || strings.TrimSpace(created.Path) == "" {
		return NewError(CodeCapsuleWorkflow, "capsule write routing: dev-workspace.sh create returned no workspace path", "")
	}
	rt.wsID = id
	rt.wsPath = filepath.Clean(created.Path)
	return nil
}

// integrate commits a completed capsule-routed write on the dedicated
// workspace branch. It deliberately does not merge into staging/local: MCP
// proposal writes are review/PR handoffs, and the response includes the branch
// and SHA needed for that handoff. A clean workspace ("no changes" — e.g. the
// engine rejected the write, or a dry-run) is a successful no-op, never an
// error.
func (r *WriteRouter) integrate(ctx context.Context, primaryPath, message string) (*CapsuleWriteEvidence, *ErrorPayload) {
	rt := r.route(primaryPath)
	r.mu.Lock()
	skip := rt.via != WriteViaCapsule || rt.wsPath == ""
	r.mu.Unlock()
	if skip {
		return nil, nil
	}
	script := r.script(rt)
	commit, err := r.runner.Run(ctx, rt.repoRoot, script, "commit", rt.wsPath, "--message", message, "--json")
	if err != nil {
		return nil, NewError(CodeCapsuleWorkflow, "capsule write routing: commit: "+err.Error(),
			"the write is on disk in "+rt.wsPath+" but not committed")
	}
	if commit.ExitCode != 0 {
		if strings.Contains(commit.Stderr, "no changes") {
			return nil, nil
		}
		return nil, NewError(CodeCapsuleWorkflow,
			fmt.Sprintf("capsule write routing: dev-workspace.sh commit exited %d: %s", commit.ExitCode, strings.TrimSpace(commit.Stderr)),
			"the write is on disk in "+rt.wsPath+" but not committed")
	}
	var committed struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal([]byte(commit.Stdout), &committed); err != nil || strings.TrimSpace(committed.SHA) == "" {
		return nil, NewError(CodeCapsuleWorkflow, "capsule write routing: dev-workspace.sh commit returned no commit sha",
			"the write may be committed in "+rt.wsPath+"; inspect the workspace branch agent/"+rt.wsID)
	}
	return &CapsuleWriteEvidence{
		Via:           WriteViaCapsule,
		WorkspaceID:   rt.wsID,
		WorkspacePath: rt.wsPath,
		Branch:        "agent/" + rt.wsID,
		BaseBranch:    "staging/local",
		CommitSHA:     committed.SHA,
		PRReady:       true,
	}, nil
}

// resolveRead resolves a tool call's catalog arg for a READ: the bound
// alias/path, with the path re-routed into the capsule workspace when one
// exists. anchorPath is always the bound (primary) path — receipts and
// feedback artifacts anchor to the primary repo root, never to a disposable
// workspace.
func (d *Deps) resolveRead(arg string) (enginePath, anchorPath, alias string, ep *ErrorPayload) {
	anchorPath, alias, ep = d.Catalogs.Resolve(arg)
	if ep != nil {
		return "", "", "", ep
	}
	enginePath = anchorPath
	if d.Router != nil {
		enginePath = d.Router.readPath(anchorPath)
	}
	return enginePath, anchorPath, alias, nil
}

// resolveWrite resolves a tool call's catalog arg for a WRITE, materializing
// the capsule workspace on first use when the catalog routes via capsule.
func (d *Deps) resolveWrite(ctx context.Context, arg string) (enginePath, anchorPath, alias string, ep *ErrorPayload) {
	anchorPath, alias, ep = d.Catalogs.Resolve(arg)
	if ep != nil {
		return "", "", "", ep
	}
	enginePath = anchorPath
	if d.Router != nil {
		enginePath, ep = d.Router.writePath(ctx, anchorPath)
		if ep != nil {
			return "", "", "", ep
		}
	}
	return enginePath, anchorPath, alias, nil
}

// integrateWrite lands a successful write through the capsule workflow (a
// no-op for direct-routed catalogs). bestEffort suppresses the error payload
// — used when the tool result already carries a more important outcome (an
// engine rejection) that a lifecycle warning must not mask.
func (d *Deps) integrateWrite(ctx context.Context, anchorPath, message string, bestEffort bool) (*CapsuleWriteEvidence, *ErrorPayload) {
	if d.Router == nil {
		return nil, nil
	}
	evidence, ep := d.Router.integrate(ctx, anchorPath, message)
	if bestEffort {
		return evidence, nil
	}
	return evidence, ep
}
