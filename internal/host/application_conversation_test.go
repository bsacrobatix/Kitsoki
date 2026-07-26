package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/applicationconversation"
	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestApplicationConversationHandlerRejectsEveryExtraArgument(t *testing.T) {
	handler := NewApplicationConversationHandler(&applicationconversation.Service{})
	result, err := handler(context.Background(), map[string]any{
		"chat_id": "chat", "question": "question", "provider": "caller-choice",
	})
	require.NoError(t, err)
	require.Contains(t, result.Error, "only chat_id and question")

	result, err = handler(context.Background(), map[string]any{
		"chat_id": 42, "question": "question",
	})
	require.NoError(t, err)
	require.Contains(t, result.Error, "must be strings")
}

func TestApplicationConversationBuiltinSchemaAndEffect(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	handler, ok := registry.Get("host.application_conversation.ask")
	require.True(t, ok)
	result, err := handler(context.Background(), map[string]any{
		"chat_id": "chat", "question": "question",
	})
	require.NoError(t, err)
	require.Contains(t, result.Error, "configured daemon binding is unavailable")

	class, deterministic := ClassifyDispatchedCall("host.application_conversation.ask", nil)
	require.Equal(t, effect.External, class)
	require.False(t, deterministic)

	spec, ok := opschema.Builtins().Lookup("host.application_conversation", "ask")
	require.True(t, ok)
	require.Equal(t, "string", spec.Input["chat_id"].Type)
	require.Equal(t, "string", spec.Input["question"].Type)
	require.Len(t, spec.Input, 2)
}
