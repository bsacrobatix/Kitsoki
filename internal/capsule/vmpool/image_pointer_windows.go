//go:build windows

package vmpool

import (
	"fmt"
	"os"
)

func openTrustedImagePointer(path string, _ uint32) (*os.File, int64, error) {
	return nil, 0, fmt.Errorf("vmpool: worker image pointer %s: strict root-owned pointer mode is unsupported on windows", path)
}
