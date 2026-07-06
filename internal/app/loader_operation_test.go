package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperationValidation(t *testing.T) {
	t.Run("commit_outside_operation_rejected", func(t *testing.T) {
		yaml := `
app: { id: op-test, version: 0.1.0 }
root: start
intents: { go: {} }
states:
  start:
    on:
      go:
        - target: start
          effects:
            - commit_operation:
                world: { result: "ok" }
`
		_, err := LoadBytes([]byte(yaml))
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "commit_operation is only valid inside a state with operation:"), err.Error())
	})

	t.Run("persist_draft_requires_id", func(t *testing.T) {
		yaml := `
app: { id: op-test, version: 0.1.0 }
root: start
intents: { go: {} }
states:
  start:
    operation: { scope: demo }
    on:
      go:
        - target: start
          effects:
            - persist_draft:
                world: [result]
`
		_, err := LoadBytes([]byte(yaml))
		require.Error(t, err)
		require.True(t, strings.Contains(err.Error(), "persist_draft requires id:"), err.Error())
	})
}
