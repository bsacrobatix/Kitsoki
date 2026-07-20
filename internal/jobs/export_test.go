package jobs

// SetSweepProcessAliveForTest substitutes the owner-liveness probe used by
// SweepStaleJobs and returns a restore func, so external-package tests can
// simulate dead and live owners deterministically.
func SetSweepProcessAliveForTest(f func(int) bool) func() {
	orig := sweepProcessAlive
	sweepProcessAlive = f
	return func() { sweepProcessAlive = orig }
}
