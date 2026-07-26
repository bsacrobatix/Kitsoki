package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/jobs"
	"kitsoki/internal/store"
)

type applicationSessionManagerFunc func(context.Context, appplatform.HandlerDefinition, appplatform.Invocation) (string, error)

func (f applicationSessionManagerFunc) CreateSession(ctx context.Context, def appplatform.HandlerDefinition, invocation appplatform.Invocation) (string, error) {
	return f(ctx, def, invocation)
}

func TestApplicationJournalRestoresReplayAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.jsonl")
	firstJournal, err := openApplicationJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	def := appplatform.HandlerDefinition{
		ID: "save", Name: "Save", Description: "Save once.", SemanticRef: "test.handler.save",
		Session: appplatform.SessionRequired, Effect: appplatform.EffectWrite,
		RoutingMode: appplatform.RoutingExact, Outcomes: []string{"ok"},
		Expose:      []appplatform.Transport{appplatform.TransportCLI, appplatform.TransportMCP},
		Idempotency: appplatform.IdempotencyRequired, IdempotencyScope: "application",
	}
	calls := 0
	newRegistry := func(journal *applicationJournal) *appplatform.Registry {
		registry := appplatform.NewRegistry(appplatform.Dependencies{Receipts: journal, Replay: journal})
		if err := registry.RegisterHandler(def, appplatform.HandlerFunc(func(context.Context, appplatform.Invocation) (appplatform.HandlerResult, error) {
			calls++
			return appplatform.HandlerResult{Outcome: "ok", Output: json.RawMessage(`{"saved":true}`)}, nil
		})); err != nil {
			t.Fatal(err)
		}
		return registry
	}
	first, err := newRegistry(firstJournal).Invoke(context.Background(), appplatform.Invocation{
		HandlerID: "save", Input: json.RawMessage(`{"id":1}`), SessionID: "one",
		Transport: appplatform.TransportCLI, IdempotencyKey: "save-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := openApplicationJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := newRegistry(reopened).Invoke(context.Background(), appplatform.Invocation{
		HandlerID: "save", Input: json.RawMessage(`{"id":1}`), SessionID: "two",
		Transport: appplatform.TransportMCP, IdempotencyKey: "save-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !replay.Receipt.Replayed || replay.Receipt.ReplayOf != first.Receipt.ID ||
		replay.Receipt.Transport != appplatform.TransportMCP {
		t.Fatalf("calls=%d first=%#v replay=%#v", calls, first.Receipt, replay.Receipt)
	}
}

func TestApplicationEventRuntimeBackgroundIsNonBlockingAndDurable(t *testing.T) {
	journal := &applicationJournal{
		path:   filepath.Join(t.TempDir(), "application.jsonl"),
		replay: appplatform.NewMemoryReplayStore(),
	}
	scheduler := jobs.NewInMemoryScheduler()
	runtime := applicationEventRuntime{scheduler: scheduler, journal: journal}
	release := make(chan struct{})
	started := make(chan struct{})
	outcome, err := runtime.Enqueue(
		context.Background(),
		appplatform.EventDefinition{ID: "changed", Source: "test.changed", Mode: appplatform.EventBackground},
		appplatform.HandlerDefinition{
			ID: "refresh", SemanticRef: "test.handler.refresh", Effect: appplatform.EffectRead,
		},
		appplatform.Invocation{
			HandlerID: "refresh", SessionID: "session-1", Actor: "system",
			Transport: appplatform.TransportEvent, RoutingMode: appplatform.RoutingExact,
			EventID: "changed", EventMode: appplatform.EventBackground,
			Input: json.RawMessage(`{"id":1}`),
		},
		func(context.Context) (appplatform.OutcomeEnvelope, error) {
			close(started)
			<-release
			return appplatform.OutcomeEnvelope{Outcome: "ok"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scheduled handler did not start")
	}
	if outcome.Outcome != "accepted" || len(outcome.Children) != 1 ||
		outcome.Children[0].Status != "running" || outcome.Join == nil ||
		outcome.Join.Status != "pending" || scheduler.RunningCount() != 1 {
		t.Fatalf("background acknowledgement = %#v", outcome)
	}
	close(release)
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(journal.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"status":"running"`) ||
		!strings.Contains(string(raw), `"status":"done"`) {
		t.Fatalf("durable event states:\n%s", raw)
	}
}

func TestApplicationEventSchedulerStateReacquiresAfterStoreReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	firstStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	jobStore, err := jobs.NewJobStore(firstStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	scheduler := jobs.NewScheduler(jobStore)
	runtime := applicationEventRuntime{scheduler: scheduler}
	outcome, err := runtime.Enqueue(
		context.Background(),
		appplatform.EventDefinition{ID: "durable", Source: "test.durable", Mode: appplatform.EventBackground},
		appplatform.HandlerDefinition{ID: "refresh", SemanticRef: "test.refresh", Effect: appplatform.EffectRead},
		appplatform.Invocation{
			HandlerID: "refresh", SessionID: "session-1", Actor: "system",
			Transport: appplatform.TransportEvent, RoutingMode: appplatform.RoutingExact,
			EventID: "durable", EventMode: appplatform.EventBackground, Input: json.RawMessage(`{}`),
		},
		func(context.Context) (appplatform.OutcomeEnvelope, error) {
			return appplatform.OutcomeEnvelope{Outcome: "ok"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	jobID := outcome.Children[0].ID
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedJobs, err := jobs.NewJobStore(reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	reacquired, err := reopenedJobs.ListByStatus(context.Background(), []jobs.JobStatus{jobs.JobDone})
	if err != nil {
		t.Fatal(err)
	}
	var matched *jobs.Job
	for i := range reacquired {
		if reacquired[i].ID == jobID {
			matched = &reacquired[i]
			break
		}
	}
	if matched == nil || matched.Status != jobs.JobDone || matched.SessionID != "session-1" {
		t.Fatalf("reacquired jobs = %#v", reacquired)
	}
}

func TestApplicationCreateSessionEventsAreDurableWithoutDoubleCreation(t *testing.T) {
	for _, mode := range []appplatform.EventMode{appplatform.EventBackground, appplatform.EventInterrupt} {
		t.Run(string(mode), func(t *testing.T) {
			journal := &applicationJournal{
				path:   filepath.Join(t.TempDir(), "application.jsonl"),
				replay: appplatform.NewMemoryReplayStore(),
			}
			scheduler := jobs.NewInMemoryScheduler()
			interrupted := false
			eventRuntime := applicationEventRuntime{
				scheduler: scheduler, journal: journal,
				interrupt: func(string) { interrupted = true },
			}
			creates := 0
			registry := appplatform.NewRegistry(appplatform.Dependencies{
				Events: eventRuntime, Receipts: journal,
				Sessions: applicationSessionManagerFunc(func(context.Context, appplatform.HandlerDefinition, appplatform.Invocation) (string, error) {
					creates++
					return "created-session", nil
				}),
			})
			handler := appplatform.HandlerDefinition{
				ID: "target", Name: "Target", Description: "Target event.", SemanticRef: "test.target",
				Session: appplatform.SessionCreate, Effect: appplatform.EffectRead,
				RoutingMode: appplatform.RoutingExact, Outcomes: []string{"ok"},
			}
			if err := registry.RegisterHandler(handler, appplatform.HandlerFunc(func(_ context.Context, invocation appplatform.Invocation) (appplatform.HandlerResult, error) {
				if invocation.SessionID != "created-session" {
					t.Fatalf("handler session = %q", invocation.SessionID)
				}
				return appplatform.HandlerResult{Outcome: "ok", Output: json.RawMessage(`{}`)}, nil
			})); err != nil {
				t.Fatal(err)
			}
			if err := registry.RegisterEvent(appplatform.EventDefinition{
				ID: "changed", Source: "test.changed", Session: appplatform.SessionCreate,
				Mode: mode, Handler: "target",
			}); err != nil {
				t.Fatal(err)
			}
			outcome, err := registry.DispatchEvent(context.Background(), "changed", nil, "definition-context", "system")
			if err != nil {
				t.Fatal(err)
			}
			if mode == appplatform.EventBackground {
				waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := scheduler.WaitIdle(waitCtx); err != nil {
					t.Fatal(err)
				}
			} else if !interrupted {
				t.Fatal("interrupt event did not invoke cancellation")
			}
			if creates != 1 || outcome.Receipt.SessionID != "created-session" {
				t.Fatalf("creates=%d outcome=%#v", creates, outcome)
			}
			raw, err := os.ReadFile(journal.path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"session_id":"created-session"`) {
				t.Fatalf("durable event receipt:\n%s", raw)
			}
		})
	}
}
