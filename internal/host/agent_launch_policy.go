package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"kitsoki/internal/capsule"
	"kitsoki/internal/store"
)

var defaultProtectedAgentBranches = []string{
	"main",
	"master",
	"trunk",
	"integration/*",
	"staging/*",
}

// AgentLaunchPolicy is the deterministic preflight gate for external coding
// agent launches. It is not a filesystem sandbox; it rejects unsafe working
// directories before any backend CLI is forked, and records the decision.
type AgentLaunchPolicy struct {
	Enabled           bool     `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	RequireCapsule    bool     `yaml:"require_capsule,omitempty" json:"require_capsule,omitempty"`
	ProtectedBranches []string `yaml:"protected_branches,omitempty" json:"protected_branches,omitempty"`
	ProtectedRoots    []string `yaml:"protected_roots,omitempty" json:"protected_roots,omitempty"`
	AllowedRoots      []string `yaml:"allowed_roots,omitempty" json:"allowed_roots,omitempty"`

	// Placement is the federation placement policy matrix (standing-autonomy
	// proposal §9 "Federation", invariant SA-I7): lane name -> allowed worker
	// classes / network profiles for a remote dispatch. A lane absent from
	// this map, or a nil/empty Placement map altogether, means placement is
	// not enforced for that lane — CheckPlacement passes through, so existing
	// callers that never call CheckPlacement (every current Check(...) call
	// site) are completely unaffected. This is a SEPARATE, EARLIER gate than
	// internal/capsule/executor's ValidateCapabilities (the sealed-envelope
	// Policy-vs-Capabilities check applied at execution prepare time):
	// CheckPlacement runs at launch preflight against the worker's advertised
	// *class*, before any sealed envelope exists; ValidateCapabilities stays
	// untouched as the runtime enforcement layer that follows it.
	Placement map[string]PlacementLanePolicy `yaml:"placement,omitempty" json:"placement,omitempty"`
}

// PlacementLanePolicy is one lane's placement rule: the worker placement
// classes ("thin", "workstation", "local-model" — see
// internal/daemonfederation's Placement* constants, reused verbatim rather
// than redeclared) and network profiles permitted to run that lane's
// dispatches. Empty WorkerClasses or NetworkProfiles means "no restriction"
// on that dimension; a lane simply absent from AgentLaunchPolicy.Placement
// cannot be remotely dispatched at all once any Placement policy is
// configured (see CheckPlacement) — this is how the proposal's delivery lane
// (queue worker, protected-main CAS) stays local-only: never add it to the
// map.
type PlacementLanePolicy struct {
	WorkerClasses   []string `yaml:"worker_classes,omitempty" json:"worker_classes,omitempty"`
	NetworkProfiles []string `yaml:"network_profiles,omitempty" json:"network_profiles,omitempty"`
}

// PlacementTarget describes one remote dispatch's requested lane and the
// worker it is about to be pinned to, for CheckPlacement.
type PlacementTarget struct {
	Lane           string
	WorkerID       string
	WorkerClass    string
	NetworkProfile string
}

// PlacementDecision is the auditable result of a placement preflight check,
// mirroring AgentLaunchDecision's shape for consistent logging/event
// recording.
type PlacementDecision struct {
	Allowed         bool     `json:"allowed"`
	Reason          string   `json:"reason,omitempty"`
	Lane            string   `json:"lane"`
	WorkerID        string   `json:"worker_id,omitempty"`
	WorkerClass     string   `json:"worker_class,omitempty"`
	NetworkProfile  string   `json:"network_profile,omitempty"`
	AllowedClasses  []string `json:"allowed_classes,omitempty"`
	AllowedNetworks []string `json:"allowed_networks,omitempty"`
}

// AgentLaunchDecision is the auditable result of checking a launch directory.
type AgentLaunchDecision struct {
	Enabled           bool     `json:"enabled"`
	Allowed           bool     `json:"allowed"`
	Reason            string   `json:"reason,omitempty"`
	Verb              string   `json:"verb,omitempty"`
	Agent             string   `json:"agent,omitempty"`
	WorkingDir        string   `json:"working_dir,omitempty"`
	GitRoot           string   `json:"git_root,omitempty"`
	GitBranch         string   `json:"git_branch,omitempty"`
	ProtectedRoot     string   `json:"protected_root,omitempty"`
	ProtectedBranch   string   `json:"protected_branch,omitempty"`
	CapsuleRoot       string   `json:"capsule_root,omitempty"`
	CapsuleName       string   `json:"capsule_name,omitempty"`
	CapsuleSpecPath   string   `json:"capsule_spec_path,omitempty"`
	RequireCapsule    bool     `json:"require_capsule,omitempty"`
	AllowedRoots      []string `json:"allowed_roots,omitempty"`
	ProtectedRoots    []string `json:"protected_roots,omitempty"`
	ProtectedBranches []string `json:"protected_branches,omitempty"`
}

type agentLaunchPolicyKey struct{}

func WithAgentLaunchPolicy(ctx context.Context, policy AgentLaunchPolicy) context.Context {
	if !policy.Enabled {
		return ctx
	}
	return context.WithValue(ctx, agentLaunchPolicyKey{}, policy.Normalized())
}

func AgentLaunchPolicyFromContext(ctx context.Context) AgentLaunchPolicy {
	p, _ := ctx.Value(agentLaunchPolicyKey{}).(AgentLaunchPolicy)
	return p.Normalized()
}

func (p AgentLaunchPolicy) Normalized() AgentLaunchPolicy {
	if !p.Enabled {
		return p
	}
	if len(p.ProtectedBranches) == 0 {
		p.ProtectedBranches = append([]string(nil), defaultProtectedAgentBranches...)
	}
	p.ProtectedBranches = cleanTrimStringList(p.ProtectedBranches)
	p.ProtectedRoots = cleanPathStringList(p.ProtectedRoots)
	p.AllowedRoots = cleanPathStringList(p.AllowedRoots)
	return p
}

func (p AgentLaunchPolicy) Check(ctx context.Context, verb, agentName, workingDir string) (AgentLaunchDecision, error) {
	p = p.Normalized()
	decision := AgentLaunchDecision{
		Enabled:           p.Enabled,
		Allowed:           true,
		Verb:              strings.TrimSpace(verb),
		Agent:             strings.TrimSpace(agentName),
		RequireCapsule:    p.RequireCapsule,
		AllowedRoots:      append([]string(nil), p.AllowedRoots...),
		ProtectedRoots:    append([]string(nil), p.ProtectedRoots...),
		ProtectedBranches: append([]string(nil), p.ProtectedBranches...),
	}
	if !p.Enabled {
		decision.Reason = "policy disabled"
		return decision, nil
	}
	wd := strings.TrimSpace(workingDir)
	if wd == "" {
		wd = "."
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return denyLaunch(decision, fmt.Sprintf("resolve working_dir %q: %v", workingDir, err))
	}
	abs = resolveExistingPath(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return denyLaunch(decision, fmt.Sprintf("working_dir %s is not accessible: %v", abs, err))
	}
	if !info.IsDir() {
		return denyLaunch(decision, fmt.Sprintf("working_dir %s is not a directory", abs))
	}
	decision.WorkingDir = abs

	inAllowedRoot := false
	for _, root := range p.AllowedRoots {
		if root != "" && pathContains(root, abs) {
			inAllowedRoot = true
			break
		}
	}
	if len(p.AllowedRoots) > 0 && !inAllowedRoot {
		return denyLaunch(decision, fmt.Sprintf("working_dir %s is outside allowed agent roots", abs))
	}

	if !inAllowedRoot {
		for _, root := range p.ProtectedRoots {
			if root != "" && pathContains(root, abs) {
				decision.ProtectedRoot = resolveExistingPath(root)
				return denyLaunch(decision, fmt.Sprintf("working_dir %s is inside protected root %s", abs, decision.ProtectedRoot))
			}
		}
	}

	if capsuleRoot, manifest, ok := findOpenedCapsule(abs); ok {
		decision.CapsuleRoot = capsuleRoot
		decision.CapsuleName = manifest.CapsuleName
		decision.CapsuleSpecPath = manifest.SpecPath
	}

	if root, branch, ok := gitLaunchInfo(ctx, abs); ok {
		decision.GitRoot = root
		decision.GitBranch = branch
		gitInAllowedRoot := inAllowedRoot
		if !gitInAllowedRoot {
			for _, allowedRoot := range p.AllowedRoots {
				if allowedRoot != "" && pathContains(allowedRoot, root) {
					gitInAllowedRoot = true
					break
				}
			}
		}
		if !gitInAllowedRoot {
			for _, protectedRoot := range p.ProtectedRoots {
				if protectedRoot != "" && pathContains(protectedRoot, root) {
					decision.ProtectedRoot = resolveExistingPath(protectedRoot)
					return denyLaunch(decision, fmt.Sprintf("git root %s is inside protected root %s", root, decision.ProtectedRoot))
				}
			}
		}
		gitInsideCapsule := decision.CapsuleRoot != "" && pathContains(decision.CapsuleRoot, root)
		if !gitInsideCapsule {
			if protected := matchProtectedBranch(branch, p.ProtectedBranches); protected != "" {
				decision.ProtectedBranch = protected
				return denyLaunch(decision, fmt.Sprintf("git branch %q is protected by pattern %q", branch, protected))
			}
		}
	}

	if decision.CapsuleRoot == "" && p.RequireCapsule {
		return denyLaunch(decision, fmt.Sprintf("working_dir %s is not inside an opened Kitsoki capsule", abs))
	}

	decision.Reason = "allowed"
	return decision, nil
}

// CheckPlacement enforces SA-I7 ("remote execution happens only through
// sealed envelopes against registered, enabled workers whose advertised
// capabilities satisfy the lane's policy; placement violations fail at
// launch preflight, not at runtime"). It is a companion to Check, not a
// replacement: Check governs the local working-directory/branch/capsule
// preflight; CheckPlacement additionally governs whether a specific lane may
// target a specific remote worker class/network profile at all, and runs
// BEFORE the sealed-envelope Policy-vs-Capabilities gate in
// internal/capsule/executor.ValidateCapabilities.
//
// Backward compatibility: a nil or empty Placement map on the policy (the
// zero value, and every policy configured before this field existed) makes
// CheckPlacement always allow — placement is opt-in per deployment. A lane
// present in the map that the target's class or network profile does not
// satisfy is denied with a deterministic, tested error message.
func (p AgentLaunchPolicy) CheckPlacement(target PlacementTarget) (PlacementDecision, error) {
	lane := strings.TrimSpace(target.Lane)
	decision := PlacementDecision{
		Allowed:        true,
		Lane:           lane,
		WorkerID:       strings.TrimSpace(target.WorkerID),
		WorkerClass:    strings.TrimSpace(target.WorkerClass),
		NetworkProfile: strings.TrimSpace(target.NetworkProfile),
	}
	if len(p.Placement) == 0 {
		decision.Reason = "no placement policy configured"
		return decision, nil
	}
	rule, ok := p.Placement[lane]
	if !ok {
		decision.Reason = fmt.Sprintf("lane %q has no placement policy entry", lane)
		decision.Allowed = false
		return decision, fmt.Errorf("agent launch policy denied: lane %q has no placement policy entry and placement is configured, so it may not target any remote worker", lane)
	}
	decision.AllowedClasses = append([]string(nil), rule.WorkerClasses...)
	decision.AllowedNetworks = append([]string(nil), rule.NetworkProfiles...)
	if len(rule.WorkerClasses) > 0 && !stringInList(decision.WorkerClass, rule.WorkerClasses) {
		decision.Allowed = false
		decision.Reason = fmt.Sprintf("worker class %q not in allowed classes %v", decision.WorkerClass, rule.WorkerClasses)
		return decision, fmt.Errorf("agent launch policy denied: lane %q targets worker %q (class %q) which does not satisfy placement policy (allowed classes: %v)", lane, decision.WorkerID, decision.WorkerClass, rule.WorkerClasses)
	}
	if len(rule.NetworkProfiles) > 0 && !stringInList(decision.NetworkProfile, rule.NetworkProfiles) {
		decision.Allowed = false
		decision.Reason = fmt.Sprintf("network profile %q not in allowed profiles %v", decision.NetworkProfile, rule.NetworkProfiles)
		return decision, fmt.Errorf("agent launch policy denied: lane %q targets worker %q with network profile %q which does not satisfy placement policy (allowed network profiles: %v)", lane, decision.WorkerID, decision.NetworkProfile, rule.NetworkProfiles)
	}
	decision.Reason = "allowed"
	return decision, nil
}

func stringInList(value string, list []string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// MatchProtectedRoot returns the configured protected root containing path
// (the resolved root, matching the decision's ProtectedRoot field), or "" when
// path is outside every protected root. Callers that can materialize a
// policy-approved workspace under a protected project (agent mode's
// auto-capsule provisioning) use this to find the project root to provision
// against after a denial.
func (p AgentLaunchPolicy) MatchProtectedRoot(path string) string {
	for _, root := range p.Normalized().ProtectedRoots {
		if root != "" && pathContains(root, path) {
			return resolveExistingPath(root)
		}
	}
	return ""
}

// MatchAllowedRoot is MatchProtectedRoot's counterpart: it returns the
// configured allowed root containing path, or "" when no allowed root does (a
// policy with no allowed_roots therefore always returns ""). Check already
// treats an allowed root as an explicit operator carve-out that outranks
// protected-root containment; callers that must decide whether a working
// directory is *already* policy-approved — rather than something to redirect
// into a freshly provisioned workspace — use this instead of re-deriving the
// containment rule.
func (p AgentLaunchPolicy) MatchAllowedRoot(path string) string {
	for _, root := range p.Normalized().AllowedRoots {
		if root != "" && pathContains(root, path) {
			return resolveExistingPath(root)
		}
	}
	return ""
}

func CheckAgentLaunchPolicy(ctx context.Context, verb, agentName, workingDir string) (AgentLaunchDecision, error) {
	return AgentLaunchPolicyFromContext(ctx).Check(ctx, verb, agentName, workingDir)
}

func RequireAgentLaunchAllowed(ctx context.Context, verb, agentName, workingDir string) (AgentLaunchDecision, string) {
	decision, err := CheckAgentLaunchPolicy(ctx, verb, agentName, workingDir)
	if decision.Enabled {
		appendAgentLaunchPolicyEvent(ctx, "", decision)
	}
	if err != nil {
		return decision, err.Error()
	}
	return decision, ""
}

func AppendAgentLaunchPolicyEvent(ctx context.Context, callID string, decision AgentLaunchDecision) {
	appendAgentLaunchPolicyEvent(ctx, callID, decision)
}

func appendAgentLaunchPolicyEvent(ctx context.Context, callID string, decision AgentLaunchDecision) {
	sink := EventSinkFromAgentCtx(ctx)
	if sink == nil {
		return
	}
	oc := AgentCallCtxFrom(ctx)
	raw, err := json.Marshal(decision)
	if err != nil {
		return
	}
	_ = sink.Append(store.Event{
		Turn:      oc.Turn,
		Ts:        time.Now(),
		Kind:      store.EventKind("agent.launch.policy"),
		StatePath: oc.StatePath,
		Payload:   raw,
		CallID:    callID,
	})
}

func denyLaunch(decision AgentLaunchDecision, reason string) (AgentLaunchDecision, error) {
	decision.Allowed = false
	decision.Reason = reason
	return decision, fmt.Errorf("agent launch policy denied: %s", reason)
}

func cleanTrimStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func cleanPathStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, filepath.Clean(s))
		}
	}
	return out
}

func resolveExistingPath(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func pathContains(root, path string) bool {
	root = resolveExistingPath(root)
	path = resolveExistingPath(path)
	if root == path {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func gitLaunchInfo(ctx context.Context, dir string) (root, branch string, ok bool) {
	gitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rootOut, err := exec.CommandContext(gitCtx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", false
	}
	root = strings.TrimSpace(string(rootOut))
	if root == "" {
		return "", "", false
	}
	root = resolveExistingPath(root)
	branchOut, err := exec.CommandContext(gitCtx, "git", "-C", dir, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err == nil {
		branch = strings.TrimSpace(string(branchOut))
	}
	if branch == "" {
		branchOut, _ = exec.CommandContext(gitCtx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
		branch = strings.TrimSpace(string(branchOut))
	}
	return root, branch, true
}

func matchProtectedBranch(branch string, patterns []string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return ""
	}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == branch {
			return pattern
		}
		if ok, _ := filepath.Match(pattern, branch); ok {
			return pattern
		}
	}
	return ""
}

func findOpenedCapsule(dir string) (string, capsule.Manifest, bool) {
	dir = resolveExistingPath(dir)
	for {
		sentinel := filepath.Join(dir, capsule.SentinelFile)
		manifestPath := filepath.Join(dir, capsule.ManifestFile)
		if launchPolicyFileExists(sentinel) && launchPolicyFileExists(manifestPath) {
			manifest, err := capsule.ReadManifest(dir)
			if err == nil {
				return dir, manifest, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", capsule.Manifest{}, false
		}
		dir = parent
	}
}

func launchPolicyFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
