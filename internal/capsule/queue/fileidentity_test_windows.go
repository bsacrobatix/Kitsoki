//go:build windows

package queue

import "os"

func fileOwner(_ os.FileInfo) (int, int, bool) { return 0, 0, false }
