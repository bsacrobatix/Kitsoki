package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/capsule/hygiene"
)

func TestCapsuleCleanupClearUsesFixedInactiveMergedPolicy(t *testing.T) {
	opts := capsuleClearInactiveOptions("fixture")
	if opts.ProjectRoot != "fixture" || !opts.ClearInactiveMerged {
		t.Fatalf("options=%#v", opts)
	}
	if opts.KeepRuns != -1 || opts.KeepWorkspaces != -1 || opts.MinWorkspaceAge != 5*time.Minute {
		t.Fatalf("clear retention/cooloff policy=%#v", opts)
	}
	if opts.IncludeCapsuleCache || opts.IncludeGoBuildCache || opts.MeasureWorkspaceBytes {
		t.Fatalf("clear must not touch caches or walk workspace bytes: %#v", opts)
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
