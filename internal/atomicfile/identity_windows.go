//go:build windows

package atomicfile

import "os"

func inheritFileIdentity(_ *os.File, _ os.FileInfo) error { return nil }
func inheritPathIdentity(_ string, _ os.FileInfo) error   { return nil }
