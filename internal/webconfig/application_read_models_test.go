package webconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplicationReadModelsLoadExactDaemonBindings(t *testing.T) {
	cfg, err := loadConfigText(t, `
application_read_models:
  pog-application:
    streams:
      scope: pog
    federation: true
    materialization: true
`)
	require.NoError(t, err)
	binding, ok := cfg.ApplicationReadModels["pog-application"]
	require.True(t, ok)
	require.NotNil(t, binding.Streams)
	assert.Equal(t, "pog", binding.Streams.Scope)
	assert.True(t, binding.Federation)
	assert.True(t, binding.Materialization)
}

func TestApplicationReadModelsRejectPathsAndAmbiguousMaterialization(t *testing.T) {
	for _, body := range []string{
		"application_read_models:\n  ../pog:\n    federation: true\n",
		"application_read_models:\n  pog:\n    streams:\n      scope: /tmp/private\n",
		"application_read_models:\n  pog:\n    streams:\n      scope: project%2Fprivate\n",
		"application_read_models:\n  ' pog ':\n    federation: true\n",
		"application_read_models:\n  pog:\n    materialization: true\n  other:\n    materialization: true\n",
	} {
		_, err := loadConfigText(t, body)
		require.Error(t, err)
		assert.True(t,
			strings.Contains(err.Error(), "opaque") ||
				strings.Contains(err.Error(), "ambiguous"),
			"unexpected error: %v", err,
		)
	}
}

func TestApplicationReadModelsLocalConfigMergesByApplication(t *testing.T) {
	base := WebConfig{ApplicationReadModels: map[string]ApplicationReadModelConfig{
		"app-a": {Federation: true},
	}}
	local := WebConfig{ApplicationReadModels: map[string]ApplicationReadModelConfig{
		"app-b": {Materialization: true},
	}}
	merged := mergeConfig(base, local)
	assert.Len(t, merged.ApplicationReadModels, 2)
	assert.True(t, merged.ApplicationReadModels["app-a"].Federation)
	assert.True(t, merged.ApplicationReadModels["app-b"].Materialization)
}
