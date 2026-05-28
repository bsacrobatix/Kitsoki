package journal

import "strings"

// CheckpointContext carries the information a Policy uses to decide whether
// to emit a checkpoint for a given document.
type CheckpointContext struct {
	// Turn is the current session turn number.
	Turn int64

	// ChatMessageCount is the number of messages appended to this chat since
	// the last checkpoint. Only relevant for "chats/<id>" documents.
	ChatMessageCount int

	// JobStatusChanged reports whether the job's status changed this turn.
	// Only relevant for "jobs/<id>" documents.
	JobStatusChanged bool
}

// Policy decides when to emit a checkpoint for a document.
type Policy interface {
	// ShouldCheckpoint returns true if a checkpoint should be emitted for doc
	// given ctx.
	ShouldCheckpoint(doc DocID, ctx CheckpointContext) bool
}

// defaultPolicy implements Policy with the cadences described in §4.4.
type defaultPolicy struct{}

// DefaultPolicy returns the standard checkpoint policy (§4.4):
//
//   - world / state: every 20 turns.
//   - chats/<id>: every 10 appended messages.
//   - jobs/<id>: on every status transition.
func DefaultPolicy() Policy {
	return defaultPolicy{}
}

func (defaultPolicy) ShouldCheckpoint(doc DocID, ctx CheckpointContext) bool {
	switch {
	case doc == "world" || doc == "state":
		return ctx.Turn > 0 && ctx.Turn%20 == 0

	case strings.HasPrefix(string(doc), "chats/"):
		return ctx.ChatMessageCount > 0 && ctx.ChatMessageCount%10 == 0

	case strings.HasPrefix(string(doc), "jobs/"):
		return ctx.JobStatusChanged

	default:
		// Unknown doc kind falls back to the 20-turn global cadence.
		return ctx.Turn > 0 && ctx.Turn%20 == 0
	}
}
