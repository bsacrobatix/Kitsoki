package queue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/headroom"
	capsuleproject "kitsoki/internal/capsule/project"
	"kitsoki/internal/capsule/reconcile"
	"kitsoki/internal/capsule/record"
)

// StagingIntegration is the local production adapter for a protected project.
// It never changes main itself: it creates a disposable managed workspace,
// merges the candidate there, and delegates staging/local updates to the
// existing development-workspace lifecycle script. Final main promotion stays
// an explicit staging-capsule operation.
type StagingIntegration struct {
	ProjectRoot string
	QueueRoot   string
	GateCommand string
	Runner      CommandRunner
	Headroom    headroom.Guard
}

// ProtectedIntegration prepares the exact tree that will be compare-and-swap
// promoted to the protected source target. It deliberately retains its managed
// workspace after queue admission: the promotion reconciler consumes that tree
// and Capsule-local branch refs never become the source of truth.
//
// Divergent histories are handed to reconcile, which persists a continuation
// artifact and integration checkout. ResolverCommand is an optional bounded
// command supplied by project policy; without it the candidate remains in
// retry_wait with the continuation available for a later queue pass.
type ProtectedIntegration struct {
	ProjectRoot     string
	QueueRoot       string
	TargetRef       string
	DefinitionID    string
	ResolverCommand string
	Runner          CommandRunner
	Manager         *control.Manager
	// KitsokiBin overrides the binary used to launch the git-ops
	// conflict_resolver; the running executable is used when empty.
	KitsokiBin string
	Headroom   headroom.Guard
	// GitOpsEmbeddedResolver optionally resolves the git-ops
	// conflict_resolver's app.yaml from the embedded kitsoki story library —
	// consulted ONLY when the project carries no stories/git-ops/app.yaml of
	// its own (project-local always wins; see runGitOpsResolver in
	// resolver.go). Nil defaults to defaultGitOpsEmbeddedResolver, which
	// materializes basestories' embedded library, the same mechanism
	// internal/capsule/storylauncher.Launcher and basestories.DefaultResolver
	// use to satisfy an `@kitsoki/<name>` import with no on-disk kitsoki
	// checkout present. Tests inject a fake here instead of relying on the
	// real embedded library (which may not be staged into the test binary —
	// see basestories.ErrNotStaged) and to keep tests free of any real agent
	// launch.
	GitOpsEmbeddedResolver func(ctx context.Context) (string, error)
}

func (p ProtectedIntegration) Speculate(ctx context.Context, c Candidate, ahead []Candidate) (Speculation, error) {
	if c.TargetRef != p.targetRef() {
		return Speculation{}, fmt.Errorf("queue: protected integration target %q refuses candidate %s for target %q", p.targetRef(), c.ID, c.TargetRef)
	}
	root, err := p.root()
	if err != nil {
		return Speculation{}, err
	}
	if err := p.Headroom.Ensure(root); err != nil {
		return Speculation{}, Environmental(err)
	}
	def, err := p.definition(ctx, root)
	if err != nil {
		return Speculation{}, err
	}
	if def.Source.Kind != control.SourceDevWorkspaceScript {
		return p.speculateNative(ctx, root, def, c, ahead)
	}
	return p.speculateScript(ctx, root, c, ahead)
}

func (p ProtectedIntegration) speculateScript(ctx context.Context, root string, c Candidate, ahead []Candidate) (Speculation, error) {
	if _, err := os.Stat(filepath.Join(root, "scripts", "dev-workspace.sh")); err != nil {
		return Speculation{}, fmt.Errorf("queue: managed workspace lifecycle unavailable: %w", err)
	}
	target := p.targetRef()
	id := "queue-" + c.ID
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, id)
	branch := "queue/candidate/" + c.ID

	// Train stacking: base this candidate's workspace on the closest active
	// candidate ahead's already-speculated tree instead of always on its
	// own commit (which is itself based on whatever target looked like at
	// admission time). This is what turns per-candidate gates from mutually
	// exclusive into additive — when the predecessor lands, its tree
	// becomes the new target, and this candidate's own base already is
	// that tree, so finalize's ordinary fresh re-Plan/CAS check passes
	// without a reprepare instead of unconditionally going stale. A missing
	// or unfetchable predecessor tree just means there is nothing to stack
	// onto yet; that is not an error, it is every candidate's situation
	// today.
	createBase, stackedOn := c.SHA, ""
	if c.SourceAnchorID != "" {
		// External worker objects live only in the queue-private bare
		// repository. Create the managed workspace from the protected target;
		// the exact candidate is fetched into that disposable workspace below.
		createBase = target
	}
	if stackTree, predID, predWorkspace := stackBaseFor(ahead); stackTree != "" {
		if _, err := gitOutput(ctx, root, "fetch", "--no-tags", "--no-write-fetch-head", predWorkspace, stackTree); err == nil {
			createBase, stackedOn = stackTree, predID
		}
	}
	if err := p.run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), "create", "--repo", root, "--root", workspaceRoot, "--id", id, "--branch", branch, "--base", createBase, "--target", target); err != nil {
		return Speculation{}, Environmental(err)
	}
	if err := (Store{ProjectRoot: root, QueueRoot: p.QueueRoot}).materializeExternalCandidate(ctx, workspace, c); err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	runtimeConfig, err := refreshPreparationLocalConfig(root, workspace)
	if err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(fmt.Errorf("queue: refresh preparation local config: %w", err))
	}
	var stackEvidence []string
	if stackedOn != "" {
		if c.SourceAnchorID == "" {
			if _, err := gitOutput(ctx, workspace, "fetch", "--no-tags", "--no-write-fetch-head", root, c.SHA); err != nil {
				return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
			}
		}
		if err := p.run(ctx, workspace, "git", "merge", "--no-ff", "--no-edit", c.SHA); err != nil {
			// The predecessor's changes conflict with this candidate's own
			// changes. This is content, not infrastructure, but it is also
			// not this candidate's own fault — retrying the identical merge
			// would fail identically every time, so failing the candidate
			// (or routing it through the target-divergence conflict
			// machinery, which is shaped for candidate-vs-target divergence
			// with receipt/gate identity considerations that don't apply
			// here) would be wrong. Fall back to the always-safe unstacked
			// base instead: abort the merge and re-point the branch
			// directly at the candidate's own commit, exactly the
			// pre-stacking behavior. Losing this round's stacking headroom
			// is always safe; failing over a conflict with an unrelated
			// candidate it merely happened to queue behind is not.
			if _, abortErr := gitOutput(ctx, workspace, "merge", "--abort"); abortErr != nil {
				return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(fmt.Errorf("queue: abort failed stack merge onto %s: %w", stackedOn, abortErr))
			}
			if _, err := gitOutput(ctx, workspace, "checkout", "-B", branch, c.SHA); err != nil {
				return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
			}
			stackEvidence = []string{"queue:stack-conflict-with=" + stackedOn + " fell-back-to-unstacked"}
		} else {
			stackEvidence = []string{"queue:stacked-on=" + stackedOn}
		}
	} else if c.SourceAnchorID != "" {
		if _, err := gitOutput(ctx, workspace, "checkout", "-B", branch, c.SHA); err != nil {
			return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
		}
	}
	plan, err := (reconcile.Reconciler{VCS: reconcile.Git{}}).Plan(ctx, reconcile.PlanRequest{
		Workspace: workspace, ProtectedProjectRoot: root, TargetRef: target, Operation: reconcile.Promote,
	})
	if err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	spec := Speculation{
		SHA: plan.Candidate, BaseSHA: plan.Expected.Target,
		RuntimeConfigDigest: runtimeConfig.Digest,
		WorkspaceID:         id, WorkspacePath: workspace,
		Evidence: append([]string{"queue:reconcile-plan=" + plan.Digest, runtimeConfig.evidence()}, stackEvidence...),
	}
	if plan.Continuation == nil {
		if plan.Class != reconcile.LocalAhead && plan.Class != reconcile.UpToDate {
			return spec, fmt.Errorf("queue: protected integration is %s", plan.Class)
		}
		return spec, nil
	}
	reconciler := reconcile.Reconciler{VCS: reconcile.Git{}, Headroom: p.Headroom}
	artifact, artifactPath, err := reconciler.MaterializeConflictArtifact(ctx, plan, root)
	if err != nil {
		return spec, Environmental(err)
	}
	instance, instanceArtifact, err := reconciler.MaterializeIntegrationInstance(ctx, plan, root)
	if err != nil {
		return spec, Environmental(err)
	}
	instancePath := filepath.Join(root, filepath.FromSlash(instance.InstancePath))
	if _, err := installLocalConfigSnapshot(root, instancePath, runtimeConfig); err != nil {
		return spec, Environmental(fmt.Errorf("queue: propagate preparation local config to continuation: %w", err))
	}
	spec.Evidence = append(spec.Evidence, "queue:local-config-propagated-to-continuation digest="+runtimeConfig.Digest)
	spec.WorkspaceID = artifact.ContinuationToken
	spec.WorkspacePath = instancePath
	spec.Evidence = append(spec.Evidence,
		"queue:continuation="+artifact.ContinuationToken,
		"queue:conflict-artifact="+relativePath(root, artifactPath),
		"queue:integration-artifact="+relativePath(root, instanceArtifact),
	)
	if len(instance.ConflictPaths) == 0 {
		// Divergent histories whose changes are disjoint merge cleanly; the
		// train continues without any resolver. Only the commit is missing.
		if _, err := gitOutput(ctx, instancePath, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
			if _, err := gitOutput(ctx, instancePath, "-c", "user.name=kitsoki-queue", "-c", "user.email=queue@kitsoki.invalid", "commit", "--no-edit"); err != nil {
				return spec, err
			}
			spec.Evidence = append(spec.Evidence, "queue:disjoint-histories-merged-automatically")
		}
	} else {
		evidence, resolveErr := p.resolveConflicts(ctx, root, instancePath, artifact.ContinuationToken, instance.ConflictPaths)
		spec.Evidence = append(spec.Evidence, evidence...)
		if resolveErr != nil {
			return spec, resolveErr
		}
	}
	status, err := gitOutput(ctx, instancePath, "status", "--porcelain")
	if err != nil {
		return spec, err
	}
	if status != "" {
		return spec, fmt.Errorf("queue: resolver unavailable or incomplete; continuation %s is retained", artifact.ContinuationToken)
	}
	sha, err := gitOutput(ctx, instancePath, "rev-parse", "HEAD")
	if err != nil {
		return spec, err
	}
	spec.SHA = sha
	return spec, nil
}

func (p ProtectedIntegration) speculateNative(ctx context.Context, root string, def control.Definition, c Candidate, ahead []Candidate) (Speculation, error) {
	target := p.targetRef()
	id := "queue-" + c.ID
	branch := "queue/candidate/" + c.ID
	createBase, stackedOn := c.SHA, ""
	if c.SourceAnchorID != "" {
		createBase = target
	}
	if stackTree, predID, predWorkspace := stackBaseFor(ahead); stackTree != "" {
		if _, err := gitOutput(ctx, root, "fetch", "--no-tags", "--no-write-fetch-head", predWorkspace, stackTree); err == nil {
			createBase, stackedOn = stackTree, predID
		}
	}
	manager, err := p.manager(root)
	if err != nil {
		return Speculation{}, err
	}
	handle, err := manager.Create(ctx, control.CreateRequest{ID: id, DefinitionID: def.ID, Owner: "queue"})
	if err != nil {
		return Speculation{}, Environmental(err)
	}
	workspace, err := manager.WorkspacePath(ctx, handle)
	if err != nil {
		return Speculation{WorkspaceID: id}, Environmental(err)
	}
	_, _ = gitOutput(ctx, workspace, "merge", "--abort")
	if _, err := gitOutput(ctx, workspace, "reset", "--hard"); err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	if _, err := gitOutput(ctx, workspace, "clean", "-fd"); err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	if _, err := gitOutput(ctx, workspace, "fetch", "--no-tags", root, createBase); err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	if _, err := gitOutput(ctx, workspace, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	if err := (Store{ProjectRoot: root, QueueRoot: p.QueueRoot}).materializeExternalCandidate(ctx, workspace, c); err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	runtimeConfig, err := refreshPreparationLocalConfig(root, workspace)
	if err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(fmt.Errorf("queue: refresh preparation local config: %w", err))
	}
	var stackEvidence []string
	if stackedOn != "" {
		if c.SourceAnchorID == "" {
			if _, err := gitOutput(ctx, workspace, "fetch", "--no-tags", "--no-write-fetch-head", root, c.SHA); err != nil {
				return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
			}
		}
		if err := p.run(ctx, workspace, "git", "merge", "--no-ff", "--no-edit", c.SHA); err != nil {
			if _, abortErr := gitOutput(ctx, workspace, "merge", "--abort"); abortErr != nil {
				return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(fmt.Errorf("queue: abort failed stack merge onto %s: %w", stackedOn, abortErr))
			}
			if _, err := gitOutput(ctx, workspace, "checkout", "-B", branch, c.SHA); err != nil {
				return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
			}
			stackEvidence = []string{"queue:stack-conflict-with=" + stackedOn + " fell-back-to-unstacked"}
		} else {
			stackEvidence = []string{"queue:stacked-on=" + stackedOn}
		}
	} else if c.SourceAnchorID != "" {
		if _, err := gitOutput(ctx, workspace, "checkout", "-B", branch, c.SHA); err != nil {
			return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
		}
	}
	plan, err := (reconcile.Reconciler{VCS: reconcile.Git{}}).Plan(ctx, reconcile.PlanRequest{
		Workspace: workspace, ProtectedProjectRoot: root, TargetRef: target, Operation: reconcile.Promote,
	})
	if err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	spec := Speculation{
		SHA: plan.Candidate, BaseSHA: plan.Expected.Target,
		RuntimeConfigDigest: runtimeConfig.Digest,
		WorkspaceID:         id, WorkspacePath: workspace,
		Evidence: append([]string{"queue:native-capsule-definition=" + def.ID, "queue:reconcile-plan=" + plan.Digest, runtimeConfig.evidence()}, stackEvidence...),
	}
	if plan.Continuation == nil {
		if plan.Class != reconcile.LocalAhead && plan.Class != reconcile.UpToDate {
			return spec, fmt.Errorf("queue: protected integration is %s", plan.Class)
		}
		return spec, nil
	}
	reconciler := reconcile.Reconciler{VCS: reconcile.Git{}, Headroom: p.Headroom}
	artifact, artifactPath, err := reconciler.MaterializeConflictArtifact(ctx, plan, root)
	if err != nil {
		return spec, Environmental(err)
	}
	instance, instanceArtifact, err := reconciler.MaterializeIntegrationInstance(ctx, plan, root)
	if err != nil {
		return spec, Environmental(err)
	}
	instancePath := filepath.Join(root, filepath.FromSlash(instance.InstancePath))
	if _, err := installLocalConfigSnapshot(root, instancePath, runtimeConfig); err != nil {
		return spec, Environmental(fmt.Errorf("queue: propagate preparation local config to continuation: %w", err))
	}
	spec.Evidence = append(spec.Evidence, "queue:local-config-propagated-to-continuation digest="+runtimeConfig.Digest)
	spec.WorkspaceID = artifact.ContinuationToken
	spec.WorkspacePath = instancePath
	spec.Evidence = append(spec.Evidence,
		"queue:continuation="+artifact.ContinuationToken,
		"queue:conflict-artifact="+relativePath(root, artifactPath),
		"queue:integration-artifact="+relativePath(root, instanceArtifact),
	)
	if len(instance.ConflictPaths) == 0 {
		if _, err := gitOutput(ctx, instancePath, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
			if _, err := gitOutput(ctx, instancePath, "-c", "user.name=kitsoki-queue", "-c", "user.email=queue@kitsoki.invalid", "commit", "--no-edit"); err != nil {
				return spec, err
			}
			spec.Evidence = append(spec.Evidence, "queue:disjoint-histories-merged-automatically")
		}
	} else {
		evidence, resolveErr := p.resolveConflicts(ctx, root, instancePath, artifact.ContinuationToken, instance.ConflictPaths)
		spec.Evidence = append(spec.Evidence, evidence...)
		if resolveErr != nil {
			return spec, resolveErr
		}
	}
	status, err := gitOutput(ctx, instancePath, "status", "--porcelain")
	if err != nil {
		return spec, err
	}
	if status != "" {
		return spec, fmt.Errorf("queue: resolver unavailable or incomplete; continuation %s is retained", artifact.ContinuationToken)
	}
	sha, err := gitOutput(ctx, instancePath, "rev-parse", "HEAD")
	if err != nil {
		return spec, err
	}
	spec.SHA = sha
	return spec, nil
}

// localConfigSnapshot is the exact machine-local runtime policy used by one
// preparation. It is installed into the ordinary managed workspace first and
// then carried to any reconcile continuation; the live protected checkout is
// never re-read midway through a preparation.
type localConfigSnapshot struct {
	Present bool
	Data    []byte
	Mode    os.FileMode
	Digest  string
}

func (s localConfigSnapshot) evidence() string {
	return fmt.Sprintf("queue:runtime-config present=%t digest=%s", s.Present, s.Digest)
}

func refreshPreparationLocalConfig(root, workspace string) (localConfigSnapshot, error) {
	snapshot, err := readLocalConfigSnapshotWithHook(root, nil)
	if err != nil {
		return localConfigSnapshot{}, err
	}
	if _, err := installLocalConfigSnapshot(root, workspace, snapshot); err != nil {
		return localConfigSnapshot{}, err
	}
	installed, err := readLocalConfigSnapshotWithHook(workspace, nil)
	if err != nil {
		return localConfigSnapshot{}, err
	}
	if installed.Digest != snapshot.Digest {
		return localConfigSnapshot{}, fmt.Errorf("preparation local config does not match captured runtime policy")
	}
	return installed, nil
}

func readLocalConfigSnapshotWithHook(sourceRoot string, afterOpen func()) (localConfigSnapshot, error) {
	source := filepath.Join(sourceRoot, ".kitsoki.local.yaml")
	linkInfo, err := os.Lstat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return localConfigSnapshot{Digest: fingerprint("local-config/absent/v1")}, nil
		}
		return localConfigSnapshot{}, err
	}
	if !linkInfo.Mode().IsRegular() {
		return localConfigSnapshot{}, fmt.Errorf("local config must be a regular file, got mode %s", linkInfo.Mode())
	}
	input, err := os.Open(source)
	if err != nil {
		return localConfigSnapshot{}, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return localConfigSnapshot{}, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(linkInfo, info) {
		return localConfigSnapshot{}, fmt.Errorf("local config changed identity while opening")
	}
	if afterOpen != nil {
		afterOpen()
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return localConfigSnapshot{}, err
	}
	after, err := input.Stat()
	if err != nil {
		return localConfigSnapshot{}, err
	}
	if info.Size() != after.Size() || info.Mode() != after.Mode() || !info.ModTime().Equal(after.ModTime()) {
		return localConfigSnapshot{}, fmt.Errorf("local config changed while snapshotting")
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return localConfigSnapshot{}, err
	}
	confirmation, err := io.ReadAll(input)
	if err != nil {
		return localConfigSnapshot{}, err
	}
	if !bytes.Equal(data, confirmation) {
		return localConfigSnapshot{}, fmt.Errorf("local config changed while snapshotting")
	}
	mode := info.Mode().Perm()
	return localConfigSnapshot{
		Present: true,
		Data:    append([]byte(nil), data...),
		Mode:    mode,
		Digest:  fingerprint("local-config/present/v1", fmt.Sprintf("%#o", mode), string(data)),
	}, nil
}

func installLocalConfigSnapshot(projectRoot, destination string, snapshot localConfigSnapshot) (bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return false, err
	}
	resolvedDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		return false, err
	}
	relativeDestination, err := filepath.Rel(resolvedRoot, resolvedDestination)
	if err != nil || relativeDestination == "." || relativeDestination == ".." || strings.HasPrefix(relativeDestination, ".."+string(os.PathSeparator)) {
		return false, fmt.Errorf("local config destination escapes protected project root")
	}
	target := filepath.Join(destination, ".kitsoki.local.yaml")
	targetInfo, targetErr := os.Lstat(target)
	if targetErr != nil && !os.IsNotExist(targetErr) {
		return false, targetErr
	}
	if targetErr == nil && !targetInfo.Mode().IsRegular() {
		return false, fmt.Errorf("local config target must be a regular file or absent, got mode %s", targetInfo.Mode())
	}
	if !snapshot.Present {
		if os.IsNotExist(targetErr) {
			return false, nil
		}
		if err := os.Remove(target); err != nil {
			return false, err
		}
		if err := syncDirectory(destination); err != nil {
			return false, err
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return false, fmt.Errorf("stale local config remained after removal")
		}
		return true, nil
	}
	temp, err := os.CreateTemp(destination, ".kitsoki.local.yaml.tmp-")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(snapshot.Data); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Chmod(snapshot.Mode.Perm()); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return false, err
	}
	if err := syncDirectory(destination); err != nil {
		return false, err
	}
	installed, err := readLocalConfigSnapshotWithHook(destination, nil)
	if err != nil {
		return false, err
	}
	if installed.Digest != snapshot.Digest || installed.Mode.Perm() != snapshot.Mode.Perm() || !bytes.Equal(installed.Data, snapshot.Data) {
		return false, fmt.Errorf("installed local config failed exact verification")
	}
	return true, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// copyProtectedLocalConfigWithHook remains a narrow test seam for exercising
// the snapshot race. Production prepares once through
// refreshPreparationLocalConfig and carries that returned snapshot forward.
func copyProtectedLocalConfig(root, destination string) (bool, error) {
	return copyProtectedLocalConfigWithHook(root, destination, nil)
}

func copyProtectedLocalConfigWithHook(root, destination string, afterOpen func()) (bool, error) {
	snapshot, err := readLocalConfigSnapshotWithHook(root, afterOpen)
	if err != nil {
		return false, err
	}
	return installLocalConfigSnapshot(root, destination, snapshot)
}

func (p ProtectedIntegration) Land(context.Context, Speculation) error { return nil }

// stackBaseFor picks the closest active candidate ahead whose speculative
// tree and workspace are both already known, so a candidate can chain onto
// it instead of always speculating against whatever the target looked like
// at its own admission time. ahead is not assumed sorted; "closest" means
// highest Sequence — the most recently admitted candidate with a usable
// tree, i.e. nearest in the train to the one asking. A candidate still in
// its very first Preparing pass (no TreeSHA yet) or whose workspace has
// since gone away is simply skipped, never an error: there is nothing to
// stack onto yet, which is every candidate's situation before this existed.
func stackBaseFor(ahead []Candidate) (treeSHA, id, workspacePath string) {
	var best *Candidate
	for i := range ahead {
		c := &ahead[i]
		if c.TreeSHA == "" || c.WorkspacePath == "" {
			continue
		}
		if best == nil || c.Sequence > best.Sequence {
			best = c
		}
	}
	if best == nil {
		return "", "", ""
	}
	return best.TreeSHA, best.ID, best.WorkspacePath
}

// ProtectedFinalizer is the only queue adapter that mutates a protected ref.
// It repeats the identity checks immediately before reconcile.Apply, whose git
// update-ref expected-old argument provides the protected compare-and-swap.
type ProtectedFinalizer struct {
	ProjectRoot string
	TargetRef   string
	QueueRoot   string
	// SkipWIPPreservation disables the dirty-checkout capture; only tests
	// exercising the CAS in isolation should set it.
	SkipWIPPreservation bool
}

func (p ProtectedFinalizer) Finalize(ctx context.Context, c Candidate) (FinalizeResult, error) {
	if err := validatePreparedTuple(c); err != nil {
		return FinalizeResult{}, err
	}
	target := p.targetRef()
	if c.TargetRef != target {
		return FinalizeResult{}, fmt.Errorf("queue: protected finalizer target %q refuses candidate %s for target %q", target, c.ID, c.TargetRef)
	}
	// A dirty protected checkout never blocks the train and never loses data:
	// the work is captured on an immutable preserved-WIP branch and the
	// checkout restored clean before the ref CAS.
	var preserved string
	var wipSkipped []string
	var checkoutWarning string
	projectionPending := false
	if !p.SkipWIPPreservation {
		projection, reusable, projectionErr := p.reusableProjection(ctx, target)
		if projectionErr != nil {
			return FinalizeResult{}, Environmental(fmt.Errorf("queue: inspect pending checkout projection: %w", projectionErr))
		}
		if reusable {
			preserved = projection.PreservedBranch
			projectionPending = true
			checkoutWarning = "protected checkout still matches its pending projection; skipped duplicate WIP capture"
		} else {
			var err error
			preserved, wipSkipped, err = PreserveWIP(ctx, p.ProjectRoot, time.Now().UTC())
			if err != nil {
				// PreserveWIP publishes the immutable preservation branch before it
				// attempts to restore the protected checkout. Once that branch
				// exists, a restore failure (most commonly a 0444 primary checkout)
				// is checkout hygiene, not a reason to block the protected-ref CAS:
				// the bytes are durable and the checkout is still carrying them.
				//
				// If capture itself failed and no branch exists, retain the old
				// fail-closed behavior because advancing the ref could then make
				// unanchored work harder to recover.
				var restoreErr *PreservedWIPRestoreError
				if preserved == "" || !errors.As(err, &restoreErr) {
					return FinalizeResult{}, Environmental(fmt.Errorf("queue: protected checkout WIP preservation: %w", err))
				}
				projectionPending = true
				checkoutWarning = fmt.Sprintf("protected checkout restore failed after durable WIP capture on %s: %v", preserved, err)
			}
		}
	}
	planRequest := reconcile.PlanRequest{
		Workspace: c.WorkspacePath, ProtectedProjectRoot: p.ProjectRoot, TargetRef: target,
		Operation: reconcile.Promote,
	}
	if c.admission() == ReceiptAdmission {
		planRequest.ReceiptCandidate = c.SHA
		planRequest.RequiredGate = c.GateVersion
	}
	plan, err := (reconcile.Reconciler{VCS: reconcile.Git{}}).Plan(ctx, planRequest)
	if err != nil {
		return FinalizeResult{}, Environmental(err)
	}
	if plan.Expected.Target != c.BaseSHA {
		// The protected target moved since this candidate's base was
		// observed at speculation time. That is not automatically stale:
		// plan.Class was just freshly computed against the live target, and
		// if it is still LocalAhead/UpToDate, this candidate's prepared
		// tree already contains the new target as an ancestor — exactly
		// what happens when a candidate was chained onto the predecessor
		// that just landed (train stacking, see Speculate) rather than
		// waiting for it. Landing is still a plain fast-forward from the
		// new target and the deterministic gate already validated this
		// exact tree (checked next); only a target that moved to something
		// this tree does NOT already contain is genuine staleness.
		if plan.Class != reconcile.LocalAhead && plan.Class != reconcile.UpToDate {
			return FinalizeResult{OldMainSHA: plan.Expected.Target, Stale: true, Log: "prepared base no longer matches protected target"}, nil
		}
	}
	if plan.Candidate != c.TreeSHA || c.ValidatedSHA != c.TreeSHA {
		return FinalizeResult{}, fmt.Errorf("queue: prepared tree changed after deterministic gate")
	}
	reconciler := reconcile.Reconciler{VCS: reconcile.Git{}}
	if c.admission() == ReceiptAdmission {
		reconciler.Gates = record.PromotionGate{ProjectRoot: p.ProjectRoot, ReceiptRef: c.ReceiptRef, RunRecordRef: c.RunRecordRef}
	}
	result, err := reconciler.Apply(ctx, plan, c.ReceiptID)
	if err != nil {
		if strings.Contains(err.Error(), "stale plan") {
			return FinalizeResult{OldMainSHA: plan.Expected.Target, Stale: true, Log: err.Error()}, nil
		}
		// Apply's failure modes mix transient CAS/lock contention with
		// genuine policy rejection (e.g. record.PromotionGate refusing a
		// receipt) — the two need very different retry treatment and Apply
		// does not currently distinguish them, so this stays product-
		// classified (bounded attempts) rather than risk giving a real
		// policy rejection hours of environmental retry noise.
		return FinalizeResult{}, err
	}
	log := "protected CAS applied"
	if preserved != "" {
		log += "; preserved protected-checkout WIP on " + preserved
	}
	if len(wipSkipped) > 0 {
		log += fmt.Sprintf("; %d path(s) could not be read and were left untouched in the checkout: %s", len(wipSkipped), strings.Join(wipSkipped, ", "))
	}
	if checkoutWarning != "" {
		log += "; " + checkoutWarning
	}
	// The CAS only moves the ref; when the target branch is the protected
	// checkout's HEAD the worktree must follow it, or the old tree lingers as
	// a staged reversal of the commit that just landed.
	// A failed restore means the checkout deliberately still carries local
	// bytes. Do not hard-reset it after the CAS; the protected ref is already
	// authoritative and the preserved branch is the durable recovery point.
	if !projectionPending {
		summary, syncErr := syncProtectedCheckout(ctx, p.ProjectRoot, target, result.OldTarget)
		if syncErr != nil {
			projectionPending = true
			log += "; checkout sync failed: " + syncErr.Error()
		} else if summary != "" {
			// Successful and deliberately skipped syncs are both evidence:
			// retaining the summary keeps them distinguishable.
			log += "; " + summary
		}
	}
	if projectionPending {
		if projectionErr := p.writeProjection(ctx, target, result.NewTarget, preserved); projectionErr != nil {
			// The ref CAS already won and is the authority. Never report the
			// candidate as unlanded; retain a loud diagnostic for reconciliation.
			log += "; checkout projection persistence failed: " + projectionErr.Error()
		} else {
			log += "; checkout projection retained for the next finalizer"
		}
	} else {
		p.clearProjection(target)
	}
	return FinalizeResult{OldMainSHA: result.OldTarget, NewMainSHA: result.NewTarget, Log: log, PreservedWIPBranch: preserved}, nil
}

func (p ProtectedIntegration) targetRef() string {
	if strings.TrimSpace(p.TargetRef) == "" {
		return "main"
	}
	return strings.TrimSpace(p.TargetRef)
}

func (p ProtectedFinalizer) targetRef() string {
	if strings.TrimSpace(p.TargetRef) == "" {
		return "main"
	}
	return strings.TrimSpace(p.TargetRef)
}

func (p ProtectedIntegration) root() (string, error) {
	root, err := filepath.Abs(p.ProjectRoot)
	if err != nil {
		return "", err
	}
	return root, nil
}

func (p ProtectedIntegration) definitionID() string {
	if strings.TrimSpace(p.DefinitionID) == "" {
		return "development"
	}
	return strings.TrimSpace(p.DefinitionID)
}

func (p ProtectedIntegration) definition(ctx context.Context, root string) (control.Definition, error) {
	def, err := (control.FileDefinitionStore{ProjectRoot: root}).Get(ctx, p.definitionID())
	if err != nil {
		return control.Definition{}, fmt.Errorf("queue: load capsule definition %q: %w", p.definitionID(), err)
	}
	switch def.Source.Kind {
	case control.SourceSelf, control.SourcePinned, control.SourceDevWorkspaceScript:
		return def, nil
	default:
		return control.Definition{}, fmt.Errorf("queue: protected integration does not support capsule source kind %q", def.Source.Kind)
	}
}

func (p ProtectedIntegration) manager(root string) (*control.Manager, error) {
	if p.Manager != nil {
		return p.Manager, nil
	}
	return capsuleproject.Open(root, []string{p.targetRef()})
}

func (p ProtectedIntegration) run(ctx context.Context, dir, program string, args ...string) error {
	output, err := p.runner().Run(ctx, dir, program, args...)
	if err != nil {
		return fmt.Errorf("queue: lifecycle %s: %w%s", filepath.Base(program), err, outputSuffix(output))
	}
	return nil
}

func (p ProtectedIntegration) runner() CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return execCommandRunner{}
}

// ShellRepairer is intentionally opt-in. The caller sets a project-owned
// repair profile; failure merely preserves the queue entry for the next pass.
type ShellRepairer struct {
	Command string
	Runner  CommandRunner
}

func (r ShellRepairer) Repair(ctx context.Context, spec Speculation, _ error) ([]string, error) {
	if strings.TrimSpace(r.Command) == "" {
		return nil, fmt.Errorf("repair profile is unavailable")
	}
	if strings.TrimSpace(spec.WorkspacePath) == "" {
		return nil, fmt.Errorf("repair workspace is unavailable")
	}
	runner := r.Runner
	if runner == nil {
		runner = scrubbedShellRunner{}
	}
	output, err := runner.Run(ctx, spec.WorkspacePath, "sh", "-c", r.Command)
	return commandEvidence("queue:repair", output), err
}

type ShellRepairReviewer struct {
	Command, ReviewerID string
	Runner              CommandRunner
}

func (r ShellRepairReviewer) Review(ctx context.Context, in RepairReview) (RepairReviewResult, error) {
	if strings.TrimSpace(r.Command) == "" || strings.TrimSpace(r.ReviewerID) == "" {
		return RepairReviewResult{}, fmt.Errorf("repair review command and reviewer identity are required")
	}
	if strings.TrimSpace(in.After.WorkspacePath) == "" {
		return RepairReviewResult{}, fmt.Errorf("repair review workspace is unavailable")
	}
	runner := r.Runner
	if runner == nil {
		runner = scrubbedShellRunner{}
	}
	output, err := runner.Run(ctx, in.After.WorkspacePath, "sh", "-c", r.Command, "--", in.Before.SHA, in.After.SHA)
	return RepairReviewResult{
		Passed: err == nil, ReviewerID: r.ReviewerID,
		Evidence: commandEvidence("queue:repair-review", output),
	}, err
}

// workspaceHead reads the current HEAD of a speculative workspace, used to
// refresh the durable tree identity after a repairer commits into it. A
// backend whose speculation carries no git workspace (empty path, or a path
// that is not a git worktree — stub and remote integrations) reports no
// identity change rather than an error.
func workspaceHead(ctx context.Context, workspacePath string) (string, error) {
	if strings.TrimSpace(workspacePath) == "" {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(workspacePath, ".git")); err != nil {
		return "", nil
	}
	out, err := gitOutput(ctx, workspacePath, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("queue: read repaired workspace HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func relativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// CommandRunner makes lifecycle composition testable without weakening the
// production path, which uses exec.CommandContext.
type CommandRunner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type CommandRunnerFunc func(context.Context, string, string, ...string) ([]byte, error)

func (f CommandRunnerFunc) Run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	return f(ctx, dir, program, args...)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// scrubbedShellRunner preserves the ordinary toolchain environment while
// removing provider/cloud credentials from untrusted gate, repair, and review
// subprocesses. Lifecycle git operations keep using execCommandRunner.
type scrubbedShellRunner struct{}

func (scrubbedShellRunner) Run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL") ||
			strings.Contains(upper, "API_KEY") || strings.Contains(upper, "PRIVATE_KEY") ||
			strings.Contains(upper, "ACCESS_KEY") || strings.Contains(upper, "KEY_ID") ||
			strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "AZURE_") ||
			strings.HasPrefix(upper, "GOOGLE_") || strings.HasPrefix(upper, "S3_") ||
			strings.HasPrefix(upper, "DO_") || strings.HasPrefix(upper, "KITSOKI_WORKER_OUTPUTS_") {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	if tier, _ := ctx.Value(gateTierContextKey{}).(string); strings.TrimSpace(tier) != "" {
		cmd.Env = setEnv(cmd.Env, "KITSOKI_GATE_TIER", strings.TrimSpace(tier))
	}
	if lease := gateCapacityLeaseFromContext(ctx); lease != nil {
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		err := lease.RunCommand(ctx, cmd)
		return output.Bytes(), err
	}
	return cmd.CombinedOutput()
}

type gateTierContextKey struct{}

func (s StagingIntegration) Speculate(ctx context.Context, c Candidate, ahead []Candidate) (Speculation, error) {
	root, err := s.root()
	if err != nil {
		return Speculation{}, err
	}
	if err := s.Headroom.Ensure(root); err != nil {
		return Speculation{}, Environmental(err)
	}
	id := "queue-" + c.ID
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, id)
	branch := "queue/speculative/" + c.ID

	// Train stacking: see the comment in ProtectedIntegration.Speculate.
	// Here the merge step already exists unconditionally, so stacking is
	// just choosing what to base the workspace on before it.
	createBase, stackedOn := "staging/local", ""
	if stackTree, predID, predWorkspace := stackBaseFor(ahead); stackTree != "" {
		if _, err := gitOutput(ctx, root, "fetch", "--no-tags", "--no-write-fetch-head", predWorkspace, stackTree); err == nil {
			createBase, stackedOn = stackTree, predID
		}
	}
	if err := s.run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), "create", "--repo", root, "--root", workspaceRoot, "--id", id, "--branch", branch, "--base", createBase, "--target", "staging/local"); err != nil {
		// Workspace creation is infrastructure, not a verdict on the
		// candidate: classify environmental so a transient create race burns
		// the lenient env-retry budget, matching ProtectedIntegration.
		return Speculation{}, Environmental(err)
	}
	if err := (Store{ProjectRoot: root, QueueRoot: s.QueueRoot}).materializeExternalCandidate(ctx, workspace, c); err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(err)
	}
	runtimeConfig, err := refreshPreparationLocalConfig(root, workspace)
	if err != nil {
		return Speculation{WorkspaceID: id, WorkspacePath: workspace}, Environmental(fmt.Errorf("queue: refresh preparation local config: %w", err))
	}
	var stackEvidence []string
	if err := s.run(ctx, workspace, "git", "merge", "--no-ff", "--no-edit", c.SHA); err != nil {
		if stackedOn == "" {
			return Speculation{}, fmt.Errorf("queue: speculative merge %s: %w", c.SHA, err)
		}
		// The predecessor's changes conflict with this candidate's own —
		// not this candidate's fault, and retrying the identical merge
		// would fail identically forever. Fall back to the always-safe
		// unstacked base: re-point the branch at the live target and merge
		// again, exactly the pre-stacking behavior.
		if _, abortErr := gitOutput(ctx, workspace, "merge", "--abort"); abortErr != nil {
			return Speculation{WorkspaceID: id, WorkspacePath: workspace}, fmt.Errorf("queue: abort failed stack merge onto %s: %w", stackedOn, abortErr)
		}
		if _, err := gitOutput(ctx, workspace, "checkout", "-B", branch, "source/staging/local"); err != nil {
			return Speculation{WorkspaceID: id, WorkspacePath: workspace}, err
		}
		if err := s.run(ctx, workspace, "git", "merge", "--no-ff", "--no-edit", c.SHA); err != nil {
			return Speculation{WorkspaceID: id, WorkspacePath: workspace}, fmt.Errorf("queue: speculative merge %s: %w", c.SHA, err)
		}
		stackEvidence = []string{"queue:stack-conflict-with=" + stackedOn + " fell-back-to-unstacked"}
	} else if stackedOn != "" {
		stackEvidence = []string{"queue:stacked-on=" + stackedOn}
	}
	sha, err := gitOutput(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		return Speculation{}, err
	}
	base, err := gitOutput(ctx, workspace, "rev-parse", "HEAD^")
	if err != nil {
		return Speculation{}, err
	}
	evidence := append([]string{
		"queue:speculative-workspace=" + filepath.ToSlash(filepath.Join(".capsules", "workspaces", id)),
		runtimeConfig.evidence(),
	}, stackEvidence...)
	return Speculation{SHA: sha, BaseSHA: base, RuntimeConfigDigest: runtimeConfig.Digest, WorkspaceID: id, WorkspacePath: workspace, Evidence: evidence}, nil
}

// Land delegates staging/local mutation to the established protected
// dev-workspace lifecycle. The deterministic gate is intentionally repeated
// after the merge helper's rebase; main promotion remains a separate
// refresh-staging-local.sh / merge-to-main.sh operation.
func (s StagingIntegration) Land(ctx context.Context, spec Speculation) error {
	root, err := s.root()
	if err != nil {
		return err
	}
	if spec.WorkspaceID == "" || spec.WorkspacePath == "" {
		return fmt.Errorf("queue: staging integration requires a managed speculative workspace")
	}
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	if !sameResolvedPath(spec.WorkspacePath, filepath.Join(workspaceRoot, spec.WorkspaceID)) {
		return fmt.Errorf("queue: speculative workspace escapes managed workspace root")
	}
	if err := s.run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), "merge", "--repo", root, "--root", workspaceRoot, spec.WorkspaceID, "--gate", s.GateCommand, "--teardown"); err != nil {
		return err
	}
	return nil
}

// sameResolvedPath accepts release aliases only when both names resolve to
// the exact same existing path. Hosted release directories point their
// .capsules roots at one stable store, so persisted speculative paths from an
// older release remain valid without weakening workspace-ID confinement.
func sameResolvedPath(actual, expected string) bool {
	actualResolved, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return false
	}
	expectedResolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return false
	}
	return filepath.Clean(actualResolved) == filepath.Clean(expectedResolved)
}

// GateEnvExitCode is the reserved process exit status by which a deterministic
// shell gate declares its own failure environmental — the worker's toolchain,
// host state, or a dependency was unusable — rather than a verdict on the
// candidate's tree. It is sysexits.h's EX_TEMPFAIL ("temporary failure, the
// user is invited to retry"), which is exactly this contract.
//
// A shell gate cannot hand the worker a typed Go EnvError the way an in-process
// adapter can, so the exit status is its classification channel. Gate scripts
// should preflight the tools they need and exit GateEnvExitCode with a named
// message when one is absent, instead of letting "command not found" surface
// hundreds of seconds deep inside a suite where it is indistinguishable from a
// red test.
const GateEnvExitCode = 75

// classifyShellGateFailure maps a shell gate's process exit into the queue's
// failure taxonomy. Only the exit status is consulted — never the output text —
// so this stays a declared contract rather than a pattern-matching guess, in
// keeping with EnvError's rule that adapters classify at the exact operation
// that failed.
//
// Environmental statuses:
//
//   - GateEnvExitCode: the gate explicitly declared its environment unusable.
//   - 126 / 127: POSIX shell "found but not executable" / "command not found".
//     The shell never reached the program, so this cannot be a verdict on the
//     candidate's code.
//   - killed by a signal, or a shell-reported 128+N: the gate process died
//     rather than reporting. An OOM or resource kill (SIGKILL), a worker
//     shutdown or lease loss (SIGTERM), or a cancelled context says nothing
//     about the tree under test.
//
// Everything else — most importantly a plain exit 1 from a suite that ran and
// reported red — stays a product failure and consumes the bounded attempt
// budget.
//
// Every environmental verdict here is Immediate: none of these conditions clears
// by waiting, so the candidate parks at once with the cause named rather than
// rediscovering the same thing over a retry window. Misclassifying in either
// direction can never land red code: the caller keeps Passed=false regardless,
// so the only thing this decides is whether the candidate burns an attempt and
// how it is labelled.
func classifyShellGateFailure(err error) error {
	cause := shellGateFailureCause(err)
	if cause == "" {
		return err
	}
	return EnvironmentalImmediate(cause, err)
}

// shellGateFailureCause names the environmental condition behind a failed shell
// gate, or "" when the gate ran and reported a genuine red verdict.
func shellGateFailureCause(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// No process status at all: a runner-level failure (the shell could not
		// be started, a custom CommandRunner failed). Left as a product failure
		// so an unclassifiable gate never silently gains a different policy.
		return ""
	}
	code := exitErr.ExitCode()
	if code < 0 {
		// ExitCode reports -1 when the process was terminated by a signal. Which
		// signal killed the gate is the single most useful datum for diagnosing a
		// gate that died mid-suite, and was exactly what earlier evidence lacked.
		// The number, not the name, keeps this token stable across platforms and
		// identical to the shell-reported 128+N form below.
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return fmt.Sprintf("killed_signal_%d", int(status.Signal()))
		}
		return "killed_unknown_signal"
	}
	switch {
	case code == GateEnvExitCode:
		return "gate_declared_environment_unusable"
	case code == 126:
		return "command_not_executable"
	case code == 127:
		return "command_not_found"
	case code > 128:
		// A shell reports a signal-killed child as 128+N.
		return fmt.Sprintf("killed_signal_%d", code-128)
	}
	return ""
}

// shellGateExitEvidence records the gate's exit status, the class it was given,
// and the named cause, so `queue status` shows an operator why a candidate was
// parked as infrastructure rather than judged red.
func shellGateExitEvidence(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return "queue:gate:exit=unknown class=product"
	}
	cause := shellGateFailureCause(err)
	if cause == "" {
		return fmt.Sprintf("queue:gate:exit=%d class=product", exitErr.ExitCode())
	}
	return fmt.Sprintf("queue:gate:exit=%d class=environment cause=%s", exitErr.ExitCode(), cause)
}

// ShellGate runs the declared deterministic command only in the managed
// speculative workspace. It rejects a command that leaves that workspace dirty
// or moves HEAD, so a gate cannot smuggle unvalidated changes into staging.
type ShellGate struct {
	Command string
	Runner  CommandRunner
}

func (g ShellGate) Run(ctx context.Context, spec Speculation) (GateResult, error) {
	if strings.TrimSpace(g.Command) == "" {
		return GateResult{}, fmt.Errorf("queue: deterministic gate command is required")
	}
	if spec.WorkspacePath == "" {
		return GateResult{}, fmt.Errorf("queue: gate requires a speculative workspace")
	}
	before, err := gitOutput(ctx, spec.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return GateResult{}, err
	}
	runner := g.Runner
	if runner == nil {
		runner = scrubbedShellRunner{}
	}
	output, runErr := runner.Run(ctx, spec.WorkspacePath, "sh", "-c", g.Command)
	evidence := commandEvidence("queue:gate", output)
	if runErr != nil {
		wrapped := fmt.Errorf("queue: deterministic gate: %w", runErr)
		evidence = append(evidence, shellGateExitEvidence(wrapped))
		return GateResult{Passed: false, Evidence: evidence}, classifyShellGateFailure(wrapped)
	}
	after, err := gitOutput(ctx, spec.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return GateResult{Passed: false, Evidence: evidence}, err
	}
	if before != after {
		return GateResult{Passed: false, Evidence: evidence}, fmt.Errorf("queue: deterministic gate moved speculative HEAD (%s -> %s)", before, after)
	}
	dirty, err := gitOutput(ctx, spec.WorkspacePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return GateResult{Passed: false, Evidence: evidence}, err
	}
	for _, line := range strings.Split(dirty, "\n") {
		if line != "" && !strings.HasSuffix(line, ".kitsoki-capsule") && !strings.HasSuffix(line, ".kitsoki-clone") && !strings.HasSuffix(line, "capsule-manifest.json") && !strings.HasSuffix(line, ".kitsoki-dev-workspace.json") && !strings.HasSuffix(line, ".kitsoki-owner") {
			return GateResult{Passed: false, Evidence: evidence}, fmt.Errorf("queue: deterministic gate left speculative workspace dirty: %s", line)
		}
	}
	return GateResult{Passed: true, Evidence: evidence}, nil
}

func (s StagingIntegration) root() (string, error) {
	if strings.TrimSpace(s.GateCommand) == "" {
		return "", fmt.Errorf("queue: deterministic gate command is required")
	}
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "dev-workspace.sh")); err != nil {
		return "", fmt.Errorf("queue: managed workspace lifecycle unavailable: %w", err)
	}
	return root, nil
}

func (s StagingIntegration) run(ctx context.Context, dir, program string, args ...string) error {
	runner := s.Runner
	if runner == nil {
		runner = execCommandRunner{}
	}
	output, err := runner.Run(ctx, dir, program, args...)
	if err != nil {
		return fmt.Errorf("queue: lifecycle %s: %w%s", filepath.Base(program), err, outputSuffix(output))
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("queue: git %s: %w%s", strings.Join(args, " "), err, outputSuffix(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func commandEvidence(prefix string, output []byte) []string {
	text := strings.TrimSpace(string(output))
	if len(text) > 2048 {
		text = text[:2048] + "…"
	}
	if text == "" {
		return []string{prefix + ":passed"}
	}
	return []string{prefix + ":" + text}
}

func outputSuffix(output []byte) string {
	if text := strings.TrimSpace(string(output)); text != "" {
		return ": " + text
	}
	return ""
}
