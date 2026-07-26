package applicationconversation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/chats"
	"kitsoki/internal/clock"
	"kitsoki/internal/store"
)

type fixedGraph struct {
	snapshot GraphSnapshot
}

func (g fixedGraph) Snapshot(context.Context) (GraphSnapshot, error) {
	return g.snapshot, nil
}

type recordingRunner struct {
	mu        sync.Mutex
	calls     []RunRequest
	responses []string
	errors    []error
}

func (r *recordingRunner) Run(_ context.Context, request RunRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, request)
	index := len(r.calls) - 1
	if index < len(r.errors) && r.errors[index] != nil {
		return "", r.errors[index]
	}
	if index < len(r.responses) {
		return r.responses[index], nil
	}
	return "answer", nil
}

func (r *recordingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type failAssistantOnce struct {
	ChatStore
	mu     sync.Mutex
	failed bool
}

func (s *failAssistantOnce) AppendMessage(
	ctx context.Context,
	chatID, role, content string,
	metadata map[string]any,
) (chats.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role == "assistant" && !s.failed {
		s.failed = true
		return chats.Message{}, errors.New("synthetic assistant persistence failure")
	}
	return s.ChatStore.AppendMessage(ctx, chatID, role, content, metadata)
}

func newServiceFixture(
	t *testing.T,
	applicationID string,
	runner Runner,
) (Service, *SQLStore, *chats.Store) {
	t.Helper()
	sessionStore, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessionStore.Close()) })
	chatStore, err := chats.NewStore(sessionStore.DB())
	require.NoError(t, err)
	turnStore, err := NewSQLiteStore(sessionStore.DB(), clock.Real())
	require.NoError(t, err)
	return Service{
		ApplicationID: applicationID,
		BindingDigest: "binding-v1",
		Bounds:        DefaultBounds(),
		Store:         turnStore,
		Chats:         chatStore,
		Graph: fixedGraph{snapshot: GraphSnapshot{
			Digest: "sha256:graph",
			JSON:   []byte(`{"schema":"kitsoki/graph-snapshot/v1","nodes":[]}`),
		}},
		Runner: runner,
	}, turnStore, chatStore
}

func TestServiceScopesReplaysAndEmitsPrivacySafeReceipt(t *testing.T) {
	runner := &recordingRunner{responses: []string{"first answer", "second answer", "other app"}}
	service, turnStore, chatStore := newServiceFixture(t, "caller-app", runner)
	ctx := context.Background()

	first, err := service.Ask(ctx, "customer-42", "secret question")
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Equal(t, "first answer", first.Answer)
	require.Equal(t, ReceiptSchema, first.Receipt.Schema)

	replay, err := service.Ask(ctx, "customer-42", "secret question")
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, first.Receipt, replay.Receipt)
	require.Equal(t, 1, runner.count())

	second, err := service.Ask(ctx, "customer-42", "follow up")
	require.NoError(t, err)
	require.Equal(t, first.TurnRef, second.Receipt.PredecessorRef)
	require.Equal(t, 2, runner.count())

	stored, err := turnStore.Get(ctx, first.TurnRef)
	require.NoError(t, err)
	receipt := string(stored.ReceiptJSON)
	for _, forbidden := range []string{
		"secret question", "first answer", "provider", "profile", "catalog_path", "http://", "/tmp/",
	} {
		require.NotContains(t, receipt, forbidden)
	}

	transcript, err := chatStore.Transcript(ctx, first.ConversationRef, 0)
	require.NoError(t, err)
	require.Len(t, transcript, 4)

	other := service
	other.ApplicationID = "other-app"
	otherResult, err := other.Ask(ctx, "customer-42", "secret question")
	require.NoError(t, err)
	require.NotEqual(t, first.ConversationRef, otherResult.ConversationRef)
	require.Equal(t, 3, runner.count())
}

func TestServiceRetriesInterruptedTurnWithoutDuplicatingQuestion(t *testing.T) {
	runner := &recordingRunner{
		responses: []string{"", "recovered"},
		errors:    []error{errors.New("cassette failure")},
	}
	service, turnStore, chatStore := newServiceFixture(t, "caller-app", runner)
	ctx := context.Background()

	_, err := service.Ask(ctx, "chat-a", "retry me")
	require.ErrorContains(t, err, "configured conversation provider")
	chat, _, err := chatStore.Resolve(
		ctx,
		"caller-app",
		RoomID,
		"application-conversation:"+digest("scope/v1", "caller-app", "binding-v1", "chat-a"),
		"ignored",
	)
	require.NoError(t, err)
	interrupted, err := turnStore.Latest(ctx, "caller-app", chat.ID)
	require.NoError(t, err)
	require.Equal(t, TurnInterrupted, interrupted.Status)

	_, err = service.Ask(ctx, "chat-a", "different")
	require.ErrorContains(t, err, "retry its original question")
	recovered, err := service.Ask(ctx, "chat-a", "retry me")
	require.NoError(t, err)
	require.Equal(t, "recovered", recovered.Answer)
	require.Equal(t, 2, runner.count())
	transcript, err := chatStore.Transcript(ctx, recovered.ConversationRef, 0)
	require.NoError(t, err)
	require.Len(t, transcript, 2)
	require.Equal(t, "user", transcript[0].Role)
	require.Equal(t, "assistant", transcript[1].Role)
}

func TestServiceCompletesPreparedAnswerAfterRestartWithoutRerun(t *testing.T) {
	runner := &recordingRunner{responses: []string{"prepared answer"}}
	service, turnStore, chatStore := newServiceFixture(t, "caller-app", runner)
	service.Chats = &failAssistantOnce{ChatStore: chatStore}
	ctx := context.Background()

	_, err := service.Ask(ctx, "chat-restart", "prepare")
	require.ErrorContains(t, err, "persist conversation response")
	chat, _, err := chatStore.Resolve(
		ctx,
		"caller-app",
		RoomID,
		"application-conversation:"+digest(
			"scope/v1", "caller-app", "binding-v1", "chat-restart",
		),
		"ignored",
	)
	require.NoError(t, err)
	prepared, err := turnStore.Latest(ctx, "caller-app", chat.ID)
	require.NoError(t, err)
	require.Equal(t, TurnAnswerReady, prepared.Status)
	require.Equal(t, "sha256:graph", prepared.GraphDigest)

	service.Chats = chatStore
	result, err := service.Ask(ctx, "chat-restart", "prepare")
	require.NoError(t, err)
	require.Equal(t, "prepared answer", result.Answer)
	require.Equal(t, 1, runner.count())
}

func TestServiceConcurrentSameQuestionRunsOnce(t *testing.T) {
	runner := &recordingRunner{responses: []string{"one answer"}}
	service, _, _ := newServiceFixture(t, "caller-app", runner)
	ctx := context.Background()
	const count = 8
	results := make(chan Result, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := service.Ask(ctx, "same-chat", "same question")
			results <- result
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		require.ErrorContains(t, err, "chat busy")
	}
	var ref string
	for result := range results {
		if result.Answer == "" {
			continue
		}
		require.Equal(t, "one answer", result.Answer)
		if ref == "" {
			ref = result.TurnRef
		}
		require.Equal(t, ref, result.TurnRef)
	}
	require.GreaterOrEqual(t, successes, 1)
	require.Equal(t, 1, runner.count())
}

func TestServiceRejectsNonOpaqueChatAndBounds(t *testing.T) {
	service, _, _ := newServiceFixture(t, "caller-app", &recordingRunner{})
	for _, chatID := range []string{" ../escape", "../escape", "https://authority", "has/slash"} {
		_, err := service.Ask(context.Background(), chatID, "question")
		require.ErrorContains(t, err, "opaque", chatID)
	}
	_, err := service.Ask(
		context.Background(),
		"chat",
		strings.Repeat("x", service.Bounds.MaxQuestionBytes+1),
	)
	require.ErrorContains(t, err, "exceeding")
}

func TestServiceRejectsNonCanonicalStoredReceipt(t *testing.T) {
	runner := &recordingRunner{responses: []string{"answer"}}
	service, turns, _ := newServiceFixture(t, "caller-app", runner)
	ctx := context.Background()
	result, err := service.Ask(ctx, "chat", "question")
	require.NoError(t, err)
	_, err = turns.db.ExecContext(
		ctx,
		`UPDATE application_conversation_turns
SET receipt_json = receipt_json || ' ' WHERE turn_ref = ?`,
		result.TurnRef,
	)
	require.NoError(t, err)
	_, err = service.Ask(ctx, "chat", "question")
	require.ErrorContains(t, err, "not canonical")
	require.Equal(t, 1, runner.count())
}
