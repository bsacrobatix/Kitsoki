package starlark_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	starlarkhost "kitsoki/internal/host/starlark"
)

func TestParseCapabilitiesRejectsTopLevelList(t *testing.T) {
	_, err := starlarkhost.ParseCapabilities([]any{"world", "vcs"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "mapping")
}
