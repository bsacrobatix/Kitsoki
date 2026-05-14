package host

import (
	"context"
	"encoding/json"
)

// SetAskStructuredForTest swaps the AskStructured implementation for tests
// and returns a restore function the test should defer.
func SetAskStructuredForTest(fn func(ctx context.Context, opts AskStructuredOptions) (json.RawMessage, error)) (restore func()) {
	prev := askStructuredFunc
	askStructuredFunc = fn
	return func() { askStructuredFunc = prev }
}
