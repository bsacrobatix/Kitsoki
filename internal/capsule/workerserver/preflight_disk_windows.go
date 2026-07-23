//go:build windows

package workerserver

// preflightDiskFree has no Windows implementation (Capsule workers run on
// Linux droplets; Windows is a local-dev-only build target for this
// package). known=false so preflightDiskHeadroom treats it as "cannot
// answer" rather than failing closed.
func preflightDiskFree(string) (freeBytes int64, known bool, err error) {
	return 0, false, nil
}
