package main

import "testing"

func TestCapsuleReleaseFrontDoor(t *testing.T) {
	cmd := capsuleReleaseCmd()
	for _, name := range []string{"plan", "create", "show"} {
		if found, _, err := cmd.Find([]string{name}); err != nil || found == cmd {
			t.Fatalf("release command missing %q: %v", name, err)
		}
	}
}
