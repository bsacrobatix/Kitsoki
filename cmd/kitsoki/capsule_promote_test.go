package main

import (
	"context"
	"path/filepath"
	"testing"

	"kitsoki/internal/capsule/control"
)

func TestCapsulePromoteExposesExplicitEmergencySkipTestsFlag(t *testing.T) {
	flag := capsulePromoteCmd().Flags().Lookup("skip-tests")
	if flag == nil {
		t.Fatal("capsule promote is missing --skip-tests")
	}
	if flag.DefValue != "false" {
		t.Fatalf("skip-tests default = %q", flag.DefValue)
	}
}

// TestCapsulePromoteDoctorCheckReturnsTypedNotReadyForMissingConfig guards
// the receipt-bound promotion path: `capsule promote` must run the same
// bounded, no-spend readiness preflight as `capsule ci doctor` and surface a
// typed not-ready report instead of failing with an opaque error (or, prior
// to this preflight existing, proceeding into a run that could hang).
func TestCapsulePromoteDoctorCheckReturnsTypedNotReadyForMissingConfig(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, ".capsules", "workspaces", "w")
	instance := control.Instance{ID: "w", State: control.StateReady, Generation: 1, DefinitionDigest: "sha256:def", Head: "sha256:source"}

	report, err := capsulePromoteDoctorCheck(context.Background(), root, "change", instance, workspacePath)
	if err != nil {
		t.Fatalf("doctor check returned an error instead of a typed report: %v", err)
	}
	if report.Ready {
		t.Fatalf("expected a not-ready report for a workspace missing .kitsoki/ci.yaml, got %#v", report)
	}
	if len(report.Checks) == 0 {
		t.Fatal("expected at least one typed check explaining why promote is blocked")
	}
}
