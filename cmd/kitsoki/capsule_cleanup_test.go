package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/capsule/hygiene"
)

func TestCapsuleCleanupClearUsesFixedArchiveFreeRetentionPolicy(t *testing.T) {
	opts := capsuleClearRetentionOptions("fixture")
	if opts.ProjectRoot != "fixture" {
		t.Fatalf("options=%#v", opts)
	}
	if opts.KeepWorkspaces != -1 || opts.MinAge != 5*time.Minute {
		t.Fatalf("clear retention/cooloff policy=%#v", opts)
	}
	if opts.MaxBytes != -1 || opts.CloseWorkspace != nil {
		t.Fatalf("clear must use the internal archive-free remover without a payload walk or provider: %#v", opts)
	}
}

func TestCapsuleCleanupPlanApplyNeverInvokeWorkspaceArchiveProviders(t *testing.T) {
	opts := (capsuleCleanupFlags{project: "fixture"}).options()
	if !opts.ArchiveFreeWorkspaceRemovalOnly {
		t.Fatalf("operator cleanup options can invoke materializing workspace providers: %#v", opts)
	}
}

func TestCapsuleCleanupWriteSurfacesTypedActivityDiagnostics(t *testing.T) {
	cmd := capsuleCleanupPlanCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := capsuleCleanupWrite(cmd, hygiene.Plan{
		Diagnostics: []hygiene.ActivityDiagnostic{{
			Code:     "lsof_irrelevant_tracefs_unavailable",
			Severity: "warning",
			Source:   "lsof",
			Message:  "workspace process inventory remained conclusive",
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cleanup diagnostic [warning/lsof_irrelevant_tracefs_unavailable] lsof: workspace process inventory remained conclusive") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestCapsuleCleanupOwnerReconcileDryRunWritesTypedJSONToRootOutput(t *testing.T) {
	project := t.TempDir()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"capsule", "cleanup", "reconcile-owners", "--project", project, "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("dry-run emitted no stdout")
	}
	var plan hygiene.OwnerReconcilePlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("stdout is not typed JSON: %v: %q", err, out.String())
	}
	if plan.Schema != hygiene.OwnerReconcileSchema || !plan.DryRun {
		t.Fatalf("plan=%#v", plan)
	}
}
