package host

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

func requiredSnapshotBound(args map[string]any, handler, name string, ceiling int) (int, error) {
	raw, ok := args[name]
	if !ok {
		return 0, fmt.Errorf("%s: missing required arg %q", handler, name)
	}
	var value int
	switch v := raw.(type) {
	case int:
		value = v
	case int64:
		if int64(int(v)) != v {
			return 0, fmt.Errorf("%s: %q is outside the supported integer range", handler, name)
		}
		value = int(v)
	case float64:
		if math.Trunc(v) != v || v > float64(math.MaxInt) || v < float64(math.MinInt) {
			return 0, fmt.Errorf("%s: %q must be an integer", handler, name)
		}
		value = int(v)
	default:
		return 0, fmt.Errorf("%s: %q must be an integer, got %T", handler, name, raw)
	}
	if value < 1 || value > ceiling {
		return 0, fmt.Errorf("%s: %q must be between 1 and %d, got %d", handler, name, ceiling, value)
	}
	return value, nil
}

func rejectSnapshotArgs(args map[string]any, handler string, allowed ...string) error {
	allow := map[string]bool{"op": true}
	for _, key := range allowed {
		allow[key] = true
	}
	for key := range args {
		if !allow[key] {
			return fmt.Errorf("%s: unsupported arg %q", handler, key)
		}
	}
	return nil
}

func boundedSnapshotResult(handler string, snapshot map[string]any, maxBytes int) (Result, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return Result{}, fmt.Errorf("%s: encode size check: %w", handler, err)
	}
	if len(raw) > maxBytes {
		return Result{}, fmt.Errorf("%s: encoded snapshot is %d bytes, exceeds max_bytes %d; refusing to truncate", handler, len(raw), maxBytes)
	}
	return Result{Data: map[string]any{"snapshot": snapshot}}, nil
}

func safeSemantic(value string, max int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.ContainsAny(value, `\`) &&
		!strings.HasPrefix(value, "/") &&
		!strings.Contains(lower, "://") &&
		!strings.Contains(lower, "%2f") &&
		!strings.Contains(lower, "%5c")
}
