package headroom

import (
	"errors"
	"strings"
	"testing"
)

func TestEnsureRefusesLowSpaceWithPlannerOnlyRemediation(t *testing.T) {
	guard := Guard{Enabled: true, FreeBytes: func(string) (int64, error) { return MinimumFloorBytes - 1, nil }}
	err := guard.Ensure(t.TempDir())
	var refusal *Error
	if !errors.As(err, &refusal) {
		t.Fatalf("error=%v, want typed headroom refusal", err)
	}
	if refusal.Class() != FailureClass || refusal.FloorBytes != MinimumFloorBytes || !strings.Contains(refusal.Remediation, "kitsoki capsule cleanup plan --project") || !strings.Contains(refusal.Remediation, "--json=true") {
		t.Fatalf("refusal=%#v", refusal)
	}
	if strings.Contains(refusal.Remediation, "apply") || strings.Contains(refusal.Remediation, "delete") {
		t.Fatalf("remediation must be plan-only: %q", refusal.Remediation)
	}
}

func TestEnsureRejectsConfigurationBelowMinimum(t *testing.T) {
	err := (Guard{Enabled: true, FloorBytes: MinimumFloorBytes - 1}).Ensure(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "below minimum") {
		t.Fatalf("error=%v", err)
	}
}

func TestEnsurePermitsConfiguredHigherFloor(t *testing.T) {
	const floor = 25 << 30
	err := (Guard{Enabled: true, FloorBytes: floor, FreeBytes: func(string) (int64, error) { return floor, nil }}).Ensure(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureReadsStricterFloorFromEnvironment(t *testing.T) {
	const floor = 24 << 30
	t.Setenv(EnvFloorBytes, "25769803776")
	err := (Guard{Enabled: true, FreeBytes: func(string) (int64, error) { return floor - 1, nil }}).Ensure(t.TempDir())
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.FloorBytes != floor {
		t.Fatalf("error=%v refusal=%#v", err, refusal)
	}
}
