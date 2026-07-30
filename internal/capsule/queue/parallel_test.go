package queue

import "testing"

// queueIntegrationTestSlots bounds the real-Git integration fixtures. They are
// independent and benefit substantially from running concurrently, but letting
// testing's machine-wide default parallelism launch every clone and Git process
// at once creates the same local resource contention the queue is meant to
// prevent.
var queueIntegrationTestSlots = make(chan struct{}, 4)

func parallelQueueIntegrationTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	queueIntegrationTestSlots <- struct{}{}
	t.Cleanup(func() { <-queueIntegrationTestSlots })
}
