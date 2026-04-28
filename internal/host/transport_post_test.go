package host_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hally/internal/host"
	"hally/internal/transport"
)

func TestTransportPost_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	if _, ok := r.Get("host.transport.post"); !ok {
		t.Fatal("host.transport.post was not registered by RegisterBuiltins")
	}
}

func TestTransportPost_NoRegistryInContext(t *testing.T) {
	res, err := host.TransportPostHandler(context.Background(), map[string]any{
		"transport": "tui",
		"thread":    "S-1",
		"body":      "x",
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Error)
}

func TestTransportPost_DispatchesIntoRegistry(t *testing.T) {
	tt := transport.NewTUITransport()
	reg := transport.NewRegistry()
	reg.Register(tt)
	t.Cleanup(func() { _ = reg.Close() })

	ctx := transport.WithRegistry(context.Background(), reg)

	res, err := host.TransportPostHandler(ctx, map[string]any{
		"transport": "tui",
		"thread":    "S-1",
		"phase_id":  "phase_1",
		"title":     "Reproduction",
		"body":      "Step 1: ...",
	})
	require.NoError(t, err)
	require.Empty(t, res.Error, "no error expected")
	require.NotEmpty(t, res.Data["message_id"])

	posts := tt.Drain()
	require.Len(t, posts, 1)
	assert.Equal(t, "phase_1", posts[0].Msg.PhaseID)
	assert.Equal(t, "Reproduction", posts[0].Msg.Title)
	assert.Equal(t, "Step 1: ...", posts[0].Msg.Body)
	assert.Equal(t, "tui", posts[0].Key.Transport)
	assert.Equal(t, "S-1", posts[0].Key.Thread)
	assert.Equal(t, transport.DefaultBotMarker, posts[0].Msg.BotMarker)
}

func TestTransportPost_TransportNotRegistered(t *testing.T) {
	reg := transport.NewRegistry()
	t.Cleanup(func() { _ = reg.Close() })
	ctx := transport.WithRegistry(context.Background(), reg)

	res, err := host.TransportPostHandler(ctx, map[string]any{
		"transport": "missing",
		"thread":    "S-1",
		"body":      "x",
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Error)
}

func TestTransportPost_RequiredArgs(t *testing.T) {
	reg := transport.NewRegistry()
	reg.Register(transport.NewTUITransport())
	t.Cleanup(func() { _ = reg.Close() })
	ctx := transport.WithRegistry(context.Background(), reg)

	cases := []map[string]any{
		{"thread": "S-1", "body": "x"},                // missing transport
		{"transport": "tui", "body": "x"},             // missing thread
	}
	for _, args := range cases {
		res, err := host.TransportPostHandler(ctx, args)
		require.NoError(t, err)
		assert.NotEmpty(t, res.Error)
	}
}
