package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsuletest"
)

func TestAgentModeCapsuleID(t *testing.T) {
	cases := map[string]string{
		"pog-driver":     "agent-mode-pog-driver",
		"Story_Author":   "agent-mode-story-author",
		"  weird//name ": "agent-mode-weird-name",
		"":               "agent-mode-agent",
	}
	for in, want := range cases {
		if got := agentModeCapsuleID(in); got != want {
			t.Errorf("agentModeCapsuleID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentModeCapsuleProvisioner_StableIDAndProvenance(t *testing.T) {
	orig := createProtectedRootAgentModeCapsule
	t.Cleanup(func() { createProtectedRootAgentModeCapsule = orig })

	var gotRoot, gotID, gotOwner string
	createProtectedRootAgentModeCapsule = func(_ context.Context, projectRoot, id, owner string) (control.Instance, error) {
		gotRoot, gotID, gotOwner = projectRoot, id, owner
		return control.Instance{ID: id, Path: t.TempDir(), Branch: "agent/" + id}, nil
	}

	prov, err := agentModeCapsuleProvisioner(context.Background(), "/proj", "pog-driver")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if gotRoot != "/proj" || gotID != "agent-mode-pog-driver" || gotOwner != agentModeCapsuleOwner {
		t.Fatalf("create called with (%q, %q, %q)", gotRoot, gotID, gotOwner)
	}
	if prov.ID != "agent-mode-pog-driver" || prov.Path == "" || prov.Branch != "agent/agent-mode-pog-driver" {
		t.Fatalf("provenance = %+v", prov)
	}
}

func TestAgentModeCapsuleProvisioner_CreateFailurePropagates(t *testing.T) {
	orig := createProtectedRootAgentModeCapsule
	t.Cleanup(func() { createProtectedRootAgentModeCapsule = orig })
	createProtectedRootAgentModeCapsule = func(context.Context, string, string, string) (control.Instance, error) {
		return control.Instance{}, errors.New("no development definition")
	}
	if _, err := agentModeCapsuleProvisioner(context.Background(), "/proj", "x"); err == nil {
		t.Fatal("expected create failure to propagate")
	}
}

// TestCreateProtectedRootCapsule_SelfKindDefinition proves the shared
// protected-root materializer handles a generic (non-script) `development`
// definition — the studio-sassfully shape: `source.kind: self`, no
// scripts/dev-workspace.sh — through Manager.Create, including reacquire on a
// second call. Hermetic: the project is the clean-repo capsule fixture.
func TestCreateProtectedRootCapsule_SelfKindDefinition(t *testing.T) {
	requireLocalCapsuleHeadroom(t)
	project := capsuletest.Open(t, "clean-repo")
	capsDir := filepath.Join(project, ".kitsoki", "capsules")
	require.NoError(t, os.MkdirAll(capsDir, 0o755))
	const devYAML = `schema: capsule-definition/v1
id: development
description: Self-clone workspace for the current checkout.
source:
  kind: self
policy:
  network: none
`
	require.NoError(t, os.WriteFile(filepath.Join(capsDir, "development.yaml"), []byte(devYAML), 0o600))

	ctx := context.Background()
	in, err := createProtectedRootCapsule(ctx, project, "agent-mode-demo", agentModeCapsuleOwner, "agent-mode")
	require.NoError(t, err, "a self-kind development definition must materialize through Manager.Create")
	require.Equal(t, "agent-mode-demo", in.ID)
	require.DirExists(t, in.Path)
	require.True(t, codeactCapsuleLaunchable(in.State), "state = %s", in.State)

	again, err := createProtectedRootCapsule(ctx, project, "agent-mode-demo", agentModeCapsuleOwner, "agent-mode")
	require.NoError(t, err, "a second call must reacquire, not conflict")
	require.Equal(t, in.Path, again.Path, "reacquire must land in the same workspace")
}
