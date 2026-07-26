// Package applicationconversation implements daemon-owned, application-scoped
// conversations over fixed graph and agent bindings.
package applicationconversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"kitsoki/internal/chats"
)

const (
	ReceiptSchema = "kitsoki/application-conversation-receipt/v1"
	RoomID        = "application-conversation"

	DefaultMaxQuestionBytes  = 8 * 1024
	DefaultMaxAnswerBytes    = 32 * 1024
	DefaultMaxHistoryBytes   = 128 * 1024
	DefaultMaxHistoryEntries = 40
	DefaultMaxGraphBytes     = 256 * 1024
)

var ErrTurnNotFound = errors.New("application conversation: turn not found")

type Bounds struct {
	MaxQuestionBytes  int `json:"max_question_bytes" yaml:"max_question_bytes"`
	MaxAnswerBytes    int `json:"max_answer_bytes" yaml:"max_answer_bytes"`
	MaxHistoryBytes   int `json:"max_history_bytes" yaml:"max_history_bytes"`
	MaxHistoryEntries int `json:"max_history_entries" yaml:"max_history_entries"`
	MaxGraphBytes     int `json:"max_graph_bytes" yaml:"max_graph_bytes"`
}

func DefaultBounds() Bounds {
	return Bounds{
		MaxQuestionBytes:  DefaultMaxQuestionBytes,
		MaxAnswerBytes:    DefaultMaxAnswerBytes,
		MaxHistoryBytes:   DefaultMaxHistoryBytes,
		MaxHistoryEntries: DefaultMaxHistoryEntries,
		MaxGraphBytes:     DefaultMaxGraphBytes,
	}
}

func (b Bounds) Validate() error {
	switch {
	case b.MaxQuestionBytes < 1 || b.MaxQuestionBytes > DefaultMaxQuestionBytes:
		return fmt.Errorf("max_question_bytes must be within 1..%d", DefaultMaxQuestionBytes)
	case b.MaxAnswerBytes < 1 || b.MaxAnswerBytes > DefaultMaxAnswerBytes:
		return fmt.Errorf("max_answer_bytes must be within 1..%d", DefaultMaxAnswerBytes)
	case b.MaxHistoryBytes < b.MaxQuestionBytes || b.MaxHistoryBytes > DefaultMaxHistoryBytes:
		return fmt.Errorf(
			"max_history_bytes must be within max_question_bytes..%d",
			DefaultMaxHistoryBytes,
		)
	case b.MaxHistoryEntries < 1 || b.MaxHistoryEntries > DefaultMaxHistoryEntries:
		return fmt.Errorf(
			"max_history_entries must be within 1..%d",
			DefaultMaxHistoryEntries,
		)
	case b.MaxGraphBytes < 1 || b.MaxGraphBytes > DefaultMaxGraphBytes:
		return fmt.Errorf("max_graph_bytes must be within 1..%d", DefaultMaxGraphBytes)
	default:
		return nil
	}
}

type GraphSnapshot struct {
	Digest string
	JSON   json.RawMessage
}

type GraphSource interface {
	Snapshot(context.Context) (GraphSnapshot, error)
}

type Message struct {
	Role    string
	Content string
}

type RunRequest struct {
	Graph            GraphSnapshot
	Messages         []Message
	HistoryTruncated bool
}

type Runner interface {
	Run(context.Context, RunRequest) (string, error)
}

type ChatStore interface {
	Resolve(context.Context, string, string, string, string) (*chats.Chat, bool, error)
	AppendMessage(context.Context, string, string, string, map[string]any) (chats.Message, error)
	Transcript(context.Context, string, int) ([]chats.Message, error)
	WithLock(context.Context, string, func(context.Context) error) error
}

type TurnStatus string

const (
	TurnPending     TurnStatus = "pending"
	TurnAnswerReady TurnStatus = "answer_ready"
	TurnCompleted   TurnStatus = "completed"
	TurnInterrupted TurnStatus = "interrupted"
)

type Turn struct {
	Ref                  string
	ApplicationID        string
	ChatRef              string
	BindingDigest        string
	PredecessorRef       string
	QuestionDigest       string
	GraphDigest          string
	Status               TurnStatus
	UserSeq              *int
	AssistantSeq         *int
	Answer               string
	ReceiptJSON          []byte
	InterruptedReason    string
	CreatedAt, UpdatedAt time.Time
}

type TurnStore interface {
	Latest(context.Context, string, string) (Turn, error)
	Get(context.Context, string) (Turn, error)
	Begin(context.Context, Turn) (Turn, error)
	SetUserSeq(context.Context, string, int) error
	StoreAnswer(context.Context, string, string, string) error
	SetAssistantSeq(context.Context, string, int) error
	Complete(context.Context, string, []byte) error
	Interrupt(context.Context, string, string) error
	InterruptPending(context.Context, string) (int64, error)
}

type Receipt struct {
	Schema          string `json:"schema"`
	ApplicationID   string `json:"application_id"`
	ConversationRef string `json:"conversation_ref"`
	TurnRef         string `json:"turn_ref"`
	PredecessorRef  string `json:"predecessor_ref,omitempty"`
	BindingDigest   string `json:"binding_digest"`
	GraphDigest     string `json:"graph_digest"`
	QuestionDigest  string `json:"question_digest"`
	AnswerDigest    string `json:"answer_digest"`
	UserSeq         int    `json:"user_seq"`
	AssistantSeq    int    `json:"assistant_seq"`
	Status          string `json:"status"`
}

type Result struct {
	Answer          string
	ConversationRef string
	TurnRef         string
	Receipt         Receipt
	Replayed        bool
}

func validOpaqueID(value string, maxBytes int) bool {
	trimmed := strings.TrimSpace(value)
	if value != trimmed || value == "" || len(value) > maxBytes || strings.Contains(value, "..") ||
		strings.ContainsAny(value, `/\`) || strings.Contains(value, "://") {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}
