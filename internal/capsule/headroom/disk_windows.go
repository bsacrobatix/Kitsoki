//go:build windows

package headroom

import "fmt"

func freeBytes(string) (int64, error) {
	return 0, fmt.Errorf("local disk probe is unsupported on windows")
}
