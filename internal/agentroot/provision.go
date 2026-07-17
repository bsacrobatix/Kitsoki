package agentroot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"kitsoki/internal/effect"
	"kitsoki/internal/host"
)

// Provisioned describes a capsule workspace materialized for an agent-mode
// session — the provenance callers surface to the operator (mirroring
// agentLaunchCapsuleProvenance on the CodeAct launch path).
type Provisioned struct {
	// Path is the workspace checkout the session's working dir becomes.
	Path string
	// ID is the managed capsule workspace id (stable per agent name, so
	// resume reacquires the same workspace).
	ID string
	// Branch is the workspace branch, when the provisioner reports it.
	Branch string
	// ProtectedRoot is the protected project root the workspace was
	// provisioned under.
	ProtectedRoot string
}

// CapsuleProvisioner materializes (or reacquires) a managed capsule workspace
// under protectedRoot for the named agent, returning the workspace path the
// session should run in. It is the injectable seam between agentroot (which
// knows the policy outcome) and the capsule control machinery (which lives
// with the callers): cmd/kitsoki injects the dev-workspace-script
// implementation; tests inject fakes; a nil provisioner preserves the plain
// denial behavior.
type CapsuleProvisioner func(ctx context.Context, protectedRoot, agentName string) (Provisioned, error)

// EnsureWorkingDir is the protected-root auto-capsule gate that runs BEFORE
// Synthesize, honoring the same invariant as the CodeAct launch path: the
// launch policy must see the materialized capsule, never a protected checkout
// with a special-case allow rule.
//
//   - read|pure agents pass through untouched (they are preflight-exempt).
//   - write|external agents run the `agent.mode` policy check on workingDir.
//     Allowed → pass through. Denied while workingDir sits under a configured
//     protected root and a provisioner is injected → provision (stable id, so
//     repeat runs and --continue reacquire the same workspace), re-check the
//     policy against the capsule path, and return it plus provenance.
//   - any other denial — no protected-root match, nil provisioner, or a
//     provisioning failure — returns the standard *PolicyDeniedError so the
//     operator sees exactly the auditable decision they see today (with the
//     provisioning failure appended when there was one).
//
// The returned working dir is what callers pass as Options.WorkingDir (and
// publish as KITSOKI_APP_DIR); Synthesize's own preflight then re-checks it
// and passes naturally.
func EnsureWorkingDir(ctx context.Context, def Def, policy host.AgentLaunchPolicy, check PolicyCheckFunc, workingDir string, provision CapsuleProvisioner) (string, *Provisioned, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		workingDir = "."
	}
	if abs, err := filepath.Abs(workingDir); err == nil {
		workingDir = abs
	}
	if def.Effect != effect.Write && def.Effect != effect.External {
		return workingDir, nil, nil
	}
	normalized := policy.Normalized()
	if check == nil {
		check = normalized.Check
	}
	decision, checkErr := check(ctx, "agent.mode", def.Name, workingDir)
	if checkErr == nil {
		return workingDir, nil, nil
	}
	denied := &PolicyDeniedError{Decision: decision, Err: checkErr}
	protectedRoot := normalized.MatchProtectedRoot(workingDir)
	if protectedRoot == "" || provision == nil {
		return "", nil, denied
	}
	prov, provErr := provision(ctx, protectedRoot, def.Name)
	if provErr != nil {
		return "", nil, &PolicyDeniedError{
			Decision: decision,
			Err:      fmt.Errorf("%w; auto-capsule provisioning under %s failed: %v", checkErr, protectedRoot, provErr),
		}
	}
	prov.ProtectedRoot = protectedRoot
	capsuleDecision, capsuleErr := check(ctx, "agent.mode", def.Name, prov.Path)
	if capsuleErr != nil {
		return "", nil, &PolicyDeniedError{
			Decision: capsuleDecision,
			Err:      fmt.Errorf("provisioned capsule workspace %s is still denied: %w", prov.Path, capsuleErr),
		}
	}
	return prov.Path, &prov, nil
}
