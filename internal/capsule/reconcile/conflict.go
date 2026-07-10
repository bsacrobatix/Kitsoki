package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ConflictArtifactSchema = "capsule-sync-conflict/v1"

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

func (r Reconciler) MaterializeConflictArtifact(ctx context.Context, p Plan, projectRoot string) (ConflictArtifact, string, error) {
	if p.Digest == "" || p.Digest != planDigest(p) {
		return ConflictArtifact{}, "", fmt.Errorf("capsule reconcile: invalid plan digest")
	}
	if p.Class != Diverged || p.Continuation == nil {
		return ConflictArtifact{}, "", fmt.Errorf("capsule reconcile: conflict artifact requires a diverged continuation plan")
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

func changedPaths(ctx context.Context, dir, from, to string) ([]string, error) {
	out, err := git(ctx, dir, "diff", "--name-only", from, to)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, filepath.ToSlash(line))
		}
	}
	sort.Strings(paths)
	return paths, nil
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
