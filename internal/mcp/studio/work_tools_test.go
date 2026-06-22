package studio_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	studio "kitsoki/internal/mcp/studio"
)

type studioFakeRunner struct {
	responses map[string]studioFakeResp
}

type studioFakeResp struct {
	stdout string
	stderr string
	code   int
	err    error
}

func (f *studioFakeRunner) run(ctx context.Context, dir, name string, args ...string) (string, string, int, error) {
	key := name + " " + strings.Join(args, " ")
	if r, ok := f.responses[key]; ok {
		return r.stdout, r.stderr, r.code, r.err
	}
	return "", "unexpected command: " + key, 1, nil
}

func TestStudioWorkAggregatesAsyncReacquisitionAcrossHandles(t *testing.T) {
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	cs := connectInProcess(ctx, t, srv)
	appPath := writeBackgroundJobStory(t)

	_, err := callTool(ctx, cs, "session.new", map[string]any{
		"story_path": cloakApp,
		"harness":    "replay",
		"cassette":   cloakCassette,
		"key":        "cloak",
		"trace":      t.TempDir() + "/cloak.jsonl",
	})
	require.NoError(t, err)

	res, err := callTool(ctx, cs, "session.new", map[string]any{
		"story_path": appPath,
		"harness":    "replay",
		"key":        "async-work",
		"trace":      t.TempDir() + "/async.jsonl",
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "session.new async: %s", contentText(res))

	res, err = callTool(ctx, cs, "session.submit", map[string]any{
		"handle": "async-work",
		"intent": "enter",
	})
	require.NoError(t, err)
	require.True(t, driveResult(t, res).OK)

	var work studio.WorkResult
	require.Eventually(t, func() bool {
		res, err := callTool(ctx, cs, "studio.work", nil)
		if err != nil || res.IsError {
			return false
		}
		if err := json.Unmarshal([]byte(contentText(res)), &work); err != nil {
			return false
		}
		return work.Summary.NotificationsUnread == 2 && len(work.Items) == 2
	}, 3*time.Second, 25*time.Millisecond)

	assert.True(t, work.OK)
	assert.Equal(t, 2, work.Summary.Sessions)
	assert.Equal(t, 1, work.Summary.JobsTerminal)
	assert.Equal(t, 2, work.Summary.NotificationsUnread)
	assert.Equal(t, 2, work.Summary.NeedsAttention)
	require.Len(t, work.Sessions, 2)
	sessionsByHandle := map[string]studio.WorkSessionSummary{}
	for _, session := range work.Sessions {
		sessionsByHandle[session.Handle] = session
	}
	require.Contains(t, sessionsByHandle, "cloak")
	require.Contains(t, sessionsByHandle, "async-work")
	assert.Equal(t, 1, sessionsByHandle["async-work"].Async.JobsTerminal)

	require.NotEmpty(t, work.Items)
	top := work.Items[0]
	assert.Equal(t, "notification", top.Kind)
	assert.Equal(t, "async-work", top.Handle)
	assert.Equal(t, jobs.SeveritySuccess, top.Severity)
	assert.Equal(t, "session.teleport", top.Reacquire.Tool)
	assert.Equal(t, "async-work", top.Reacquire.Args["handle"])
	assert.Equal(t, top.NotificationID, top.Reacquire.Args["notification_id"])

	res, err = callTool(ctx, cs, "session.teleport", map[string]any{
		"handle":          top.Handle,
		"notification_id": top.NotificationID,
	})
	require.NoError(t, err)
	require.True(t, driveResult(t, res).OK)

	res, err = callTool(ctx, cs, "studio.work", nil)
	require.NoError(t, err)
	require.False(t, res.IsError, "studio.work after teleport: %s", contentText(res))
	var after studio.WorkResult
	require.NoError(t, json.Unmarshal([]byte(contentText(res)), &after))
	assert.Equal(t, 1, after.Summary.NotificationsUnread)
	assert.Len(t, after.Items, 1, "read notification drops out of the active queue")
}

func TestGitHubInboxSyncFeedsStudioWork(t *testing.T) {
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	cs := connectInProcess(ctx, t, srv)

	res, err := callTool(ctx, cs, "session.new", map[string]any{
		"story_path": cloakApp,
		"harness":    "replay",
		"cassette":   cloakCassette,
		"key":        "github-sync",
		"trace":      t.TempDir() + "/github-sync.jsonl",
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "session.new: %s", contentText(res))

	fr := &studioFakeRunner{responses: map[string]studioFakeResp{
		"gh --version": {stdout: "gh version 2.x\n"},
		"gh issue list --repo acme/repo --state open --assignee @me --limit 10 --json number,title,assignees,url": {
			stdout: `[{"number":7,"title":"Assigned issue","url":"https://github.com/acme/repo/issues/7","assignees":[{"login":"brad"}]}]`,
		},
		"gh pr list --repo acme/repo --state open --review-requested @me --limit 10 --json number,title,author,url": {
			stdout: `[{"number":42,"title":"Review this","url":"https://github.com/acme/repo/pull/42","author":{"login":"alice"}}]`,
		},
	}}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err = callTool(ctx, cs, "inbox.sync_github", map[string]any{
		"handle": "github-sync",
		"repo":   "acme/repo",
		"limit":  10,
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "inbox.sync_github: %s", contentText(res))
	var synced studio.GitHubInboxSyncResult
	require.NoError(t, json.Unmarshal([]byte(contentText(res)), &synced))
	assert.Equal(t, 2, synced.Fetched)
	assert.Equal(t, 2, synced.Inserted)
	assert.Equal(t, 0, synced.Skipped)

	res, err = callTool(ctx, cs, "studio.work", nil)
	require.NoError(t, err)
	require.False(t, res.IsError, "studio.work: %s", contentText(res))
	var work studio.WorkResult
	require.NoError(t, json.Unmarshal([]byte(contentText(res)), &work))
	assert.Equal(t, 2, work.Summary.NotificationsUnread)
	assert.Equal(t, 2, work.Summary.NotificationsActionRequired)
	require.Len(t, work.Items, 2)
	var prItem *studio.WorkItem
	for i := range work.Items {
		if work.Items[i].OriginRef == "github:acme/repo/pr/42" {
			prItem = &work.Items[i]
			break
		}
	}
	require.NotNil(t, prItem, "studio.work should include the review-requested PR")
	assert.Equal(t, "https://github.com/acme/repo/pull/42", prItem.OriginURL)
	assert.Equal(t, map[string]any{"pr_author": "alice", "pr_id": "42", "pr_title": "Review this"}, prItem.TeleportSlots)

	res, err = callTool(ctx, cs, "inbox.sync_github", map[string]any{
		"handle": "github-sync",
		"repo":   "acme/repo",
		"limit":  10,
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "second inbox.sync_github: %s", contentText(res))
	var second studio.GitHubInboxSyncResult
	require.NoError(t, json.Unmarshal([]byte(contentText(res)), &second))
	assert.Equal(t, 2, second.Fetched)
	assert.Equal(t, 0, second.Inserted)
	assert.Equal(t, 2, second.Skipped)
}

func TestStudioWorkShowsRunningJobs(t *testing.T) {
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	cs := connectInProcess(ctx, t, srv)
	appPath := writeSlowBackgroundJobStory(t)

	res, err := callTool(ctx, cs, "session.new", map[string]any{
		"story_path": appPath,
		"harness":    "replay",
		"key":        "slow-work",
		"trace":      t.TempDir() + "/trace.jsonl",
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "session.new: %s", contentText(res))

	res, err = callTool(ctx, cs, "session.submit", map[string]any{
		"handle": "slow-work",
		"intent": "enter",
	})
	require.NoError(t, err)
	require.True(t, driveResult(t, res).OK)

	var work studio.WorkResult
	require.Eventually(t, func() bool {
		res, err := callTool(ctx, cs, "studio.work", nil)
		if err != nil || res.IsError {
			return false
		}
		if err := json.Unmarshal([]byte(contentText(res)), &work); err != nil {
			return false
		}
		if work.Summary.JobsRunning != 1 {
			return false
		}
		for _, item := range work.Items {
			if item.Kind == "job" && item.Status == "running" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)

	assert.Equal(t, 1, work.Summary.Sessions)
	assert.Equal(t, 1, work.Summary.JobsRunning)
	var runningJob studio.WorkItem
	for _, item := range work.Items {
		if item.Kind == "job" && item.Status == "running" {
			runningJob = item
			break
		}
	}
	require.NotEmpty(t, runningJob.JobID)
	assert.Equal(t, "slow-work", runningJob.Handle)
	assert.Equal(t, "session.inspect", runningJob.Reacquire.Tool)
	assert.Equal(t, "slow-work", runningJob.Reacquire.Args["handle"])
}
