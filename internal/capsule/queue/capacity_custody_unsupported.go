//go:build !darwin && !linux

package queue

import (
	"fmt"
	"os"
)

func acquireGateCustody(string) (*os.File, error) {
	return nil, fmt.Errorf("queue: kernel-bound gate custody is unavailable on this platform")
}
