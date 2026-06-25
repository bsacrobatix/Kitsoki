package ghagent

import (
	"context"
	"fmt"
	"strings"

	"kitsoki/internal/host"
)

// CommentStore posts and edits the rolling-status/ack comment on a GitHub thread
// via host.gh.ticket. Exec is the host.Handler bound to host.gh.ticket (the DI
// seam — a cassette dispatcher in tests, host.GitHubTicketHandler in prod).
type CommentStore struct {
	Exec host.Handler
	Repo string
}

// Meta is the fenced ```kitsoki block payload echoed in every status comment.
// Its field tags mirror the key names ghParseMetadata recovers.
type Meta struct {
	JobID     string `json:"job_id"`
	OriginRef string `json:"origin_ref"`
	Story     string `json:"story"`
	State     string `json:"state"`
	RunURL    string `json:"run_url,omitempty"`
}

// RenderMeta renders the fenced ```kitsoki metadata block as `key: value` lines,
// matching host.ghAppendMetadata's convention so host.ghParseMetadata
// round-trips it.
func RenderMeta(m Meta) string {
	type kv struct{ k, v string }
	fields := []kv{
		{"job_id", m.JobID},
		{"origin_ref", m.OriginRef},
		{"story", m.Story},
		{"state", m.State},
		{"run_url", m.RunURL},
	}
	var lines []string
	for _, f := range fields {
		if strings.TrimSpace(f.v) != "" {
			lines = append(lines, f.k+": "+f.v)
		}
	}
	return "```kitsoki\n" + strings.Join(lines, "\n") + "\n```"
}

// renderBody composes the prose plus the fenced metadata block.
func renderBody(prose string, meta Meta) string {
	prose = strings.TrimRight(prose, "\n")
	if prose == "" {
		return RenderMeta(meta)
	}
	return prose + "\n\n" + RenderMeta(meta) + "\n"
}

// Post creates the FIRST status comment (op=comment) and returns the comment id
// captured from the host.gh.ticket result's data.comment_id. body carries the
// prose; the fenced metadata block is appended automatically.
func (c *CommentStore) Post(ctx context.Context, issueID, body string, meta Meta) (string, error) {
	res, err := c.Exec(ctx, map[string]any{
		"op":   "comment",
		"id":   issueID,
		"repo": c.Repo,
		"body": renderBody(body, meta),
	})
	if err != nil {
		return "", fmt.Errorf("ghagent: post comment: %w", err)
	}
	if res.Error != "" {
		return "", fmt.Errorf("ghagent: post comment: %s", res.Error)
	}
	commentID, _ := res.Data["comment_id"].(string)
	return commentID, nil
}

// Update edits the existing status comment in place. If commentID is empty, or
// the edit op fails, it falls back to posting a new comment so the run still
// reports progress in the GitHub thread.
func (c *CommentStore) Update(ctx context.Context, issueID, commentID, body string, meta Meta) (string, error) {
	rendered := renderBody(body, meta)
	if strings.TrimSpace(commentID) != "" {
		res, err := c.Exec(ctx, map[string]any{
			"op":         "comment_edit",
			"comment_id": commentID,
			"repo":       c.Repo,
			"body":       rendered,
		})
		if err == nil && res.Error == "" {
			if nextID, _ := res.Data["comment_id"].(string); strings.TrimSpace(nextID) != "" {
				return nextID, nil
			}
			return commentID, nil
		}
	}
	res, err := c.Exec(ctx, map[string]any{
		"op":   "comment",
		"id":   issueID,
		"repo": c.Repo,
		"body": rendered,
	})
	if err != nil {
		return commentID, fmt.Errorf("ghagent: update comment: %w", err)
	}
	if res.Error != "" {
		return commentID, fmt.Errorf("ghagent: update comment: %s", res.Error)
	}
	if nextID, _ := res.Data["comment_id"].(string); strings.TrimSpace(nextID) != "" {
		return nextID, nil
	}
	return commentID, nil
}
