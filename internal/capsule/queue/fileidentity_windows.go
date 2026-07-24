//go:build windows

package queue

import "os"

func preserveFileOwner(_ *os.File, _ os.FileInfo) error { return nil }

func fileOwner(_ os.FileInfo) (int, int, bool) { return 0, 0, false }
