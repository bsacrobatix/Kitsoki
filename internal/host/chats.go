// Package host — ChatStore interface and context helpers for persistent
// agent-room chats. Defined here (not in internal/chats) so the host package
// stays free of a chats import, avoiding import cycles in tests / mocks.
// *chats.Store does NOT satisfy this interface directly — use the adapter in
// internal/chathost to bridge between the two packages.
package host

import (
	"context"
	"errors"
	"time"
)

// ErrChatBusy is returned by ChatStore.WithLock when another process holds the
// per-chat lock. The concrete error from chats.Store is a *chatBusyError; the
// adapter in internal/chathost translates it to this sentinel so callers in
// host package only need to import host, not chats.
var ErrChatBusy = errors.New("chats: chat busy")

// chatBusyHostError wraps an underlying lock error while implementing
// errors.Is(target == ErrChatBusy) == true.
type chatBusyHostError struct{ cause error }

func (e *chatBusyHostError) Error() string   { return e.cause.Error() }
func (e *chatBusyHostError) Unwrap() error   { return e.cause }
func (e *chatBusyHostError) Is(t error) bool { return t == ErrChatBusy }

// NewChatBusyError wraps cause as a host.ErrChatBusy-compatible error.
// The adapter in internal/chathost calls this when it detects chats.ErrChatBusy.
func NewChatBusyError(cause error) error { return &chatBusyHostError{cause: cause} }

// ChatRecord mirrors chats.Chat at the host-package boundary.
// Same fields, no methods — conversion is the adapter's responsibility.
type ChatRecord struct {
	ID, AppID, Room, ScopeKey, Title, Status string
	ClaudeSessionID, ParentChatID, SessionID string
	CreatedAt, UpdatedAt, LastActiveAt       time.Time
}

// ChatMessage mirrors chats.Message at the host-package boundary.
type ChatMessage struct {
	ChatID    string
	Seq       int
	Role      string
	Content   string
	Metadata  map[string]any
	CreatedAt time.Time
}

// ChatStore is the subset of *chats.Store the host package needs.
// Defined here to avoid a host → chats import.
// Use internal/chathost.NewAdapter to wrap a *chats.Store.
type ChatStore interface {
	Get(ctx context.Context, chatID string) (*ChatRecord, error)
	// Resolve performs a transactional get-or-create on (app, room, scopeKey).
	// The bool reports whether the chat was newly created (true) or returned
	// from existing rows (false).
	Resolve(ctx context.Context, app, room, scopeKey, title string) (*ChatRecord, bool, error)
	Create(ctx context.Context, app, room, scopeKey, title string) (*ChatRecord, error)
	List(ctx context.Context, app, room, scopeKey string) ([]ChatRecord, error)
	Fork(ctx context.Context, parentID, newTitle string) (*ChatRecord, error)
	Archive(ctx context.Context, chatID string) error
	Rename(ctx context.Context, chatID, title string) error
	SetClaudeSessionID(ctx context.Context, chatID, claudeID string) error
	AppendMessage(ctx context.Context, chatID, role, content string, metadata map[string]any) (ChatMessage, error)
	Transcript(ctx context.Context, chatID string, sinceSeq int) ([]ChatMessage, error)
	LatestSeq(ctx context.Context, chatID string) (int, error)
	WithLock(ctx context.Context, chatID string, fn func(context.Context) error) error
}

// chatStoreKey is the unexported context key for the injected ChatStore.
type chatStoreKey struct{}

// WithChatStore injects a ChatStore into ctx so that chat-aware host handlers
// can access the store without importing the chats package.
// The orchestrator calls this before dispatching host effects when a chat store
// has been wired via orchestrator.WithChatStore.
func WithChatStore(ctx context.Context, cs ChatStore) context.Context {
	return context.WithValue(ctx, chatStoreKey{}, cs)
}

// ChatStoreFromContext retrieves the ChatStore from ctx, or nil if none was
// injected (e.g. the orchestrator was not configured with a chat store).
func ChatStoreFromContext(ctx context.Context) ChatStore {
	v, _ := ctx.Value(chatStoreKey{}).(ChatStore)
	return v
}
