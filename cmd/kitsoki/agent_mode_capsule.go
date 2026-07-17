package main

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"kitsoki/internal/agentroot"
	"kitsoki/internal/capsule/control"
)

// agentModeCapsuleOwner is the lease owner recorded on capsules that agent
// mode materializes for protected-root sessions.
const agentModeCapsuleOwner = "agent-mode"

// createProtectedRootAgentModeCapsule is a stubbable seam mirroring
// createProtectedRootCodeactCapsule.
var createProtectedRootAgentModeCapsule = func(ctx context.Context, projectRoot, id, owner string) (control.Instance, error) {
	return createProtectedRootCapsule(ctx, projectRoot, id, owner, "agent-mode")
}

// agentModeCapsuleProvisioner is the production agentroot.CapsuleProvisioner:
// materialize (or reacquire) the managed capsule workspace an agent-mode
// session runs in when started from a protected root. The workspace id is
// stable per agent name, so repeat runs and --continue land in the same
// workspace instead of accreting new ones.
func agentModeCapsuleProvisioner(ctx context.Context, protectedRoot, agentName string) (agentroot.Provisioned, error) {
	id := agentModeCapsuleID(agentName)
	in, err := createProtectedRootAgentModeCapsule(ctx, protectedRoot, id, agentModeCapsuleOwner)
	if err != nil {
		return agentroot.Provisioned{}, err
	}
	path, err := filepath.Abs(in.Path)
	if err != nil {
		return agentroot.Provisioned{}, err
	}
	return agentroot.Provisioned{Path: path, ID: in.ID, Branch: in.Branch}, nil
}

var agentModeCapsuleIDUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// agentModeCapsuleID maps an agent name to its stable workspace id
// (agent-mode-<name>, lowercased, unsafe runs collapsed to '-').
func agentModeCapsuleID(agentName string) string {
	slug := agentModeCapsuleIDUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(agentName)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "agent"
	}
	return "agent-mode-" + slug
}
