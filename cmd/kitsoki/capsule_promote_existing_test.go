package main

import "testing"

func TestCapsulePromoteExistingExposesNoWaiverOrDirectFinalizationFlags(t *testing.T) {
	command := capsulePromoteExistingCmd()
	for _, forbidden := range []string{"skip-tests", "wait", "override"} {
		if command.Flags().Lookup(forbidden) != nil {
			t.Fatalf("promote-existing unexpectedly exposes --%s", forbidden)
		}
	}
	for _, required := range []string{"project", "source-target", "sha", "target", "pipeline", "gate", "definition"} {
		if command.Flags().Lookup(required) == nil {
			t.Fatalf("promote-existing is missing --%s", required)
		}
	}
}
