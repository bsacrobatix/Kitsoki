package agentroot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/effect"
	"kitsoki/internal/host"
)

func provisionTestDef(eff effect.Effect) Def {
	return Def{
		Name:         "worker",
		SystemPrompt: "You are worker.",
		Tools:        []string{"Read", "Grep", "Glob", "Edit", "Write", "Bash"},
		Effect:       eff,
	}
}

// denyInside returns a PolicyCheckFunc that denies any working dir inside
// protectedRoot and allows everything else — the POG-shaped policy.
func denyInside(protectedRoot string, calls *[]string) PolicyCheckFunc {
	return func(_ context.Context, verb, agentName, workingDir string) (host.AgentLaunchDecision, error) {
		if calls != nil {
			*calls = append(*calls, workingDir)
		}
		if verb != "agent.mode" {
			return host.AgentLaunchDecision{}, fmt.Errorf("unexpected verb %q", verb)
		}
		rel, err := filepath.Rel(protectedRoot, workingDir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			decision := host.AgentLaunchDecision{Enabled: true, Allowed: false, Verb: verb, Agent: agentName, WorkingDir: workingDir}
			return decision, fmt.Errorf("agent launch policy denied: working_dir %s is outside allowed agent roots", workingDir)
		}
		return host.AgentLaunchDecision{Enabled: true, Allowed: true, Verb: verb, Agent: agentName, WorkingDir: workingDir}, nil
	}
}

func TestEnsureWorkingDir_ReadAgentPassesThrough(t *testing.T) {
	root := t.TempDir()
	called := false
	dir, prov, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.Read),
		host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{root}},
		func(context.Context, string, string, string) (host.AgentLaunchDecision, error) {
			called = true
			return host.AgentLaunchDecision{}, errors.New("must not be consulted")
		}, root, nil)
	if err != nil {
		t.Fatalf("EnsureWorkingDir: %v", err)
	}
	if called {
		t.Fatal("read agent must not run the launch-policy preflight")
	}
	if prov != nil {
		t.Fatal("read agent must not provision")
	}
	if dir != root {
		t.Fatalf("dir = %q, want %q", dir, root)
	}
}

func TestEnsureWorkingDir_AllowedWriteAgentPassesThrough(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "elsewhere")
	dir, prov, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.Write),
		host.AgentLaunchPolicy{Enabled: true},
		denyInside(filepath.Join(root, "protected"), nil), allowed,
		func(context.Context, string, string) (Provisioned, error) {
			return Provisioned{}, errors.New("must not provision when allowed")
		})
	if err != nil {
		t.Fatalf("EnsureWorkingDir: %v", err)
	}
	if prov != nil {
		t.Fatal("allowed working dir must not provision")
	}
	if dir != allowed {
		t.Fatalf("dir = %q, want %q", dir, allowed)
	}
}

func TestEnsureWorkingDir_DeniedProtectedRootProvisionsCapsule(t *testing.T) {
	base := t.TempDir()
	protected := filepath.Join(base, "project")
	capsule := filepath.Join(base, "capsules", "agent-mode-worker")
	var checks []string
	var gotRoot, gotAgent string
	dir, prov, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.Write),
		host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{protected}},
		denyInside(protected, &checks), protected,
		func(_ context.Context, protectedRoot, agentName string) (Provisioned, error) {
			gotRoot, gotAgent = protectedRoot, agentName
			return Provisioned{Path: capsule, ID: "agent-mode-worker", Branch: "agent/agent-mode-worker"}, nil
		})
	if err != nil {
		t.Fatalf("EnsureWorkingDir: %v", err)
	}
	if dir != capsule {
		t.Fatalf("dir = %q, want provisioned capsule %q", dir, capsule)
	}
	if prov == nil || prov.ID != "agent-mode-worker" || prov.Path != capsule {
		t.Fatalf("provenance = %+v", prov)
	}
	if prov.ProtectedRoot == "" || filepath.Base(prov.ProtectedRoot) != "project" {
		t.Fatalf("provenance protected root = %q", prov.ProtectedRoot)
	}
	if gotAgent != "worker" {
		t.Fatalf("provisioner agent = %q", gotAgent)
	}
	if filepath.Base(gotRoot) != "project" {
		t.Fatalf("provisioner root = %q", gotRoot)
	}
	// The capsule path must be re-checked: deny(protected) then allow(capsule).
	if len(checks) != 2 || checks[1] != capsule {
		t.Fatalf("policy checks = %v, want [protected, capsule]", checks)
	}
}

func TestEnsureWorkingDir_ExternalAgentProvisionsToo(t *testing.T) {
	base := t.TempDir()
	protected := filepath.Join(base, "project")
	capsule := filepath.Join(base, "ws")
	dir, prov, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.External),
		host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{protected}},
		denyInside(protected, nil), protected,
		func(context.Context, string, string) (Provisioned, error) {
			return Provisioned{Path: capsule, ID: "agent-mode-worker"}, nil
		})
	if err != nil {
		t.Fatalf("EnsureWorkingDir: %v", err)
	}
	if dir != capsule || prov == nil {
		t.Fatalf("dir = %q, prov = %+v", dir, prov)
	}
}

func TestEnsureWorkingDir_NilProvisionerKeepsDenial(t *testing.T) {
	base := t.TempDir()
	protected := filepath.Join(base, "project")
	_, _, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.Write),
		host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{protected}},
		denyInside(protected, nil), protected, nil)
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want *PolicyDeniedError", err)
	}
	if !strings.Contains(err.Error(), "run from a managed capsule workspace") {
		t.Fatalf("denial lost its capsule guidance: %v", err)
	}
}

func TestEnsureWorkingDir_DenialOutsideProtectedRootsStays(t *testing.T) {
	base := t.TempDir()
	denyDir := filepath.Join(base, "denied")
	_, _, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.Write),
		host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{filepath.Join(base, "unrelated")}},
		denyInside(denyDir, nil), denyDir,
		func(context.Context, string, string) (Provisioned, error) {
			t.Fatal("must not provision outside a protected root")
			return Provisioned{}, nil
		})
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want *PolicyDeniedError", err)
	}
}

func TestEnsureWorkingDir_ProvisioningFailureDegradesToDenial(t *testing.T) {
	base := t.TempDir()
	protected := filepath.Join(base, "project")
	_, _, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.Write),
		host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{protected}},
		denyInside(protected, nil), protected,
		func(context.Context, string, string) (Provisioned, error) {
			return Provisioned{}, errors.New("dev-workspace script exploded")
		})
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want *PolicyDeniedError", err)
	}
	if !strings.Contains(err.Error(), "auto-capsule provisioning") || !strings.Contains(err.Error(), "dev-workspace script exploded") {
		t.Fatalf("denial must carry the provisioning failure: %v", err)
	}
}

func TestEnsureWorkingDir_ProvisionedDirStillDeniedFails(t *testing.T) {
	base := t.TempDir()
	protected := filepath.Join(base, "project")
	// Provisioner returns a path INSIDE the protected root, so the re-check
	// denies again — the invariant that the policy sees the materialized
	// capsule must hold, never a blind allow after provisioning.
	_, _, err := EnsureWorkingDir(context.Background(), provisionTestDef(effect.Write),
		host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{protected}},
		denyInside(protected, nil), protected,
		func(context.Context, string, string) (Provisioned, error) {
			return Provisioned{Path: filepath.Join(protected, "nested"), ID: "x"}, nil
		})
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want *PolicyDeniedError", err)
	}
	if !strings.Contains(err.Error(), "still denied") {
		t.Fatalf("err = %v, want provisioned-dir re-check failure", err)
	}
}

func TestMatchProtectedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	policy := host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{root}}
	if got := policy.MatchProtectedRoot(filepath.Join(root, "sub", "dir")); got == "" {
		t.Fatal("subdir of protected root must match")
	}
	if got := policy.MatchProtectedRoot(root); got == "" {
		t.Fatal("protected root itself must match")
	}
	if got := policy.MatchProtectedRoot(filepath.Join(base, "other")); got != "" {
		t.Fatalf("unrelated dir matched %q", got)
	}
}
