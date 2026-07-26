package applicationconversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Service struct {
	ApplicationID string
	BindingDigest string
	Bounds        Bounds
	Store         TurnStore
	Chats         ChatStore
	Graph         GraphSource
	Runner        Runner
}

func (s Service) Ask(ctx context.Context, chatID, question string) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	if !validOpaqueID(chatID, 128) {
		return Result{}, fmt.Errorf("chat_id must be a bounded opaque identity")
	}
	if strings.TrimSpace(question) == "" {
		return Result{}, fmt.Errorf("question is required")
	}
	if len(question) > s.Bounds.MaxQuestionBytes {
		return Result{}, fmt.Errorf(
			"question encodes to %d bytes, exceeding %d",
			len(question),
			s.Bounds.MaxQuestionBytes,
		)
	}

	scopeKey := "application-conversation:" + digest(
		"scope/v1", s.ApplicationID, s.BindingDigest, chatID,
	)
	chat, _, err := s.Chats.Resolve(
		ctx,
		s.ApplicationID,
		RoomID,
		scopeKey,
		"Application conversation",
	)
	if err != nil {
		return Result{}, fmt.Errorf("resolve application conversation: %w", err)
	}
	if chat == nil || chat.AppID != s.ApplicationID || chat.Room != RoomID ||
		chat.ScopeKey != scopeKey {
		return Result{}, fmt.Errorf("resolved chat violates application conversation scope")
	}

	var result Result
	err = s.Chats.WithLock(ctx, chat.ID, func(lockCtx context.Context) error {
		var inner error
		result, inner = s.askLocked(lockCtx, chat.ID, question)
		return inner
	})
	if err != nil {
		return Result{}, fmt.Errorf("run application conversation: %w", err)
	}
	return result, nil
}

func (s Service) askLocked(ctx context.Context, chatRef, question string) (Result, error) {
	questionDigest := digest("question/v1", question)
	latest, latestErr := s.Store.Latest(ctx, s.ApplicationID, chatRef)
	switch {
	case latestErr == nil && latest.Status == TurnCompleted &&
		latest.QuestionDigest == questionDigest:
		return s.replay(latest)
	case latestErr == nil && latest.Status != TurnCompleted &&
		latest.QuestionDigest != questionDigest:
		return Result{}, fmt.Errorf(
			"conversation has an interrupted or active turn; retry its original question first",
		)
	case latestErr != nil && !errors.Is(latestErr, ErrTurnNotFound):
		return Result{}, fmt.Errorf("load latest conversation turn: %w", latestErr)
	}

	var turn Turn
	if latestErr == nil && latest.Status != TurnCompleted {
		turn = latest
		if turn.Status == TurnPending {
			return Result{}, fmt.Errorf("conversation turn %q is already in progress", turn.Ref)
		}
		if turn.Status == TurnInterrupted {
			var err error
			turn, err = s.Store.Begin(ctx, turn)
			if err != nil {
				return Result{}, fmt.Errorf("resume interrupted conversation turn: %w", err)
			}
		}
	} else {
		predecessor := ""
		if latestErr == nil {
			predecessor = latest.Ref
		}
		turn = Turn{
			Ref: digestRef(
				"application-conversation-turn-",
				"turn/v1",
				s.ApplicationID,
				chatRef,
				s.BindingDigest,
				predecessor,
				questionDigest,
			),
			ApplicationID:  s.ApplicationID,
			ChatRef:        chatRef,
			BindingDigest:  s.BindingDigest,
			PredecessorRef: predecessor,
			QuestionDigest: questionDigest,
			Status:         TurnPending,
		}
		var err error
		turn, err = s.Store.Begin(ctx, turn)
		if err != nil {
			return Result{}, fmt.Errorf("begin conversation turn: %w", err)
		}
		if turn.Status == TurnCompleted {
			return s.replay(turn)
		}
	}

	if turn.Status == TurnAnswerReady {
		return s.finishAnswer(ctx, turn)
	}

	if turn.UserSeq == nil {
		if seq := s.findTurnMessage(ctx, chatRef, turn.Ref, "user"); seq != nil {
			if err := s.Store.SetUserSeq(ctx, turn.Ref, *seq); err != nil {
				return Result{}, err
			}
			turn.UserSeq = seq
		} else {
			message, err := s.Chats.AppendMessage(ctx, chatRef, "user", question, map[string]any{
				"turn_ref":        turn.Ref,
				"question_digest": questionDigest,
			})
			if err != nil {
				_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "persist_user_failed")
				return Result{}, fmt.Errorf("persist conversation question: %w", err)
			}
			if err := s.Store.SetUserSeq(ctx, turn.Ref, message.Seq); err != nil {
				_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "persist_user_ref_failed")
				return Result{}, fmt.Errorf("persist conversation question reference: %w", err)
			}
			turn.UserSeq = &message.Seq
		}
	}

	messages, truncated, err := s.history(ctx, chatRef)
	if err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "history_failed")
		return Result{}, err
	}
	graph, err := s.Graph.Snapshot(ctx)
	if err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "graph_failed")
		return Result{}, fmt.Errorf("load configured conversation graph: %w", err)
	}
	if graph.Digest == "" || !json.Valid(graph.JSON) {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "graph_invalid")
		return Result{}, fmt.Errorf("configured conversation graph returned an invalid snapshot")
	}
	if len(graph.JSON) > s.Bounds.MaxGraphBytes {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "graph_too_large")
		return Result{}, fmt.Errorf(
			"configured graph snapshot encodes to %d bytes, exceeding %d",
			len(graph.JSON),
			s.Bounds.MaxGraphBytes,
		)
	}

	answer, err := s.Runner.Run(ctx, RunRequest{
		Graph: graph, Messages: messages, HistoryTruncated: truncated,
	})
	if err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "runner_failed")
		return Result{}, fmt.Errorf("run configured conversation provider: %w", err)
	}
	if strings.TrimSpace(answer) == "" {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "empty_answer")
		return Result{}, fmt.Errorf("configured conversation provider returned an empty answer")
	}
	if len(answer) > s.Bounds.MaxAnswerBytes {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "answer_too_large")
		return Result{}, fmt.Errorf(
			"answer encodes to %d bytes, exceeding %d",
			len(answer),
			s.Bounds.MaxAnswerBytes,
		)
	}
	if err := s.Store.StoreAnswer(ctx, turn.Ref, answer, graph.Digest); err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), turn.Ref, "persist_answer_failed")
		return Result{}, fmt.Errorf("persist conversation answer: %w", err)
	}
	turn.Answer = answer
	turn.GraphDigest = graph.Digest
	turn.Status = TurnAnswerReady
	return s.finishAnswer(ctx, turn)
}

func (s Service) finishAnswer(ctx context.Context, turn Turn) (Result, error) {
	if turn.Answer == "" {
		stored, err := s.Store.Get(ctx, turn.Ref)
		if err != nil {
			return Result{}, err
		}
		turn = stored
	}
	if turn.UserSeq == nil {
		if seq := s.findTurnMessage(ctx, turn.ChatRef, turn.Ref, "user"); seq != nil {
			if err := s.Store.SetUserSeq(ctx, turn.Ref, *seq); err != nil {
				return Result{}, err
			}
			turn.UserSeq = seq
		}
	}
	if turn.UserSeq == nil {
		return Result{}, fmt.Errorf("conversation answer has no durable user message reference")
	}
	if turn.AssistantSeq == nil {
		if seq := s.findTurnMessage(ctx, turn.ChatRef, turn.Ref, "assistant"); seq != nil {
			if err := s.Store.SetAssistantSeq(ctx, turn.Ref, *seq); err != nil {
				return Result{}, err
			}
			turn.AssistantSeq = seq
		} else {
			answerDigest := digest("answer/v1", turn.Answer)
			message, err := s.Chats.AppendMessage(
				ctx,
				turn.ChatRef,
				"assistant",
				turn.Answer,
				map[string]any{"turn_ref": turn.Ref, "answer_digest": answerDigest},
			)
			if err != nil {
				return Result{}, fmt.Errorf("persist conversation response: %w", err)
			}
			if err := s.Store.SetAssistantSeq(ctx, turn.Ref, message.Seq); err != nil {
				return Result{}, fmt.Errorf("persist conversation response reference: %w", err)
			}
			turn.AssistantSeq = &message.Seq
		}
	}

	if turn.GraphDigest == "" {
		return Result{}, fmt.Errorf("conversation answer has no durable graph digest")
	}
	receipt := Receipt{
		Schema:          ReceiptSchema,
		ApplicationID:   s.ApplicationID,
		ConversationRef: turn.ChatRef,
		TurnRef:         turn.Ref,
		PredecessorRef:  turn.PredecessorRef,
		BindingDigest:   s.BindingDigest,
		GraphDigest:     turn.GraphDigest,
		QuestionDigest:  turn.QuestionDigest,
		AnswerDigest:    digest("answer/v1", turn.Answer),
		UserSeq:         *turn.UserSeq,
		AssistantSeq:    *turn.AssistantSeq,
		Status:          string(TurnCompleted),
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return Result{}, fmt.Errorf("encode conversation receipt: %w", err)
	}
	if len(raw) > 256*1024 {
		return Result{}, fmt.Errorf("conversation receipt exceeds 256 KiB")
	}
	if err := s.Store.Complete(ctx, turn.Ref, raw); err != nil {
		return Result{}, fmt.Errorf("complete conversation turn: %w", err)
	}
	return Result{
		Answer: turn.Answer, ConversationRef: turn.ChatRef, TurnRef: turn.Ref,
		Receipt: receipt,
	}, nil
}

func (s Service) replay(turn Turn) (Result, error) {
	if turn.Status != TurnCompleted || turn.Answer == "" || turn.GraphDigest == "" ||
		turn.UserSeq == nil || turn.AssistantSeq == nil || len(turn.ReceiptJSON) == 0 {
		return Result{}, fmt.Errorf("completed conversation turn %q is incomplete", turn.Ref)
	}
	if len(turn.Answer) > s.Bounds.MaxAnswerBytes || len(turn.ReceiptJSON) > 256*1024 {
		return Result{}, fmt.Errorf("stored conversation replay exceeds configured bounds")
	}
	var receipt Receipt
	if err := json.Unmarshal(turn.ReceiptJSON, &receipt); err != nil {
		return Result{}, fmt.Errorf("decode stored conversation receipt: %w", err)
	}
	if receipt.Schema != ReceiptSchema ||
		receipt.ApplicationID != s.ApplicationID ||
		receipt.ConversationRef != turn.ChatRef ||
		receipt.TurnRef != turn.Ref ||
		receipt.PredecessorRef != turn.PredecessorRef ||
		receipt.BindingDigest != s.BindingDigest ||
		receipt.GraphDigest != turn.GraphDigest ||
		receipt.QuestionDigest != turn.QuestionDigest ||
		receipt.AnswerDigest != digest("answer/v1", turn.Answer) ||
		receipt.UserSeq != *turn.UserSeq ||
		receipt.AssistantSeq != *turn.AssistantSeq ||
		receipt.Status != string(TurnCompleted) {
		return Result{}, fmt.Errorf("stored conversation receipt failed integrity validation")
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(canonical, turn.ReceiptJSON) {
		return Result{}, fmt.Errorf("stored conversation receipt is not canonical")
	}
	return Result{
		Answer: turn.Answer, ConversationRef: turn.ChatRef, TurnRef: turn.Ref,
		Receipt: receipt, Replayed: true,
	}, nil
}

func (s Service) history(ctx context.Context, chatRef string) ([]Message, bool, error) {
	transcript, err := s.Chats.Transcript(ctx, chatRef, 0)
	if err != nil {
		return nil, false, fmt.Errorf("load conversation history: %w", err)
	}
	out := make([]Message, 0, s.Bounds.MaxHistoryEntries)
	bytes := 0
	truncated := false
	for index := len(transcript) - 1; index >= 0; index-- {
		message := transcript[index]
		nextBytes := len(message.Role) + len(message.Content)
		if len(out) == s.Bounds.MaxHistoryEntries || bytes+nextBytes > s.Bounds.MaxHistoryBytes {
			truncated = true
			break
		}
		out = append(out, Message{Role: message.Role, Content: message.Content})
		bytes += nextBytes
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("configured history bounds exclude the current question")
	}
	return out, truncated, nil
}

func (s Service) findTurnMessage(ctx context.Context, chatRef, turnRef, role string) *int {
	messages, err := s.Chats.Transcript(ctx, chatRef, 0)
	if err != nil {
		return nil
	}
	for index := range messages {
		message := messages[index]
		if message.Role != role || message.Metadata["turn_ref"] != turnRef {
			continue
		}
		seq := message.Seq
		return &seq
	}
	return nil
}

func (s Service) validate() error {
	switch {
	case !validOpaqueID(s.ApplicationID, 180):
		return fmt.Errorf("application conversation application id is invalid")
	case !validOpaqueID(s.BindingDigest, 180):
		return fmt.Errorf("application conversation binding digest is invalid")
	case s.Store == nil:
		return fmt.Errorf("application conversation durable store is unavailable")
	case s.Chats == nil:
		return fmt.Errorf("application conversation chat store is unavailable")
	case s.Graph == nil:
		return fmt.Errorf("application conversation graph source is unavailable")
	case s.Runner == nil:
		return fmt.Errorf("application conversation runner is unavailable")
	default:
		return s.Bounds.Validate()
	}
}

func digest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func digestRef(prefix string, parts ...string) string {
	value := strings.TrimPrefix(digest(parts...), "sha256:")
	return prefix + value[:32]
}
