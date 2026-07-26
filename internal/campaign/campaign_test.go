package campaign

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/store"
)

func TestCatalogSourceDiscoversOnlyWiredApplication(t *testing.T) {
	path := writeCampaignCatalog(t, "story-intent")
	source := CatalogSource{Path: path}
	defs, err := source.Discover(context.Background(), "runner")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(defs) != 1 || defs[0].ID != "campaign-one" {
		t.Fatalf("definitions = %#v", defs)
	}
	if defs[0].Action.Story != "stories/worker/app.yaml" ||
		defs[0].Action.Intent != "tick" ||
		defs[0].Budget.MaxTicksPerDay != 2 ||
		defs[0].Budget.MaxConcurrency != 1 {
		t.Fatalf("definition = %#v", defs[0])
	}
}

func TestCatalogSourceRejectsCommandAuthority(t *testing.T) {
	path := writeCampaignCatalog(t, "script")
	_, err := (CatalogSource{Path: path}).Discover(context.Background(), "runner")
	if err == nil || !strings.Contains(err.Error(), `action.kind must be "story-intent"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestMemoryStoreEnforcesCadenceBudgetConcurrencyAndRestartTruth(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	start := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	def := testDefinition()
	if err := store.Reconcile(ctx, "runner", []Definition{def}, start); err != nil {
		t.Fatal(err)
	}

	claims, err := store.ClaimDue(ctx, "runner", start, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("first claims = %#v, %v", claims, err)
	}
	first := claims[0]
	claims, err = store.ClaimDue(ctx, "runner", start.Add(time.Minute), 10)
	if err != nil || len(claims) != 0 {
		t.Fatalf("concurrent claims = %#v, %v", claims, err)
	}

	if interrupted, err := store.InterruptRunning(ctx, "daemon_restarted", start.Add(2*time.Minute)); err != nil || interrupted != 1 {
		t.Fatalf("interrupt = %d, %v", interrupted, err)
	}
	schedules, err := store.List(ctx, "runner", 10)
	if err != nil || len(schedules) != 1 || schedules[0].Running != 0 ||
		schedules[0].LastStatus != "interrupted" {
		t.Fatalf("restart schedules = %#v, %v", schedules, err)
	}

	claims, err = store.ClaimDue(ctx, "runner", start.Add(5*time.Minute), 10)
	if err != nil || len(claims) != 1 || claims[0].IdempotencyKey == first.IdempotencyKey {
		t.Fatalf("second cadence claims = %#v, %v", claims, err)
	}
	if err := store.Complete(ctx, claims[0], "job-durable", nil, start.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	claims, err = store.ClaimDue(ctx, "runner", start.Add(10*time.Minute), 10)
	if err != nil || len(claims) != 0 {
		t.Fatalf("daily budget claims = %#v, %v", claims, err)
	}
	claims, err = store.ClaimDue(ctx, "runner", start.Add(24*time.Hour), 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("next-day claims = %#v, %v", claims, err)
	}
}

func TestSQLiteStorePersistsScheduleAndInterruptsOnlyProcessWork(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqlite, err := NewSQLiteStore(db.DB())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	def := testDefinition()
	if err := sqlite.Reconcile(context.Background(), "runner", []Definition{def}, start); err != nil {
		t.Fatal(err)
	}
	claims, err := sqlite.ClaimDue(context.Background(), "runner", start, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims = %#v, %v", claims, err)
	}
	if n, err := sqlite.InterruptRunning(context.Background(), "daemon_restarted", start.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("interrupt = %d, %v", n, err)
	}

	reopened, err := NewSQLiteStore(db.DB())
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := reopened.List(context.Background(), "runner", 10)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %#v, %v", schedules, err)
	}
	if schedules[0].NextDueAt != start.Add(5*time.Minute) ||
		schedules[0].Running != 0 ||
		schedules[0].LastStatus != "interrupted" {
		t.Fatalf("restored schedule = %#v", schedules[0])
	}
}

func TestServiceWatchIsSingletonAndSnapshotIsBounded(t *testing.T) {
	start := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	scheduler := &captureScheduler{}
	service := &Service{
		Store:      NewMemoryStore(),
		Source:     staticSource{defs: []Definition{testDefinition()}},
		Scheduler:  scheduler,
		Clock:      clock.NewFake(start),
		Dispatcher: captureDispatcher{},
	}
	first, err := service.Watch(context.Background(), "runner", 5)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Watch(context.Background(), "runner", 5)
	if err != nil {
		t.Fatal(err)
	}
	if first["watch_job_ref"] != second["watch_job_ref"] || scheduler.submitCount != 1 {
		t.Fatalf("watch results = %#v %#v, submits=%d", first, second, scheduler.submitCount)
	}
	jobRefs, ok := first["job_refs"].([]any)
	if !ok || len(jobRefs) != 1 || jobRefs[0] != "job-1" {
		t.Fatalf("immediate durable job refs = %#v", first["job_refs"])
	}
	snapshot, err := service.Snapshot(context.Background(), "runner", 10, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	counts := snapshot["counts"].(map[string]any)
	if counts["campaigns"] != 1 || counts["enabled"] != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, err := service.Snapshot(context.Background(), "runner", 0, 64*1024); err == nil {
		t.Fatal("expected zero max_campaigns to fail closed")
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteStoreResetsDailyBudgetAtUTCDateBoundary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqlite, err := NewSQLiteStore(db.DB())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 26, 23, 58, 0, 0, time.UTC)
	def := testDefinition()
	def.Budget.MaxTicksPerDay = 1
	if err := sqlite.Reconcile(context.Background(), "runner", []Definition{def}, start); err != nil {
		t.Fatal(err)
	}
	claims, err := sqlite.ClaimDue(context.Background(), "runner", start, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("first claims = %#v, %v", claims, err)
	}
	if err := sqlite.Complete(context.Background(), claims[0], "job-1", nil, start); err != nil {
		t.Fatal(err)
	}
	claims, err = sqlite.ClaimDue(context.Background(), "runner", start.Add(5*time.Minute), 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("next UTC day claims = %#v, %v", claims, err)
	}
}

func testDefinition() Definition {
	return Definition{
		ID: "campaign-one", AppID: "runner", Title: "Campaign one",
		Enabled: true, Cadence: 5 * time.Minute,
		Budget: Budget{MaxTicksPerDay: 2, MaxConcurrency: 1},
		Action: Action{
			Story: "stories/worker/app.yaml", Intent: "tick",
			Input: map[string]any{"scope": "all"},
		},
		DefinitionHash: "definition-one",
	}
}

type staticSource struct {
	defs []Definition
}

func (s staticSource) Discover(context.Context, string) ([]Definition, error) {
	return append([]Definition(nil), s.defs...), nil
}

type captureScheduler struct {
	mu          sync.Mutex
	submitCount int
	cancelled   []string
}

func (s *captureScheduler) Submit(_ context.Context, _ string, _ func(context.Context) error) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submitCount++
	return "watch-1", nil
}

func (s *captureScheduler) Cancel(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = append(s.cancelled, ref)
	return nil
}

func (s *captureScheduler) WaitIdle(context.Context) error { return nil }

type captureDispatcher struct{}

func (captureDispatcher) Dispatch(context.Context, Claim) (string, error) {
	return "job-1", nil
}

func writeCampaignCatalog(t *testing.T, kind string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	body := `schema: project-object-graph/seed-catalog/v0
catalog:
  id: campaign-fixture
type_registry:
  - id: core-node
    schema: graph-type/v0
    extends: null
    required_fields: [id, schema, title, status, visibility]
  - id: campaign
    schema: graph-type/v0
    extends: core-node
nodes:
  - schema: fixture/campaign/v1
    id: campaign-one
    title: Campaign one
    status: active
    visibility: internal
    application_id: runner
    enabled: true
    paused: false
    cadence_seconds: 300
    budget:
      max_ticks_per_day: 2
      max_concurrency: 1
    action:
      kind: ` + kind + `
      story: stories/worker/app.yaml
      intent: tick
      input:
        scope: all
  - schema: fixture/campaign/v1
    id: campaign-other
    title: Other app campaign
    status: active
    visibility: internal
    application_id: other
    enabled: true
    cadence_seconds: 300
    budget:
      max_ticks_per_day: 1
      max_concurrency: 1
    action:
      kind: story-intent
      story: stories/other/app.yaml
      intent: tick
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
