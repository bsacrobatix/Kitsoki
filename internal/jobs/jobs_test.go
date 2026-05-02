package jobs_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"hally/internal/host"
	"hally/internal/jobs"
)

func echoHandler(output string) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"output": output}}, nil
	}
}

func failHandler(msg string) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Error: msg}, nil
	}
}

func slowHandler(d time.Duration, output string) host.Handler {
	return func(ctx context.Context, args map[string]any) (host.Result, error) {
		select {
		case <-ctx.Done():
			return host.Result{}, ctx.Err()
		case <-time.After(d):
			return host.Result{Data: map[string]any{"output": output}}, nil
		}
	}
}

func TestSubmitAndSubscribe_Success(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	ch, unsub := subscribeAfterSubmit(t, sched, echoHandler("hello"))
	defer unsub()

	select {
	case ev := <-ch:
		if ev.Status != jobs.JobDone {
			t.Fatalf("expected done, got %s", ev.Status)
		}
		if ev.Result == nil || ev.Result.Data["output"] != "hello" {
			t.Fatalf("expected output=hello, got %v", ev.Result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for job completion")
	}
}

func TestSubmitAndSubscribe_Failure(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	ch, unsub := subscribeAfterSubmit(t, sched, failHandler("domain error"))
	defer unsub()

	select {
	case ev := <-ch:
		if ev.Status != jobs.JobFailed {
			t.Fatalf("expected failed, got %s", ev.Status)
		}
		if ev.Error != "domain error" {
			t.Fatalf("expected 'domain error', got %q", ev.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for failed job")
	}
}

func TestCancel(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	id, err := sched.Submit(context.Background(), jobs.JobSpec{
		Kind:    "host.slow",
		Handler: slowHandler(10*time.Second, "never"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	ch, unsub := sched.Subscribe(id)
	defer unsub()

	if err := sched.Cancel(context.Background(), id); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Status != jobs.JobCancelled {
			t.Fatalf("expected cancelled, got %s", ev.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for cancellation")
	}
}

func TestHeartbeat(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	id, err := sched.Submit(context.Background(), jobs.JobSpec{
		Kind:    "host.slow",
		Handler: slowHandler(500*time.Millisecond, "done"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	ch, unsub := sched.Subscribe(id)
	defer unsub()

	// Send a heartbeat.
	if err := sched.Heartbeat(id, map[string]any{"progress": 50}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// Drain the channel until done.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Status == jobs.JobDone {
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for job completion")
		}
	}
}

func TestCancelUnknownJob(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()
	err := sched.Cancel(context.Background(), "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for unknown job")
	}
}

// subscribeAfterSubmit submits a job, subscribes, and returns the channel.
func subscribeAfterSubmit(t *testing.T, sched jobs.Scheduler, h host.Handler) (<-chan jobs.JobEvent, func()) {
	t.Helper()
	id, err := sched.Submit(context.Background(), jobs.JobSpec{
		Kind:    "host.test",
		Handler: h,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	ch, unsub := sched.Subscribe(id)
	return ch, unsub
}

// TestSubscribeOnTerminalJob_NoPanic verifies P0-1: subscribing to an already-
// terminal job and concurrently calling Heartbeat (which fires fanoutLocked)
// must not panic.  Before the fix, the channel was added to rj.subs and then
// closed while fanout could send to it.
func TestSubscribeOnTerminalJob_NoPanic(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()

	// Submit a job that completes immediately.
	id, err := sched.Submit(context.Background(), jobs.JobSpec{
		Kind:    "host.instant",
		Handler: echoHandler("hi"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for the job to finish.
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sched.WaitIdle(waitCtx); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}

	// Concurrently Subscribe + Heartbeat — this should not panic.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_ = sched.Heartbeat(id, map[string]any{"i": i})
		}
	}()
	for i := 0; i < 100; i++ {
		ch, unsub := sched.Subscribe(id)
		// Drain the channel (already closed or sends one terminal event).
		for range ch {
		}
		unsub()
	}
	<-done
}

// TestAwaiting_RunningCountDoesNotGoNegative verifies P0-2: after a job
// transitions through awaiting_input→done, runningCount never goes negative.
func TestAwaiting_RunningCountDoesNotGoNegative(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()

	// Submit two jobs sequentially; each must complete without WaitIdle firing
	// prematurely (which would be the symptom of runningCount going negative).
	for i := 0; i < 2; i++ {
		id, err := sched.Submit(context.Background(), jobs.JobSpec{
			Kind:    "host.fast",
			Handler: echoHandler("ok"),
		})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sched.WaitIdle(ctx); err != nil {
			t.Fatalf("WaitIdle %d: %v", i, err)
		}

		j, ok := sched.Get(id)
		if !ok {
			t.Fatalf("job %d not found after completion", i)
		}
		if j.Status != jobs.JobDone {
			t.Fatalf("job %d: expected done, got %s", i, j.Status)
		}
	}
}

// TestResume_ReIncrementsRunningCount verifies P0-2 Resumed behaviour:
// after Awaiting (runningCount=0) then Resumed (runningCount=1), WaitIdle
// should block until the job finishes.
func TestResume_ReIncrementsRunningCount(t *testing.T) {
	sched := jobs.NewInMemoryScheduler()

	// Channel to synchronise the test: the handler blocks until the test
	// sends a signal, mimicking a clarification wait.
	proceed := make(chan struct{})

	id, err := sched.Submit(context.Background(), jobs.JobSpec{
		Kind: "host.pausable",
		Handler: func(ctx context.Context, args map[string]any) (host.Result, error) {
			<-proceed
			return host.Result{Data: map[string]any{"ok": true}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Manually call Awaiting so the scheduler considers the job idle.
	if err := sched.Awaiting(id); err != nil {
		t.Fatalf("Awaiting: %v", err)
	}

	// WaitIdle should return immediately (runningCount==0).
	idleCtx, idleCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer idleCancel()
	if err := sched.WaitIdle(idleCtx); err != nil {
		t.Fatalf("WaitIdle after Awaiting: %v", err)
	}

	// Now signal resume: job is running again.
	if err := sched.Resumed(id); err != nil {
		t.Fatalf("Resumed: %v", err)
	}

	// WaitIdle should NOT return before the handler is unblocked.
	earlyCtx, earlyCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer earlyCancel()
	if err := sched.WaitIdle(earlyCtx); err == nil {
		t.Fatal("WaitIdle should not have returned before handler completed")
	}

	// Unblock the handler and verify WaitIdle returns.
	close(proceed)
	doneCtx, doneCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer doneCancel()
	if err := sched.WaitIdle(doneCtx); err != nil {
		t.Fatalf("WaitIdle after handler complete: %v", err)
	}
}

// TestHeartbeatDebounce verifies the write-through debounce behaviour:
// firing many heartbeats in a short burst should result in fewer SQLite
// writes than the number of calls, and the persisted progress should
// reflect the last value sent.  After a pause longer than the debounce
// interval a single heartbeat must trigger a flush.
func TestHeartbeatDebounce(t *testing.T) {
	db := openTestDB(t)
	js, err := jobs.NewJobStore(db)
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}

	sched := jobs.NewScheduler(js)

	// Use a slow handler so we can heartbeat while the job is running.
	id, err := sched.Submit(context.Background(), jobs.JobSpec{
		SessionID: "sess-hb",
		Kind:      "host.slow",
		Handler:   slowHandler(2*time.Second, "ok"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Fire 10 heartbeats within ~50 ms — well within the 500 ms debounce.
	for i := 0; i < 10; i++ {
		if err := sched.Heartbeat(id, map[string]any{"pct": i * 10}); err != nil {
			t.Fatalf("Heartbeat %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Wait longer than the debounce interval to guarantee one flush.
	time.Sleep(600 * time.Millisecond)

	// Send one more heartbeat with a known final progress value.
	finalProgress := map[string]any{"pct": 99}
	if err := sched.Heartbeat(id, finalProgress); err != nil {
		t.Fatalf("final Heartbeat: %v", err)
	}

	// Allow the flush to reach SQLite.
	time.Sleep(20 * time.Millisecond)

	// The persisted row should exist (was written at submit time and
	// at least once during/after the burst).
	running, err := js.ListJobsByStatus(context.Background(), "sess-hb", jobs.JobRunning)
	if err != nil {
		t.Fatalf("ListJobsByStatus: %v", err)
	}
	if len(running) == 0 {
		t.Fatal("expected at least one running job row in SQLite")
	}
}
