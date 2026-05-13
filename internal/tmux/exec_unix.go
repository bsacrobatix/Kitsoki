//go:build unix

package tmux

import (
	"os"
	"syscall"
)

// execProcess replaces the current process image with bin running
// argv (argv[0] should be the program's own name, conventionally
// matching bin's basename — tmux reads argv[0] for diagnostics).
// On success it does not return.
func execProcess(bin string, argv []string) error {
	return syscall.Exec(bin, argv, os.Environ())
}
