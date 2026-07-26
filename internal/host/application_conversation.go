package host

import (
	"context"
	"encoding/json"
	"fmt"

	"kitsoki/internal/applicationconversation"
)

type isolatedAgentInvocationKey struct{}

// WithIsolatedAgentInvocation removes ambient editor, visual, plugin, Studio,
// and operator-ask capabilities from AgentAskHandler. The caller must still
// inject a tool-free Agent.
func WithIsolatedAgentInvocation(ctx context.Context) context.Context {
	return context.WithValue(ctx, isolatedAgentInvocationKey{}, true)
}

func isolatedAgentInvocation(ctx context.Context) bool {
	value, _ := ctx.Value(isolatedAgentInvocationKey{}).(bool)
	return value
}

// ApplicationConversationHandler is the fail-closed builtin. Daemon session
// construction replaces it only for caller applications with an exact binding.
func ApplicationConversationHandler(
	_ context.Context,
	_ map[string]any,
) (Result, error) {
	return Result{Error: "host.application_conversation.ask: configured daemon binding is unavailable"}, nil
}

// NewApplicationConversationHandler binds the exact two-field story contract
// to a daemon-owned application conversation service.
func NewApplicationConversationHandler(service *applicationconversation.Service) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if service == nil {
			return Result{Error: "host.application_conversation.ask: backing service is unavailable"}, nil
		}
		if len(args) != 2 {
			return Result{Error: "host.application_conversation.ask: only chat_id and question are accepted"}, nil
		}
		chatID, chatOK := args["chat_id"].(string)
		question, questionOK := args["question"].(string)
		if !chatOK || !questionOK {
			return Result{Error: "host.application_conversation.ask: chat_id and question must be strings"}, nil
		}
		result, err := service.Ask(ctx, chatID, question)
		if err != nil {
			return Result{}, fmt.Errorf("host.application_conversation.ask: %w", err)
		}
		receiptRaw, err := json.Marshal(result.Receipt)
		if err != nil {
			return Result{}, fmt.Errorf("host.application_conversation.ask: encode receipt: %w", err)
		}
		var receipt map[string]any
		if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
			return Result{}, fmt.Errorf("host.application_conversation.ask: project receipt: %w", err)
		}
		return Result{Data: map[string]any{
			"answer":           result.Answer,
			"conversation_ref": result.ConversationRef,
			"turn_ref":         result.TurnRef,
			"receipt":          receipt,
			"replayed":         result.Replayed,
		}}, nil
	}
}
