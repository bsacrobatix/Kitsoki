package queue

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates default gate capacity from concurrently running package
// binaries. Individual tests that intentionally simulate an in-process crash
// still inject their own admission because a live goroutine does not release a
// kernel flock the way a dead worker process does.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "kitsoki-queue-test-capacity-*")
	if err != nil {
		panic(err)
	}
	defaultGateCapacityRootOverride = filepath.Clean(root)
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
