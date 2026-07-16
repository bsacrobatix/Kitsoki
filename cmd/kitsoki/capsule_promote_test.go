package main

import "testing"

func TestCapsulePromoteExposesExplicitEmergencySkipTestsFlag(t *testing.T) {
	flag := capsulePromoteCmd().Flags().Lookup("skip-tests")
	if flag == nil {
		t.Fatal("capsule promote is missing --skip-tests")
	}
	if flag.DefValue != "false" {
		t.Fatalf("skip-tests default = %q", flag.DefValue)
	}
}
