package mcp

// state.go — read-side counterparts to persistStateLocked and
// writeOutputAtomically in validator.go. Hosted here so callers in the
// host and harness packages share one source of truth for the on-disk
// shape.

import (
	"encoding/json"
	"errors"
	"os"
)

// ReadStateFile reads the validator's persisted counters; missing or
// malformed files yield zero values so the abandonment-recovery loop can
// treat "no signal" as "treat as abandoned".
func ReadStateFile(path string) (attempts, successfulSubmits int, lastError string) {
	if path == "" {
		return 0, 0, ""
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return 0, 0, ""
	}
	var st validatorState
	if jErr := json.Unmarshal(data, &st); jErr != nil {
		return 0, 0, ""
	}
	return st.Attempts, st.SuccessfulSubmits, st.LastError
}

// ReadCapturedPayload returns the raw JSON written by the validator's
// last successful submit; absent or empty files return (nil, nil).
func ReadCapturedPayload(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return data, nil
}
