package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const ConflictArtifactSchema = "capsule-sync-conflict/v1"
const IntegrationInstanceSchema = "capsule-sync-integration/v1"

type ConflictArtifact struct {
	Schema            string    `json:"schema"`
	PlanDigest        string    `json:"plan_digest"`
	ContinuationToken string    `json:"continuation_token"`
	Operation         Operation `json:"operation"`
	TargetRef         string    `json:"target_ref"`
	Candidate         string    `json:"candidate"`
	Target            string    `json:"target"`
	MergeBase         string    `json:"merge_base"`
	CandidatePaths    []string  `json:"candidate_paths"`
	TargetPaths       []string  `json:"target_paths"`
	OverlapPaths      []string  `json:"overlap_paths"`
	RequiredInputs    []string  `json:"required_inputs"`
}
type IntegrationInstance struct {
	Schema            string    `json:"schema"`
	PlanDigest        string    `json:"plan_digest"`
	ContinuationToken string    `json:"continuation_token"`
	Operation         Operation `json:"operation"`
	TargetRef         string    `json:"target_ref"`
	Candidate         string    `json:"candidate"`
	Target            string    `json:"target"`
	MergeBase         string    `json:"merge_base"`
	InstancePath      string    `json:"instance_path"`
	Branch            string    `json:"branch"`
	State             string    `json:"state"`
	ConflictPaths     []string  `json:"conflict_paths,omitempty"`
	StatusPorcelain   string    `json:"status_porcelain,omitempty"`
}

func (r Reconciler) MaterializeConflictArtifact(ctx context.Context, p Plan, projectRoot string) (ConflictArtifact, string, error) {
	if err := validateConflictPlan(p); err != nil {
		return ConflictArtifact{}, "", err
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return ConflictArtifact{}, "", err
	}
	mergeBase, err := git(ctx, p.Workspace, "merge-base", p.Candidate, p.Expected.Target)
	if err != nil {
		return ConflictArtifact{}, "", err
	}
	base := strings.TrimSpace(mergeBase)
	candidatePaths, err := changedPaths(ctx, p.Workspace, base, p.Candidate)
	if err != nil {
		return ConflictArtifact{}, "", err
	}
	targetPaths, err := changedPaths(ctx, p.Workspace, base, p.Expected.Target)
	if err != nil {
		return ConflictArtifact{}, "", err
	}
	artifact := ConflictArtifact{
		Schema:            ConflictArtifactSchema,
		PlanDigest:        p.Digest,
		ContinuationToken: p.Continuation.Token,
		Operation:         p.Operation,
		TargetRef:         p.TargetRef,
		Candidate:         p.Candidate,
		Target:            p.Expected.Target,
		MergeBase:         base,
		CandidatePaths:    candidatePaths,
		TargetPaths:       targetPaths,
		OverlapPaths:      overlap(candidatePaths, targetPaths),
		RequiredInputs:    append([]string(nil), p.Continuation.RequiredInputs...),
	}
	path := filepath.Join(root, ".capsules", "sync", p.Continuation.Token+".conflict.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ConflictArtifact{}, "", err
	}
	raw, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return ConflictArtifact{}, "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return ConflictArtifact{}, "", err
	}
	return artifact, path, nil
}

func (r Reconciler) MaterializeIntegrationInstance(ctx context.Context, p Plan, projectRoot string) (IntegrationInstance, string, error) {
	if err := validateConflictPlan(p); err != nil {
		return IntegrationInstance{}, "", err
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return IntegrationInstance{}, "", err
	}
	syncDir := filepath.Join(root, ".capsules", "sync")
	if err := os.MkdirAll(syncDir, 0o755); err != nil {
		return IntegrationInstance{}, "", err
	}
	artifactPath := filepath.Join(syncDir, p.Continuation.Token+".integration.json")
	if raw, err := os.ReadFile(artifactPath); err == nil {
		var existing IntegrationInstance
		if err := json.Unmarshal(raw, &existing); err != nil {
			return IntegrationInstance{}, "", err
		}
		return existing, artifactPath, nil
	} else if !os.IsNotExist(err) {
		return IntegrationInstance{}, "", err
	}
	instancePath := filepath.Join(syncDir, p.Continuation.Token+".integration")
	if _, err := os.Stat(instancePath); err == nil {
		return IntegrationInstance{}, "", fmt.Errorf("capsule reconcile: integration instance already exists without artifact")
	} else if !os.IsNotExist(err) {
		return IntegrationInstance{}, "", err
	}
	if err := gitClone(ctx, p.Workspace, instancePath); err != nil {
		return IntegrationInstance{}, "", err
	}
	if _, err := git(ctx, instancePath, "checkout", "-B", "capsule-sync-resolution", p.Candidate); err != nil {
		return IntegrationInstance{}, "", err
	}
	mergeBase, err := git(ctx, instancePath, "merge-base", p.Candidate, p.Expected.Target)
	if err != nil {
		return IntegrationInstance{}, "", err
	}
	mergeOut, mergeErr := git(ctx, instancePath, "merge", "--no-commit", "--no-ff", p.Expected.Target)
	conflictPaths, conflictErr := unmergedPaths(ctx, instancePath)
	if mergeErr != nil && (conflictErr != nil || len(conflictPaths) == 0) {
		return IntegrationInstance{}, "", fmt.Errorf("%w: %s", mergeErr, strings.TrimSpace(mergeOut))
	}
	status, err := git(ctx, instancePath, "status", "--porcelain")
	if err != nil {
		return IntegrationInstance{}, "", err
	}
	state := "prepared"
	if len(conflictPaths) > 0 {
		state = "conflicted"
	}
	rel, err := filepath.Rel(root, instancePath)
	if err != nil {
		return IntegrationInstance{}, "", err
	}
	instance := IntegrationInstance{
		Schema:            IntegrationInstanceSchema,
		PlanDigest:        p.Digest,
		ContinuationToken: p.Continuation.Token,
		Operation:         p.Operation,
		TargetRef:         p.TargetRef,
		Candidate:         p.Candidate,
		Target:            p.Expected.Target,
		MergeBase:         strings.TrimSpace(mergeBase),
		InstancePath:      filepath.ToSlash(rel),
		Branch:            "capsule-sync-resolution",
		State:             state,
		ConflictPaths:     conflictPaths,
		StatusPorcelain:   strings.TrimSpace(status),
	}
	raw, err := json.MarshalIndent(instance, "", "  ")
	if err != nil {
		return IntegrationInstance{}, "", err
	}
	if err := os.WriteFile(artifactPath, append(raw, '\n'), 0o600); err != nil {
		return IntegrationInstance{}, "", err
	}
	return instance, artifactPath, nil
}

func changedPaths(ctx context.Context, dir, from, to string) ([]string, error) {
	return changedPathsWithFilter(ctx, dir, "", from, to)
}

func changedPathsWithFilter(ctx context.Context, dir, filter, from, to string) ([]string, error) {
	args := []string{"diff", "--name-only"}
	if filter != "" {
		args = append(args, filter)
	}
	args = append(args, from, to)
	out, err := git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return splitPathLines(out), nil
}

func splitPathLines(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, filepath.ToSlash(line))
		}
	}
	sort.Strings(paths)
	return paths
}

func unmergedPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	return splitPathLines(out), nil
}

func validateConflictPlan(p Plan) error {
	if p.Digest == "" || p.Digest != planDigest(p) {
		return fmt.Errorf("capsule reconcile: invalid plan digest")
	}
	if p.Class != Diverged || p.Continuation == nil {
		return fmt.Errorf("capsule reconcile: conflict artifact requires a diverged continuation plan")
	}
	return nil
}

func gitClone(ctx context.Context, from, to string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--no-hardlinks", from, to)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone integration instance: %w", err)
	}
	return nil
}

func overlap(a, b []string) []string {
	seen := map[string]bool{}
	for _, path := range a {
		seen[path] = true
	}
	var out []string
	for _, path := range b {
		if seen[path] {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}
