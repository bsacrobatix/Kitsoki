package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kitsoki/internal/basestories"
)

// Conflict resolution for a conflicted integration instance. Conflicts are
// never merely quarantined: the queue first replays recorded resolutions
// (git rerere), then drives the project's kitsoki git-ops conflict_resolver
// agent — the write-fenced Read/Edit agent declared in
// stories/git-ops/app.yaml; the queue, not the agent, performs every git
// operation. Outcomes are strictly classified:
//
//   - resolved            → merge committed, the train continues
//   - conflicts remain    → needs_conflict_input, continuation retained for a
//     human (resume / override / reject all apply)
//   - harness broken      → HarnessError → needs_human immediately; a broken
//     launch path must not burn bounded retry attempts
const gitOpsAppRelPath = "stories/git-ops/app.yaml"

func (p ProtectedIntegration) resolveConflicts(ctx context.Context, root, instancePath, continuation string, conflictPaths []string) ([]string, error) {
	var evidence []string
	// Recorded resolutions first: deterministic, free, and exactly what a
	// human already approved for this conflict shape.
	if out, err := gitOutput(ctx, instancePath, "rerere"); err == nil && out != "" {
		evidence = append(evidence, "queue:rerere:"+out)
	}
	remaining, err := unresolvedConflictPaths(ctx, instancePath, conflictPaths)
	if err != nil {
		return evidence, err
	}
	if len(remaining) > 0 {
		if strings.TrimSpace(p.ResolverCommand) != "" {
			output, resolveErr := p.runner().Run(ctx, instancePath, "sh", "-c", p.ResolverCommand)
			evidence = append(evidence, commandEvidence("queue:resolver", output)...)
			if resolveErr != nil {
				return evidence, fmt.Errorf("queue: resolver continuation %s: %w", continuation, resolveErr)
			}
		} else {
			gitOpsEvidence, gitOpsErr := p.runGitOpsResolver(ctx, root, instancePath, remaining)
			evidence = append(evidence, gitOpsEvidence...)
			if gitOpsErr != nil {
				return evidence, gitOpsErr
			}
		}
		remaining, err = unresolvedConflictPaths(ctx, instancePath, conflictPaths)
		if err != nil {
			return evidence, err
		}
	}
	if len(remaining) > 0 {
		return evidence, fmt.Errorf("queue: %d conflicts unresolved; continuation %s is retained", len(remaining), continuation)
	}
	// The queue owns every git operation: stage the resolved paths and commit
	// the merge.
	for _, path := range conflictPaths {
		if _, err := gitOutput(ctx, instancePath, "add", "--", path); err != nil {
			return evidence, err
		}
	}
	if still, err := gitOutput(ctx, instancePath, "diff", "--name-only", "--diff-filter=U"); err != nil {
		return evidence, err
	} else if still != "" {
		return evidence, fmt.Errorf("queue: unmerged paths remain after resolution; continuation %s is retained", continuation)
	}
	if _, err := gitOutput(ctx, instancePath, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		if _, err := gitOutput(ctx, instancePath, "rerere"); err != nil {
			evidence = append(evidence, "queue:rerere-record-failed")
		}
		if _, err := gitOutput(ctx, instancePath, "-c", "user.name=kitsoki-queue", "-c", "user.email=queue@kitsoki.invalid", "commit", "--no-edit"); err != nil {
			return evidence, err
		}
	}
	evidence = append(evidence, "queue:conflicts-resolved="+strings.Join(conflictPaths, ","))
	return evidence, nil
}

// runGitOpsResolver launches a git-ops conflict_resolver against the
// integration instance. The app.yaml is resolved in two tiers, project-local
// always winning:
//
//  1. the project's own stories/git-ops/app.yaml (gitOpsAppRelPath);
//  2. the embedded kitsoki story library's git-ops story, reached ONLY when
//     tier 1 is absent (GitOpsEmbeddedResolver / defaultGitOpsEmbeddedResolver)
//     — this is what gives a project with no stories/git-ops of its own (e.g.
//     POG) a working automatic resolver anyway.
//
// A project with neither has no automatic resolver at all: that is still a
// no-op (needs_conflict_input, no retry burn), but a LOUD one — the evidence
// records exactly why, rather than the bare unqualified tag a silent skip
// would leave behind. A story that resolves (either tier) but whose launch
// fails is a broken harness (needs_human, no retry burn); a broken embedded
// fallback mechanism itself (not merely "no story found") is likewise a
// harness failure rather than a silent no-op.
func (p ProtectedIntegration) runGitOpsResolver(ctx context.Context, root, instancePath string, conflictPaths []string) ([]string, error) {
	appPath := filepath.Join(root, filepath.FromSlash(gitOpsAppRelPath))
	source := "project-local"
	if _, err := os.Stat(appPath); err != nil {
		embeddedPath, embErr := p.gitOpsEmbeddedAppPath(ctx)
		if embErr != nil {
			return nil, Harness(fmt.Errorf("queue: embedded git-ops fallback: %w", embErr))
		}
		if embeddedPath == "" {
			return []string{
				"queue:git-ops-resolver-unavailable no automatic conflict resolver: no project-local " +
					gitOpsAppRelPath + " and no embedded @kitsoki/git-ops story available",
			}, nil
		}
		appPath = embeddedPath
		source = "embedded-library"
	}
	bin := p.KitsokiBin
	if strings.TrimSpace(bin) == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, Harness(fmt.Errorf("queue: resolve kitsoki binary: %w", err))
		}
		bin = exe
	}
	task := "Resolve the merge conflicts in this repository. Conflicted files:\n" + strings.Join(conflictPaths, "\n") +
		"\nKeep the target branch as the base and re-apply the candidate branch's additive intent. Edit only the conflicted files. Do not run git commands. Remove every conflict marker."
	// KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE marks this as a sanctioned managed
	// launch so the launch-policy shim does not refuse the integration
	// instance as a protected-root session.
	output, err := p.runner().Run(ctx, instancePath, "env", "KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE=1", bin,
		"agent", "launch", "--app", appPath, "--agent", "conflict_resolver", "--mode", "codeact", "--exec", "--task", task)
	evidence := append([]string{"queue:git-ops-resolver-source=" + source}, commandEvidence("queue:git-ops-resolver", output)...)
	if err != nil {
		return evidence, Harness(fmt.Errorf("queue: git-ops conflict_resolver launch: %w", err))
	}
	return evidence, nil
}

// gitOpsEmbeddedAppPath resolves the git-ops app.yaml from the embedded
// kitsoki story library fallback, delegating to the injected
// GitOpsEmbeddedResolver when set or defaultGitOpsEmbeddedResolver otherwise.
// A ("", nil) result means "the fallback has nothing to offer" (not staged,
// or staged but lacking a git-ops story) — the caller turns that into a loud
// no-op, not a harness failure; a non-nil error means the fallback mechanism
// itself is broken.
func (p ProtectedIntegration) gitOpsEmbeddedAppPath(ctx context.Context) (string, error) {
	resolve := p.GitOpsEmbeddedResolver
	if resolve == nil {
		resolve = defaultGitOpsEmbeddedResolver
	}
	return resolve(ctx)
}

// defaultGitOpsEmbeddedResolver is the production embedded-library fallback:
// materialize the embedded story library (basestories.Materialize) and
// resolve its git-ops story — exactly the mechanism
// internal/capsule/storylauncher.Launcher and basestories.DefaultResolver use
// to satisfy an `@kitsoki/<name>` import when no on-disk kitsoki checkout is
// present (internal/app.resolveImportSource's tier 3). An unstaged binary
// (basestories.ErrNotStaged) or a staged library that simply lacks a git-ops
// story both mean "nothing to offer" ("", nil): the embedded fallback is a
// best-effort convenience, not a required dependency of every kitsoki build.
func defaultGitOpsEmbeddedResolver(ctx context.Context) (string, error) {
	root, err := basestories.Materialize(ctx)
	if err != nil {
		if errors.Is(err, basestories.ErrNotStaged) {
			return "", nil
		}
		return "", err
	}
	candidate := filepath.Join(root, "git-ops", "app.yaml")
	if _, statErr := os.Stat(candidate); statErr != nil {
		return "", nil
	}
	return candidate, nil
}

// unresolvedConflictPaths reports which of paths are still unmerged or carry
// conflict markers.
func unresolvedConflictPaths(ctx context.Context, instancePath string, paths []string) ([]string, error) {
	unmergedOut, err := gitOutput(ctx, instancePath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	unmerged := map[string]bool{}
	for _, line := range strings.Split(unmergedOut, "\n") {
		if line != "" {
			unmerged[line] = true
		}
	}
	var out []string
	for _, path := range paths {
		if !unmerged[path] {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(instancePath, filepath.FromSlash(path)))
		if err != nil {
			if os.IsNotExist(err) {
				out = append(out, path) // deleted-vs-modified: still needs a decision
				continue
			}
			return nil, err
		}
		text := string(raw)
		if strings.Contains(text, "<<<<<<<") || strings.Contains(text, ">>>>>>>") || strings.Contains(text, "\n=======") {
			out = append(out, path)
		}
	}
	return out, nil
}
