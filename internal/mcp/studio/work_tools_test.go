package studio_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/jobs"
	studio "kitsoki/internal/mcp/studio"
)

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
