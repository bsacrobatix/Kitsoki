// Package app — duration parser shared by Timeout.After and any other
// place where authors want to write `10d` for ten days.
//
// time.ParseDuration accepts "ns", "us", "ms", "s", "m", "h" but not "d".
// The Oregon Trail use case writes durations in game-days, so we accept
// `Nd` as a small extension: `10d` → 10*24h = 240h.  Other Go-std units
// pass through to time.ParseDuration unchanged.
package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses a duration string with extended day support.
//
// Accepted forms:
//   - Any value time.ParseDuration accepts ("10s", "5m", "1h30m", "100ms", ...).
//   - "Nd" or "N.Nd" — N days, coerced to N*24h.  Must be a single
//     trailing `d` token; mixed units like "1d2h" are intentionally
//     rejected to keep the grammar small and unambiguous.
//
// Empty input returns 0 with no error so optional Timeout fields can be
// omitted in YAML without a parse error.  Use the returned non-zero value
// in callers to detect "actually configured".
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	// Trailing 'd' → days.  Reject if any other unit appears in the same
	// token (e.g. "1d2h") so the grammar stays unambiguous.
	if strings.HasSuffix(s, "d") {
		num := strings.TrimSuffix(s, "d")
		// Reject anything that already contains another unit suffix.
		for _, u := range []string{"ns", "us", "µs", "ms", "s", "m", "h"} {
			if strings.Contains(num, u) {
				return 0, fmt.Errorf("app.ParseDuration: %q mixes days with other units; declare days alone (e.g. \"10d\")", s)
			}
		}
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("app.ParseDuration: %q: invalid day count: %w", s, err)
		}
		return time.Duration(f * float64(24*time.Hour)), nil
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("app.ParseDuration: %q: %w", s, err)
	}
	return d, nil
}
