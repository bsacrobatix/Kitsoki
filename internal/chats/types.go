// Package chats — SQLite-backed persistence for persistent agent-room chats.
package chats

import "time"

// ChatStatus is the lifecycle status of a chat.
type ChatStatus string

const (
	ChatActive    ChatStatus = "active"
	ChatPaused    ChatStatus = "paused"
	ChatCompleted ChatStatus = "completed"
	ChatArchived  ChatStatus = "archived"
)

// Chat is a persistent conversation thread within a room.
type Chat struct {
	ID              string
	AppID           string
	Room            string
	ScopeKey        string
	Title           string
	Status          string
	ClaudeSessionID string
	ParentChatID    string
	SessionID       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastActiveAt    time.Time
}

// Message is one turn in a chat transcript.
type Message struct {
	ChatID    string
	Seq       int
	Role      string // user|assistant|system|tool
	Content   string
	Metadata  map[string]any
	CreatedAt time.Time
}
