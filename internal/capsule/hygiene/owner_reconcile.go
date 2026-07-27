package hygiene

// This file contains the deliberately narrow recovery seam for abandoned
// *active* Capsule records.  It never removes a workspace.  Its only effect is
// to change an identity-bound, proven-orphaned ready/materializing record to
// failed, after which the ordinary receipt-bound lifecycle remains responsible
// for close and purge.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/capsule/control"
)

const (
	OwnerReconcileSchema     = "capsule-owner-reconcile-plan/v1"
	defaultOwnerReconcileAge = 72 * time.Hour
)

type OwnerReconcileOptions struct {
	ProjectRoot           string
	MinAge                time.Duration
	Now                   func() time.Time
	ReadWorkspaceActivity func(context.Context, []string) (WorkspaceActivity, error)
	ReadWorkspaceCommands func(context.Context, []string) (WorkspaceActivity, error)
}

// OwnerReconcileCandidate is a state-only recovery proposal. Eligible does
// not authorize deletion: it authorizes only the guarded lifecycle transition
// recorded by ApplyOwnerReconcilePlan.
type OwnerReconcileCandidate struct {
	WorkspaceID string        `json:"workspace_id"`
	Generation  uint64        `json:"generation"`
	State       control.State `json:"state"`
	Owner       string        `json:"owner"`
	UpdatedAt   time.Time     `json:"updated_at"`
	AgeSeconds  int64         `json:"age_seconds"`
	Eligible    bool          `json:"eligible"`
	Action      string        `json:"action,omitempty"`
	Reason      string        `json:"reason"`
	Evidence    []string      `json:"evidence,omitempty"`
}

type OwnerReconcilePlan struct {
	Schema     string                    `json:"schema"`
	Project    string                    `json:"project"`
	MinAge     string                    `json:"min_age"`
	DryRun     bool                      `json:"dry_run"`
	Candidates []OwnerReconcileCandidate `json:"candidates"`
}

type OwnerReconcileResult struct {
	Plan       OwnerReconcilePlan        `json:"plan"`
	Reconciled []OwnerReconcileCandidate `json:"reconciled"`
	Skipped    []OwnerReconcileCandidate `json:"skipped,omitempty"`
}

func BuildOwnerReconcilePlan(ctx context.Context, opts OwnerReconcileOptions) (OwnerReconcilePlan, error) {
	root, err := canonicalRoot(opts.ProjectRoot)
	if err != nil {
		return OwnerReconcilePlan{}, err
	}
	age := opts.MinAge
	if age <= 0 {
		age = defaultOwnerReconcileAge
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	plan := OwnerReconcilePlan{Schema: OwnerReconcileSchema, Project: root, MinAge: age.String(), DryRun: true, Candidates: []OwnerReconcileCandidate{}}
	store := control.FileInstanceStore{Root: filepath.Join(root, ".capsules", "workspaces")}
	instances, err := store.List(ctx)
	if err != nil {
		return OwnerReconcilePlan{}, err
	}
	for _, in := range instances {
		if in.State != control.StateReady && in.State != control.StateMaterializing {
			continue
		}
		candidate := inspectOwnerReconcileCandidate(ctx, root, in, age, now, opts)
		plan.Candidates = append(plan.Candidates, candidate)
	}
	sort.Slice(plan.Candidates, func(i, j int) bool { return plan.Candidates[i].WorkspaceID < plan.Candidates[j].WorkspaceID })
	return plan, nil
}

func ApplyOwnerReconcilePlan(ctx context.Context, opts OwnerReconcileOptions) (OwnerReconcileResult, error) {
	plan, err := BuildOwnerReconcilePlan(ctx, opts)
	if err != nil {
		return OwnerReconcileResult{}, err
	}
	result := OwnerReconcileResult{Plan: plan, Reconciled: []OwnerReconcileCandidate{}, Skipped: []OwnerReconcileCandidate{}}
	store := control.FileInstanceStore{Root: filepath.Join(plan.Project, ".capsules", "workspaces")}
	for _, candidate := range plan.Candidates {
		if !candidate.Eligible {
			continue
		}
		_, err := store.CompareAndSwap(ctx, candidate.WorkspaceID, candidate.Generation, func(in *control.Instance) error {
			if in.State != candidate.State || in.Lease.Owner != candidate.Owner {
				return control.ErrStale
			}
			in.State = control.StateFailed
			in.Failure = &control.FailureEvidence{
				Schema: ownerReconcileFailureSchema, Kind: ownerReconcileFailureKind, Action: ownerReconcileFailureAction,
				WorkspaceID: in.ID, SourceGeneration: candidate.Generation, Path: in.Path, Head: in.Head,
				Branch: in.Branch, Owner: in.Lease.Owner, Evidence: append([]string(nil), candidate.Evidence...), RecordedAt: optsNow(opts).UTC(),
			}
			return nil
		})
		if err != nil {
			candidate.Eligible = false
			candidate.Action = ""
			candidate.Reason = "record changed after dry-run; left untouched"
			result.Skipped = append(result.Skipped, candidate)
			continue
		}
		result.Reconciled = append(result.Reconciled, candidate)
	}
	return result, nil
}

func optsNow(opts OwnerReconcileOptions) time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

func inspectOwnerReconcileCandidate(ctx context.Context, project string, in control.Instance, minAge time.Duration, now time.Time, opts OwnerReconcileOptions) OwnerReconcileCandidate {
	c := OwnerReconcileCandidate{WorkspaceID: in.ID, Generation: in.Generation, State: in.State, Owner: in.Lease.Owner, UpdatedAt: in.UpdatedAt.UTC(), Reason: "not proven orphaned"}
	if !in.UpdatedAt.IsZero() && now.After(in.UpdatedAt) {
		c.AgeSeconds = int64(now.Sub(in.UpdatedAt).Seconds())
	}
	if strings.TrimSpace(in.Lease.Owner) == "" {
		c.Reason = "lease owner is absent"
		return c
	}
	if in.UpdatedAt.IsZero() || now.Sub(in.UpdatedAt) < minAge {
		c.Reason = "record is within the bounded orphan age guard"
		return c
	}
	workspaceRoot := filepath.Join(project, ".capsules", "workspaces")
	path, err := canonicalPath(in.Path)
	if err != nil || !pathContains(workspaceRoot, path) {
		c.Reason = "recorded path is outside the managed workspace root"
		return c
	}
	if _, err := os.Stat(path); err != nil {
		c.Reason = "workspace path is unavailable"
		return c
	}
	if fileExists(filepath.Join(workspaceRoot, ".initializing", in.ID)) {
		c.Reason = "workspace has an initialization marker"
		return c
	}
	identity, err := reconcileIdentity(ctx, path, in)
	if err != nil {
		c.Reason = "workspace identity is not proven: " + err.Error()
		return c
	}
	c.Evidence = append(c.Evidence, "managed_path", "manifest_identity", identity)
	activityReader := opts.ReadWorkspaceActivity
	if activityReader == nil {
		activityReader = ProbeWorkspaceActivity
	}
	commandReader := opts.ReadWorkspaceCommands
	if commandReader == nil {
		commandReader = ProbeWorkspaceProcessCommands
	}
	for _, probe := range []struct {
		name string
		read func(context.Context, []string) (WorkspaceActivity, error)
	}{{"open-file", activityReader}, {"process-command", commandReader}} {
		activity, err := probe.read(ctx, []string{path})
		if err != nil || !activity.Known {
			c.Reason = probe.name + " liveness probe is inconclusive"
			return c
		}
		if pids := activity.PIDsByPath[path]; len(pids) > 0 {
			c.Reason = fmt.Sprintf("workspace is live in %s probe: %v", probe.name, pids)
			return c
		}
		c.Evidence = append(c.Evidence, probe.name+"_inactive")
	}
	if !in.Lease.ExpiresAt.IsZero() && now.Before(in.Lease.ExpiresAt) {
		c.Reason = "workspace lease is still unexpired"
		return c
	}
	if in.Lease.ExpiresAt.IsZero() {
		c.Evidence = append(c.Evidence, "lease_has_no_live_expiry")
	} else {
		c.Evidence = append(c.Evidence, "lease_expired")
	}
	c.Eligible, c.Action = true, "mark_failed_cleanup_eligible"
	c.Reason = "identity-bound aged active record has no live owner/process/lease; mark failed only, never delete"
	return c
}

func reconcileIdentity(ctx context.Context, path string, in control.Instance) (string, error) {
	sentinel, err := os.ReadFile(filepath.Join(path, workspaceSentinel))
	if err != nil {
		return "", fmt.Errorf("capsule sentinel: %w", err)
	}
	owner, err := os.ReadFile(filepath.Join(path, ".kitsoki-owner"))
	if err == nil {
		if strings.TrimSpace(string(owner)) != in.Lease.Owner {
			return "", fmt.Errorf("owner marker does not match lease owner")
		}
		if err := reconcileWorkspaceManifest(path, in, strings.TrimSpace(string(sentinel)) == "dev-workspace"); err != nil {
			return "", err
		}
		return "owner_marker", nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("owner marker: %w", err)
	}
	// Pre-owner-marker records are recoverable only through stronger durable
	// Git identity. A missing marker is never silently treated as ownership
	// absence: the checkout must prove the exact recorded head and branch.
	if err := reconcileWorkspaceManifest(path, in, strings.TrimSpace(string(sentinel)) == "dev-workspace"); err != nil {
		return "", err
	}
	if err := reconcileLegacyGitIdentity(ctx, path, in); err != nil {
		return "", err
	}
	return "legacy_git_identity", nil
}

func reconcileWorkspaceManifest(path string, in control.Instance, devWorkspace bool) error {
	if !devWorkspace {
		sentinel, err := os.ReadFile(filepath.Join(path, workspaceSentinel))
		if err != nil || strings.TrimSpace(string(sentinel)) != in.ID {
			return fmt.Errorf("capsule sentinel does not bind this instance")
		}
		return nil
	}
	var manifest struct {
		ID        string `json:"id"`
		Workspace string `json:"workspace"`
	}
	raw, err := os.ReadFile(filepath.Join(path, ".kitsoki-dev-workspace.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		return fmt.Errorf("development manifest is unreadable")
	}
	manifestPath, err := canonicalPath(manifest.Workspace)
	if err != nil || manifest.ID != in.ID || manifestPath != path {
		return fmt.Errorf("development manifest does not bind this instance")
	}
	return nil
}

func reconcileLegacyGitIdentity(ctx context.Context, path string, in control.Instance) error {
	if strings.TrimSpace(in.Head) == "" || strings.TrimSpace(in.Branch) == "" {
		return fmt.Errorf("legacy record lacks durable head or branch identity")
	}
	head, err := reconcileGit(ctx, path, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != strings.TrimSpace(in.Head) {
		return fmt.Errorf("Git HEAD does not match durable record")
	}
	branch, err := reconcileGit(ctx, path, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != strings.TrimSpace(in.Branch) {
		return fmt.Errorf("Git branch does not match durable record")
	}
	return nil
}

func reconcileGit(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
