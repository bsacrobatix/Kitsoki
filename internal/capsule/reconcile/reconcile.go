package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Operation string

const (
	Integrate Operation = "integrate"
	Refresh   Operation = "refresh"
	Promote   Operation = "promote"
	Publish   Operation = "publish"
)

type Class string

const (
	UpToDate    Class = "up_to_date"
	FastForward Class = "fast_forward"
	LocalAhead  Class = "local_ahead"
	RemoteAhead Class = "remote_ahead"
	Diverged    Class = "diverged"
	Dirty       Class = "dirty"
	Missing     Class = "missing"
)

type ObservedRefs struct {
	WorkspaceHead string `json:"workspace_head"`
	Target        string `json:"target"`
	Base          string `json:"base,omitempty"`
	Remote        string `json:"remote,omitempty"`
	Dirty         bool   `json:"dirty"`
	Generation    uint64 `json:"generation"`
}
type Plan struct {
	ID             string       `json:"id"`
	Digest         string       `json:"digest"`
	Operation      Operation    `json:"operation"`
	Class          Class        `json:"class"`
	Workspace      string       `json:"workspace"`
	TargetRef      string       `json:"target_ref"`
	Candidate      string       `json:"candidate"`
	Expected       ObservedRefs `json:"expected"`
	RequiredGate   string       `json:"required_gate,omitempty"`
	RequiredEffect string       `json:"required_effect"`
	CreatedAt      time.Time    `json:"created_at"`
}
type ApplyResult struct {
	PlanDigest string `json:"plan_digest"`
	OldTarget  string `json:"old_target"`
	NewTarget  string `json:"new_target"`
	Applied    bool   `json:"applied"`
}
type VCSProvider interface {
	Observe(context.Context, string, string, uint64) (ObservedRefs, error)
	IsAncestor(context.Context, string, string, string) (bool, error)
	UpdateRef(context.Context, string, string, string, string) error
}
type GateVerifier interface {
	Verify(context.Context, string, Plan) error
}
type GateVerifierFunc func(context.Context, string, Plan) error

func (f GateVerifierFunc) Verify(ctx context.Context, r string, p Plan) error { return f(ctx, r, p) }

type Reconciler struct {
	VCS   VCSProvider
	Gates GateVerifier
	Now   func() time.Time
}
type PlanRequest struct {
	Workspace    string
	TargetRef    string
	Operation    Operation
	Generation   uint64
	RequiredGate string
}

func (r Reconciler) Plan(ctx context.Context, req PlanRequest) (Plan, error) {
	if r.VCS == nil {
		return Plan{}, fmt.Errorf("capsule reconcile: vcs provider is required")
	}
	if req.Workspace == "" || req.TargetRef == "" {
		return Plan{}, fmt.Errorf("capsule reconcile: workspace and target are required")
	}
	if req.Operation == "" {
		return Plan{}, fmt.Errorf("capsule reconcile: operation is required")
	}
	if !ValidOperation(req.Operation) {
		return Plan{}, fmt.Errorf("capsule reconcile: unsupported operation %q", req.Operation)
	}
	observed, err := r.VCS.Observe(ctx, req.Workspace, req.TargetRef, req.Generation)
	if err != nil {
		return Plan{}, err
	}
	class, err := r.classify(ctx, req.Workspace, observed)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Operation: req.Operation, Class: class, Workspace: req.Workspace, TargetRef: req.TargetRef, Candidate: observed.WorkspaceHead, Expected: observed, RequiredGate: req.RequiredGate, RequiredEffect: effect(req.Operation), CreatedAt: r.now()}
	p.ID = "plan-" + shortHash([]byte(strings.Join([]string{string(p.Operation), p.Workspace, p.TargetRef, p.Candidate}, "\x00")))
	p.Digest = planDigest(p)
	return p, nil
}
func (r Reconciler) Apply(ctx context.Context, p Plan, gateReceipt string) (ApplyResult, error) {
	if r.VCS == nil {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: vcs provider is required")
	}
	if p.Digest == "" || p.Digest != planDigest(p) {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: invalid plan digest")
	}
	if p.Class == Dirty {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: dirty workspace cannot apply")
	}
	if p.Class == Diverged {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: diverged plan requires integration conflict continuation")
	}
	if p.RequiredGate != "" {
		if r.Gates == nil {
			return ApplyResult{}, fmt.Errorf("capsule reconcile: gate verifier is required")
		}
		if err := r.Gates.Verify(ctx, gateReceipt, p); err != nil {
			return ApplyResult{}, err
		}
	}
	current, err := r.VCS.Observe(ctx, p.Workspace, p.TargetRef, p.Expected.Generation)
	if err != nil {
		return ApplyResult{}, err
	}
	if current.WorkspaceHead != p.Expected.WorkspaceHead || current.Target != p.Expected.Target || current.Dirty != p.Expected.Dirty || current.Generation != p.Expected.Generation {
		return ApplyResult{}, fmt.Errorf("capsule reconcile: stale plan")
	}
	if p.Class == UpToDate {
		return ApplyResult{PlanDigest: p.Digest, OldTarget: current.Target, NewTarget: current.Target, Applied: true}, nil
	}
	if p.Operation == Promote || p.Operation == Integrate || p.Operation == Refresh {
		ok, err := r.VCS.IsAncestor(ctx, p.Workspace, current.Target, p.Candidate)
		if err != nil {
			return ApplyResult{}, err
		}
		if !ok {
			return ApplyResult{}, fmt.Errorf("capsule reconcile: protected update is not fast-forward")
		}
	}
	if err := r.VCS.UpdateRef(ctx, p.Workspace, p.TargetRef, p.Candidate, current.Target); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{PlanDigest: p.Digest, OldTarget: current.Target, NewTarget: p.Candidate, Applied: true}, nil
}
func (r Reconciler) classify(ctx context.Context, workspace string, o ObservedRefs) (Class, error) {
	if o.Dirty {
		return Dirty, nil
	}
	if o.WorkspaceHead == "" || o.Target == "" {
		return Missing, nil
	}
	if o.WorkspaceHead == o.Target {
		return UpToDate, nil
	}
	targetAncestor, err := r.VCS.IsAncestor(ctx, workspace, o.Target, o.WorkspaceHead)
	if err != nil {
		return Missing, err
	}
	if targetAncestor {
		return LocalAhead, nil
	}
	headAncestor, err := r.VCS.IsAncestor(ctx, workspace, o.WorkspaceHead, o.Target)
	if err != nil {
		return Missing, err
	}
	if headAncestor {
		return RemoteAhead, nil
	}
	return Diverged, nil
}

// ValidOperation reports whether an operation is part of the fixed local
// reconciliation vocabulary. Unknown names must never gain a default effect.
func ValidOperation(op Operation) bool {
	switch op {
	case Integrate, Refresh, Promote, Publish:
		return true
	default:
		return false
	}
}

func effect(op Operation) string {
	if op == Publish {
		return "remote_publish"
	}
	return "local_reconcile"
}
func (r Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
func planDigest(p Plan) string {
	p.Digest = ""
	p.CreatedAt = time.Time{}
	raw, _ := json.Marshal(p)
	return "sha256:" + hash(raw)
}
func shortHash(raw []byte) string { return hash(raw)[:16] }
func hash(raw []byte) string      { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

// Git is the production local VCS adapter. External remote publication is a
// distinct provider and is intentionally not hidden behind this implementation.
type Git struct{}

func (Git) Observe(ctx context.Context, dir, target string, generation uint64) (ObservedRefs, error) {
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return ObservedRefs{}, err
	}
	targetOID, err := git(ctx, dir, "rev-parse", target)
	if err != nil {
		return ObservedRefs{}, err
	}
	status, err := git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return ObservedRefs{}, err
	}
	return ObservedRefs{WorkspaceHead: strings.TrimSpace(head), Target: strings.TrimSpace(targetOID), Dirty: strings.TrimSpace(status) != "", Generation: generation}, nil
}
func (Git) IsAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", a, b)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
func (Git) UpdateRef(ctx context.Context, dir, ref, next, old string) error {
	if !strings.HasPrefix(ref, "refs/") && !strings.Contains(ref, "/") {
		ref = "refs/heads/" + ref
	}
	_, err := git(ctx, dir, "update-ref", ref, next, old)
	return err
}
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

var _ = sort.Strings
