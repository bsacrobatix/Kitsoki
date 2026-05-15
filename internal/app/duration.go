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

// Duration is a time.Duration that round-trips through YAML as a string
// using ParseDuration (so `30d` and `1h30m` both parse). Authors can
// write the field in any form ParseDuration accepts; downstream code
// reads the underlying time.Duration via `time.Duration(d)`. A zero
// Duration is the natural "not set" — callers that need to distinguish
// "author wrote 0" from "author wrote nothing" should use a *Duration.
type Duration time.Duration

// UnmarshalYAML implements goccy/go-yaml's BytesUnmarshaler. The raw
// bytes may be a quoted scalar (`"30d"`), an unquoted scalar (`30d`),
// or an empty string (a missing/explicit-empty value, which becomes 0).
func (d *Duration) UnmarshalYAML(b []byte) error {
	s := strings.TrimSpace(string(b))
	// Strip a single layer of surrounding quotes if present — go-yaml
	// hands us the raw scalar bytes, which include quotes for quoted
	// strings.
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"':
			s = s[1 : len(s)-1]
		case s[0] == '\'' && s[len(s)-1] == '\'':
			s = s[1 : len(s)-1]
		}
	}
	parsed, err := ParseDuration(s)
	if err != nil {
		return fmt.Errorf("app.Duration: %w", err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML emits the canonical string form. Used for snapshots /
// debugging — the loader never writes YAML in production paths.
func (d Duration) MarshalYAML() (interface{}, error) {
	if d == 0 {
		return "", nil
	}
	return time.Duration(d).String(), nil
}

// AsTimeDuration returns the underlying time.Duration. Provided as a
// convenience so callers don't need to spell out the type conversion.
func (d Duration) AsTimeDuration() time.Duration { return time.Duration(d) }

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
