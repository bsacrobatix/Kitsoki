package host

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestApplicationMaintenanceBuiltinsFailClosedAndHaveExactSchemas(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	for _, test := range []struct {
		verb          string
		namespace     string
		class         effect.Effect
		deterministic bool
	}{
		{SessionReconciliationVerb, "host.session_reconciliation", effect.Write, false},
		{WorkerFleetVerb, "host.worker_fleet", effect.External, false},
		{CampaignSupervisionVerb, "host.campaign_supervision", effect.Read, true},
	} {
		result, err := registry.Invoke(context.Background(), test.verb, nil)
		require.NoError(t, err)
		assert.Contains(t, result.Error, "unavailable")
		class, deterministic := ClassifyDispatchedCall(test.verb, nil)
		assert.Equal(t, test.class, class)
		assert.Equal(t, test.deterministic, deterministic)
		spec, ok := opschema.Builtins().Lookup(test.namespace, "reconcile")
		require.True(t, ok)
		assert.Empty(t, spec.Input)
		assert.Equal(t, "object", spec.Output["receipt"].Type)
	}
}

func TestApplicationMaintenanceRejectsAllCallerAuthority(t *testing.T) {
	called := false
	handler := NewApplicationMaintenanceHandler(
		SessionReconciliationVerb,
		func(context.Context) (map[string]any, error) {
			called = true
			return map[string]any{"status": "ok"}, nil
		},
	)
	for _, args := range []map[string]any{
		{"path": "/tmp/private"},
		{"url": "https://example.invalid"},
		{"command": "run"},
		{"provider": "live"},
		{"credential": "secret"},
		{"application_id": "other"},
		{"actor": "forged"},
		{"session": "forged"},
		{"transport": "http"},
		{"max_sessions": 1},
	} {
		_, err := handler(context.Background(), args)
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "accepts no caller authority"))
	}
	assert.False(t, called)

	result, err := handler(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Data["receipt"].(map[string]any)["status"])
}
