//go:build !darwin && !linux && !windows

package queue

import (
	"context"
	"fmt"
	"os/exec"
)

type gateBorrowMarker struct {
	ownerPID    int
	custodyPath string
}

func inheritedGateBorrow(_, _, _ string) (gateBorrowMarker, bool) {
	return gateBorrowMarker{}, false
}

func gateBorrowAlive(gateBorrowMarker) bool { return false }

func runSupervisedGateCommand(context.Context, *exec.Cmd, *FileGateLease) error {
	return fmt.Errorf("queue: crash-safe gate process-group supervision is unavailable on this platform")
}
