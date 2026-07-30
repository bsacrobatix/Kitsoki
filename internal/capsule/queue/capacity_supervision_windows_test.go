//go:build windows

package queue

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestGateSupervisionFailsClosedWhenProcessGroupsAreUnsupported(t *testing.T) {
	err := runSupervisedGateCommand(context.Background(), exec.Command("cmd", "/c", "exit", "0"), &FileGateLease{})
	if err == nil || !strings.Contains(err.Error(), "unavailable on windows") {
		t.Fatalf("unsupported supervision error=%v, want fail-closed diagnostic", err)
	}
}
