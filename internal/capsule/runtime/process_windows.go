//go:build windows

package runtime

import (
	"context"
	"os/exec"
)

func configureProcessGroup(*exec.Cmd) {}
func stopProcessGroup(_ context.Context, cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
