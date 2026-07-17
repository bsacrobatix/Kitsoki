package main

import (
	"context"
	"errors"
	"testing"

	"kitsoki/internal/capsule/control"
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
