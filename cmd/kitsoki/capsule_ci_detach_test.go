package main

import (
	"strings"
	"testing"
)

// TestCapsuleCIRunHelpAdvertisesDetach pins the capability-probe contract:
// downstream dispatchers (POG's feedback-dispatch backend) detect detached
// pool execution support by grepping `capsule ci run --help` for the literal
// string "--detach". Renaming or removing the flag breaks that probe.
func TestCapsuleCIRunHelpAdvertisesDetach(t *testing.T) {
	out, err := execRoot(t, "capsule", "ci", "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--detach") {
		t.Fatalf("capsule ci run --help does not advertise --detach:\n%s", out)
	}
	if !strings.Contains(out, "capsule ci status --job <id> --refresh") {
		t.Fatalf("--detach help does not point at the status --refresh reconciliation path:\n%s", out)
	}
}
