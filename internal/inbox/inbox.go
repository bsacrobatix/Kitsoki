// Package inbox implements the notification inbox and teleport system (§4).
//
// The inbox provides:
//   - $inbox.unread world value for TUI badge rendering
//   - Teleport: rehydrates a target state and proposal from a notification
//   - InboxState type for the machine to render an inbox listing
//
// Teleport pushes the inbox predecessor onto the room history stack (§5.1),
// so pressing `back` from the teleport destination returns the user to wherever
// they were before visiting the inbox.
package inbox

import (
	"context"

	"hally/internal/app"
	"hally/internal/jobs"
	"hally/internal/world"
)

const (
	// WorldKey is the reserved world variable name for the inbox summary.
	WorldKey = "$inbox"
)

// InboxSummary is stored under $inbox in world state.
type InboxSummary struct {
	// Unread is the total count of unread notifications.
	Unread int `json:"unread"`
	// NeedsAttention is the count of action_required notifications.
	NeedsAttention int `json:"needs_attention"`
}

// ToMap converts the summary to a map for world-state storage.
func (s InboxSummary) ToMap() map[string]any {
	return map[string]any{
		"unread":          s.Unread,
		"needs_attention": s.NeedsAttention,
	}
}

// TeleportTarget describes where a teleport transition should land.
type TeleportTarget struct {
	// State is the destination state path.
	State app.StatePath
	// Slots are the slots to restore in the destination state.
	Slots map[string]any
	// ProposalID, if set, identifies the proposal to rehydrate as $proposal.
	ProposalID string
	// JobID, if set, identifies the job to rehydrate as $job.
	JobID string
}

// FromNotification extracts a TeleportTarget from a Notification.
func FromNotification(n jobs.Notification) TeleportTarget {
	return TeleportTarget{
		State:      app.StatePath(n.TeleportState),
		Slots:      n.TeleportSlots,
		ProposalID: n.TeleportProposalID,
		JobID:      n.TeleportJobID,
	}
}

// RefreshSummary queries the job store for unread counts and updates $inbox in world.
// Returns a new world with $inbox updated.
func RefreshSummary(ctx context.Context, js *jobs.JobStore, sessionID app.SessionID, w world.World) (world.World, error) {
	counts, err := js.UnreadCount(ctx, sessionID)
	if err != nil {
		return w, err
	}
	sum := InboxSummary{}
	for sev, cnt := range counts {
		sum.Unread += cnt
		if sev == jobs.SeverityActionRequired {
			sum.NeedsAttention += cnt
		}
	}
	return w.With(WorldKey, sum.ToMap()), nil
}

// PostJobNotification creates a notification for a completed job.
// Called by the scheduler after a job reaches a terminal state.
func PostJobNotification(ctx context.Context, js *jobs.JobStore, sessionID app.SessionID, j *jobs.Job, title, body string, sev jobs.NotificationSeverity) error {
	n := &jobs.Notification{
		SessionID:     sessionID,
		Severity:      sev,
		Title:         title,
		Body:          body,
		TeleportState: string(j.OriginState),
		TeleportJobID: j.ID,
		OriginKind:    "job",
		OriginRef:     "job:" + j.ID,
	}
	if j.OriginProposalID != "" {
		n.TeleportProposalID = j.OriginProposalID
	}
	return js.InsertNotification(ctx, n)
}
