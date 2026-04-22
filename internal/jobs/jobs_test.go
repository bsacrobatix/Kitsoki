package jobs_test

import (
	"context"
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
