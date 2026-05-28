//go:build !unix

package tmux

import "fmt"

// execProcess is not implementable on non-unix platforms — Windows
// has no posix exec replacement. kitsoki chat attach is unsupported
// on Windows; AttachStreaming (fork-exec) is the only viable path.
func execProcess(bin string, argv []string) error {
	return fmt.Errorf("tmux.execProcess: not supported on this platform; use AttachStreaming")
}
