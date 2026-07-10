// Package hygiene plans and applies safe local cleanup for Capsule workspaces,
// CI sidecars, and explicitly granted build caches.
package hygiene

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const Schema = "capsule-hygiene-plan/v1"

type Options struct {
	ProjectRoot         string
	KeepRuns            int
	IncludeCapsuleCache bool
	IncludeGoBuildCache bool
	GoCachePath         string
	CleanGoCache        func(context.Context) error
}

type Candidate struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
	Safe   bool   `json:"safe"`
}

type Plan struct {
	Schema     string      `json:"schema"`
	Project    string      `json:"project"`
	KeepRuns   int         `json:"keep_runs"`
	Candidates []Candidate `json:"candidates"`
	TotalBytes int64       `json:"total_bytes"`
}

type ApplyResult struct {
	Plan       Plan        `json:"plan"`
	Removed    []Candidate `json:"removed"`
	TotalBytes int64       `json:"total_bytes"`
}

func BuildPlan(ctx context.Context, opts Options) (Plan, error) {
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return Plan{}, err
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	keep := opts.KeepRuns
	if keep <= 0 {
		keep = 20
	}
	plan := Plan{Schema: Schema, Project: root, KeepRuns: keep, Candidates: []Candidate{}}
	runCandidates, err := ciRunCandidates(root, keep)
	if err != nil {
		return Plan{}, err
	}
	plan.Candidates = append(plan.Candidates, runCandidates...)
	if opts.IncludeCapsuleCache {
		cacheCandidates, err := projectCacheCandidates(root)
		if err != nil {
			return Plan{}, err
		}
		plan.Candidates = append(plan.Candidates, cacheCandidates...)
	}
	if opts.IncludeGoBuildCache {
		goCandidate, err := goCacheCandidate(ctx, opts)
		if err != nil {
			return Plan{}, err
		}
		if goCandidate.Path != "" {
			plan.Candidates = append(plan.Candidates, goCandidate)
		}
	}
	sort.Slice(plan.Candidates, func(i, j int) bool {
		if plan.Candidates[i].Kind != plan.Candidates[j].Kind {
			return plan.Candidates[i].Kind < plan.Candidates[j].Kind
		}
		return plan.Candidates[i].Path < plan.Candidates[j].Path
	})
	for _, c := range plan.Candidates {
		plan.TotalBytes += c.Bytes
	}
	return plan, nil
}

func Apply(ctx context.Context, opts Options) (ApplyResult, error) {
	plan, err := BuildPlan(ctx, opts)
	if err != nil {
		return ApplyResult{}, err
	}
	var result ApplyResult
	result.Plan = plan
	for _, c := range plan.Candidates {
		if !c.Safe {
			continue
		}
		switch c.Kind {
		case "go-build-cache":
			if opts.CleanGoCache != nil {
				err = opts.CleanGoCache(ctx)
			} else {
				err = cleanGoCache(ctx)
			}
		default:
			err = removeProjectPath(plan.Project, c.Path)
		}
		if err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, c)
		result.TotalBytes += c.Bytes
	}
	return result, nil
}

type runFile struct {
	job   string
	path  string
	mod   time.Time
	bytes int64
}

func ciRunCandidates(root string, keep int) ([]Candidate, error) {
	dir := filepath.Join(root, ".capsules", "ci")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []runFile
	sidecars := map[string][]runFile{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, name)
		job := strings.TrimSuffix(name, ".run.json")
		if job != name {
			runs = append(runs, runFile{job: job, path: path, mod: info.ModTime(), bytes: info.Size()})
			continue
		}
		for _, suffix := range []string{".receipt.json", ".trace.json"} {
			job = strings.TrimSuffix(name, suffix)
			if job != name {
				sidecars[job] = append(sidecars[job], runFile{job: job, path: path, mod: info.ModTime(), bytes: info.Size()})
				break
			}
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].mod.After(runs[j].mod) })
	if len(runs) <= keep {
		return nil, nil
	}
	var out []Candidate
	for _, run := range runs[keep:] {
		files := append([]runFile{run}, sidecars[run.job]...)
		for _, file := range files {
			out = append(out, Candidate{ID: "ci-run:" + run.job + ":" + filepath.Base(file.path), Kind: "ci-run", Path: file.path, Bytes: file.bytes, Reason: fmt.Sprintf("older than newest %d Capsule CI run record(s)", keep), Safe: true})
		}
	}
	return out, nil
}

func projectCacheCandidates(root string) ([]Candidate, error) {
	dir := filepath.Join(root, ".capsules", "cache")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		bytes, err := pathSize(path)
		if err != nil {
			return nil, err
		}
		out = append(out, Candidate{ID: "capsule-cache:" + entry.Name(), Kind: "capsule-cache", Path: path, Bytes: bytes, Reason: "explicit project Capsule cache cleanup requested", Safe: true})
	}
	return out, nil
}

func goCacheCandidate(ctx context.Context, opts Options) (Candidate, error) {
	path := opts.GoCachePath
	if path == "" {
		out, err := exec.CommandContext(ctx, "go", "env", "GOCACHE").Output()
		if err != nil {
			return Candidate{}, fmt.Errorf("capsule hygiene: go env GOCACHE: %w", err)
		}
		path = strings.TrimSpace(string(out))
	}
	if path == "" {
		return Candidate{}, nil
	}
	bytes, err := pathSize(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Candidate{}, nil
		}
		return Candidate{}, err
	}
	return Candidate{ID: "go-build-cache", Kind: "go-build-cache", Path: path, Bytes: bytes, Reason: "explicit Go build/test cache cleanup requested", Safe: true}, nil
}

func removeProjectPath(root, path string) error {
	realRoot := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = real
	}
	realPath := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		realPath = real
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("capsule hygiene: refusing to remove path outside project: %s", path)
	}
	return os.RemoveAll(path)
}

func cleanGoCache(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache", "-testcache")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("capsule hygiene: go clean: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func pathSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
