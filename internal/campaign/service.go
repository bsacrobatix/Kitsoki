package campaign

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"kitsoki/internal/clock"
)

const (
	minPollSeconds = 1
	maxPollSeconds = 3600
)

// Service coordinates graph reconciliation, durable cadence claims, and exact
// story-intent dispatch. It owns no product behavior.
type Service struct {
	Store      Store
	Source     Source
	Scheduler  Scheduler
	Clock      clock.Clock
	Dispatcher Dispatcher

	mu       sync.Mutex
	watchers map[string]string
}

// Close cancels process-bound watchers. Durable schedules and dispatch history
// remain in Store for the next daemon process.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	refs := make([]string, 0, len(s.watchers))
	for _, ref := range s.watchers {
		refs = append(refs, ref)
	}
	s.watchers = make(map[string]string)
	s.mu.Unlock()
	var firstErr error
	for _, ref := range refs {
		if err := s.Scheduler.Cancel(ctx, ref); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.Scheduler.WaitIdle(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Watch reconciles the current app-scoped graph definitions and ensures one
// process-bound watcher exists for the app. The durable schedule state is
// independent of the returned scheduler reference.
func (s *Service) Watch(ctx context.Context, appID string, pollSeconds int) (map[string]any, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if appID == "" {
		return nil, fmt.Errorf("campaign: application id is required")
	}
	if pollSeconds < minPollSeconds || pollSeconds > maxPollSeconds {
		return nil, fmt.Errorf(
			"campaign: poll_seconds must be between %d and %d, got %d",
			minPollSeconds, maxPollSeconds, pollSeconds,
		)
	}
	defs, err := s.Source.Discover(ctx, appID)
	if err != nil {
		return nil, err
	}
	now := s.Clock.Now().UTC()
	if err := s.Store.Reconcile(ctx, appID, defs, now); err != nil {
		return nil, err
	}
	jobRefs, err := s.dispatchDue(ctx, appID, now)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.watchers == nil {
		s.watchers = make(map[string]string)
	}
	if ref := s.watchers[appID]; ref != "" {
		s.mu.Unlock()
		return map[string]any{
			"watch_job_ref":  ref,
			"job_id":         ref,
			"job_refs":       stringListAny(jobRefs),
			"campaign_count": len(defs),
			"restored":       true,
		}, nil
	}

	period := time.Duration(pollSeconds) * time.Second
	ref, err := s.Scheduler.Submit(ctx, "campaign.watch:"+appID, func(watchCtx context.Context) error {
		defer func() {
			s.mu.Lock()
			delete(s.watchers, appID)
			s.mu.Unlock()
		}()
		for {
			if err := s.reconcileAndDispatch(watchCtx, appID); err != nil {
				return err
			}
			select {
			case <-watchCtx.Done():
				return watchCtx.Err()
			case <-s.Clock.After(period):
			}
		}
	})
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("campaign: start watcher: %w", err)
	}
	s.watchers[appID] = ref
	s.mu.Unlock()
	return map[string]any{
		"watch_job_ref":  ref,
		"job_id":         ref,
		"job_refs":       stringListAny(jobRefs),
		"campaign_count": len(defs),
		"restored":       false,
	}, nil
}

// Snapshot returns bounded deterministic schedule status for the wired app.
func (s *Service) Snapshot(ctx context.Context, appID string, maxCampaigns, maxBytes int) (map[string]any, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if maxCampaigns < 1 || maxCampaigns > DefaultMaxDefinitions {
		return nil, fmt.Errorf(
			"campaign: max_campaigns must be between 1 and %d, got %d",
			DefaultMaxDefinitions, maxCampaigns,
		)
	}
	if maxBytes < 1 || maxBytes > DefaultMaxBytes {
		return nil, fmt.Errorf(
			"campaign: max_bytes must be between 1 and %d, got %d",
			DefaultMaxBytes, maxBytes,
		)
	}
	schedules, err := s.Store.List(ctx, appID, maxCampaigns+1)
	if err != nil {
		return nil, err
	}
	if len(schedules) > maxCampaigns {
		return nil, fmt.Errorf(
			"campaign: selected more than %d schedules; refusing to truncate",
			maxCampaigns,
		)
	}
	snapshot := buildSnapshot(appID, schedules)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("campaign: encode snapshot size check: %w", err)
	}
	if len(encoded) > maxBytes {
		return nil, fmt.Errorf(
			"campaign: snapshot encodes to %d bytes, exceeding %d; refusing to truncate",
			len(encoded), maxBytes,
		)
	}
	return snapshot, nil
}

// Restore records the process boundary without advancing or replaying a
// cadence. Definitions and next_due_at remain durable and are reconciled when
// the restored application invokes Watch.
func (s *Service) Restore(ctx context.Context) (int64, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	return s.Store.InterruptRunning(ctx, "daemon_restarted", s.Clock.Now().UTC())
}

func (s *Service) reconcileAndDispatch(ctx context.Context, appID string) error {
	defs, err := s.Source.Discover(ctx, appID)
	if err != nil {
		return err
	}
	now := s.Clock.Now().UTC()
	if err := s.Store.Reconcile(ctx, appID, defs, now); err != nil {
		return err
	}
	_, err = s.dispatchDue(ctx, appID, now)
	return err
}

// dispatchDue claims work against the same reconciliation timestamp. This
// keeps a newly discovered campaign immediately due even if wall time steps
// backward between the two operations.
func (s *Service) dispatchDue(ctx context.Context, appID string, now time.Time) ([]string, error) {
	claims, err := s.Store.ClaimDue(ctx, appID, now, DefaultMaxDefinitions)
	if err != nil {
		return nil, err
	}
	jobRefs := make([]string, 0, len(claims))
	for _, claim := range claims {
		jobRef, dispatchErr := s.Dispatcher.Dispatch(ctx, claim)
		if jobRef != "" {
			jobRefs = append(jobRefs, jobRef)
		}
		if err := s.Store.Complete(
			context.WithoutCancel(ctx), claim, jobRef, dispatchErr, s.Clock.Now().UTC(),
		); err != nil {
			return nil, err
		}
	}
	return jobRefs, nil
}

func (s *Service) validate() error {
	switch {
	case s.Store == nil:
		return fmt.Errorf("campaign: durable store is unavailable")
	case s.Source == nil:
		return fmt.Errorf("campaign: graph source is unavailable")
	case s.Scheduler == nil:
		return fmt.Errorf("campaign: scheduler is unavailable")
	case s.Clock == nil:
		return fmt.Errorf("campaign: clock is unavailable")
	case s.Dispatcher == nil:
		return fmt.Errorf("campaign: dispatcher is unavailable")
	default:
		return nil
	}
}

func buildSnapshot(appID string, schedules []Schedule) map[string]any {
	schedules = append([]Schedule(nil), schedules...)
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].ID < schedules[j].ID })
	rows := make([]any, 0, len(schedules))
	enabled := 0
	paused := 0
	running := 0
	for _, schedule := range schedules {
		if schedule.Enabled {
			enabled++
		}
		if schedule.Paused {
			paused++
		}
		running += schedule.Running
		row := map[string]any{
			"campaign_id":       schedule.ID,
			"title":             schedule.Title,
			"enabled":           schedule.Enabled,
			"paused":            schedule.Paused,
			"cadence_seconds":   int64(schedule.Cadence / time.Second),
			"max_ticks_per_day": schedule.Budget.MaxTicksPerDay,
			"max_concurrency":   schedule.Budget.MaxConcurrency,
			"ticks_today":       schedule.TicksToday,
			"running":           schedule.Running,
			"next_due_at":       schedule.NextDueAt.UTC().Format(time.RFC3339Nano),
			"last_status":       schedule.LastStatus,
		}
		if schedule.LastDispatchAt != nil {
			row["last_dispatch_at"] = schedule.LastDispatchAt.UTC().Format(time.RFC3339Nano)
		}
		if schedule.LastJobRef != "" {
			row["last_job_ref"] = schedule.LastJobRef
		}
		if schedule.LastError != "" {
			row["last_error"] = schedule.LastError
		}
		rows = append(rows, row)
	}
	return map[string]any{
		"schema":    SchemaV1,
		"app_id":    appID,
		"campaigns": rows,
		"counts": map[string]any{
			"campaigns": len(rows),
			"enabled":   enabled,
			"paused":    paused,
			"running":   running,
		},
	}
}

func stringListAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}
