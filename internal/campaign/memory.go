package campaign

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the deterministic test implementation of Store.
type MemoryStore struct {
	mu         sync.Mutex
	schedules  map[string]Schedule
	dispatches map[string]Dispatch
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		schedules:  make(map[string]Schedule),
		dispatches: make(map[string]Dispatch),
	}
}

func scheduleKey(appID, campaignID string) string { return appID + "\x00" + campaignID }

func (s *MemoryStore) Reconcile(_ context.Context, appID string, defs []Definition, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now = now.UTC()
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		if def.AppID != appID {
			return fmt.Errorf("campaign: definition %q belongs to app %q, not %q", def.ID, def.AppID, appID)
		}
		key := scheduleKey(appID, def.ID)
		seen[key] = true
		current, exists := s.schedules[key]
		nextDue := now
		lastDispatch := current.LastDispatchAt
		if exists {
			nextDue = current.NextDueAt
			if current.DefinitionHash != def.DefinitionHash && lastDispatch != nil {
				candidate := lastDispatch.Add(def.Cadence)
				if candidate.After(now) {
					nextDue = candidate
				} else {
					nextDue = now
				}
			}
		}
		current.Definition = def
		current.NextDueAt = nextDue
		current.UpdatedAt = now
		s.schedules[key] = current
	}
	for key, current := range s.schedules {
		if current.AppID == appID && !seen[key] {
			delete(s.schedules, key)
		}
	}
	return nil
}

func (s *MemoryStore) ClaimDue(_ context.Context, appID string, now time.Time, limit int) ([]Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now = now.UTC()
	if limit <= 0 {
		return nil, fmt.Errorf("campaign: claim limit must be positive")
	}
	day := now.Format("2006-01-02")
	var candidates []string
	for key, schedule := range s.schedules {
		if schedule.AppID != appID || !schedule.Enabled || schedule.Paused || schedule.NextDueAt.After(now) {
			continue
		}
		candidates = append(candidates, key)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := s.schedules[candidates[i]], s.schedules[candidates[j]]
		if !left.NextDueAt.Equal(right.NextDueAt) {
			return left.NextDueAt.Before(right.NextDueAt)
		}
		return left.ID < right.ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	claims := make([]Claim, 0, len(candidates))
	for _, key := range candidates {
		schedule := s.schedules[key]
		if schedule.TicksDay != day {
			schedule.TicksDay = day
			schedule.TicksToday = 0
		}
		if schedule.TicksToday >= schedule.Budget.MaxTicksPerDay ||
			schedule.Running >= schedule.Budget.MaxConcurrency {
			s.schedules[key] = schedule
			continue
		}
		dueAt := schedule.NextDueAt.UTC()
		idempotencyKey := tickKey(appID, schedule.ID, dueAt)
		if _, exists := s.dispatches[idempotencyKey]; exists {
			schedule.NextDueAt = dueAt.Add(schedule.Cadence)
			schedule.UpdatedAt = now
			s.schedules[key] = schedule
			continue
		}
		s.dispatches[idempotencyKey] = Dispatch{
			IdempotencyKey: idempotencyKey,
			AppID:          appID,
			CampaignID:     schedule.ID,
			DueAt:          dueAt,
			Status:         "running",
			StartedAt:      now,
		}
		schedule.TicksToday++
		schedule.Running++
		schedule.LastStatus = "running"
		schedule.LastError = ""
		schedule.NextDueAt = dueAt.Add(schedule.Cadence)
		schedule.UpdatedAt = now
		s.schedules[key] = schedule
		claims = append(claims, Claim{
			Definition:     schedule.Definition,
			IdempotencyKey: idempotencyKey,
			DueAt:          dueAt,
		})
	}
	return claims, nil
}

func (s *MemoryStore) Complete(_ context.Context, claim Claim, jobRef string, dispatchErr error, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.dispatches[claim.IdempotencyKey]
	if !ok {
		return fmt.Errorf("campaign: dispatch claim %q not found", claim.IdempotencyKey)
	}
	if record.Status != "running" {
		return nil
	}
	status := "done"
	errorText := ""
	if dispatchErr != nil {
		status = "failed"
		errorText = dispatchErr.Error()
	}
	now = now.UTC()
	record.JobRef = jobRef
	record.Status = status
	record.Error = errorText
	record.FinishedAt = &now
	s.dispatches[claim.IdempotencyKey] = record

	key := scheduleKey(claim.AppID, claim.ID)
	schedule, ok := s.schedules[key]
	if !ok {
		return nil
	}
	if schedule.Running > 0 {
		schedule.Running--
	}
	schedule.LastDispatchAt = &now
	schedule.LastJobRef = jobRef
	schedule.LastStatus = status
	schedule.LastError = errorText
	schedule.UpdatedAt = now
	s.schedules[key] = schedule
	return nil
}

func (s *MemoryStore) InterruptRunning(_ context.Context, reason string, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now = now.UTC()
	var count int64
	for key, record := range s.dispatches {
		if record.Status != "running" {
			continue
		}
		record.Status = "interrupted"
		record.Error = reason
		record.FinishedAt = &now
		s.dispatches[key] = record
		count++
	}
	for key, schedule := range s.schedules {
		if schedule.Running == 0 {
			continue
		}
		schedule.Running = 0
		schedule.LastStatus = "interrupted"
		schedule.LastError = reason
		schedule.UpdatedAt = now
		s.schedules[key] = schedule
	}
	return count, nil
}

func (s *MemoryStore) List(_ context.Context, appID string, limit int) ([]Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Schedule
	for _, schedule := range s.schedules {
		if schedule.AppID == appID {
			out = append(out, schedule)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func tickKey(appID, campaignID string, dueAt time.Time) string {
	return fmt.Sprintf("%s/%s/%d", appID, campaignID, dueAt.UTC().UnixNano())
}
