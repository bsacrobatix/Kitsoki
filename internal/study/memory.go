package study

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type state struct {
	Snapshot       Snapshot `json:"snapshot"`
	Events         []Event  `json:"events"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// MemoryStore is the deterministic no-network implementation used by unit and
// cassette flows. SQLiteStore uses the same state transitions.
type MemoryStore struct {
	mu   sync.Mutex
	now  func() time.Time
	byID map[string]state
	keys map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{now: time.Now, byID: map[string]state{}, keys: map[string]string{}}
}
func (s *MemoryStore) SetClock(now func() time.Time) {
	if now != nil {
		s.mu.Lock()
		s.now = now
		s.mu.Unlock()
	}
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }

func (s *MemoryStore) Submit(_ context.Context, req SubmitRequest) (Study, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.IdempotencyKey == "" {
		return Study{}, false, fmt.Errorf("study submit requires idempotency_key")
	}
	if err := req.Plan.Validate(); err != nil {
		return Study{}, false, err
	}
	if id := s.keys[req.IdempotencyKey]; id != "" {
		return clone(s.byID[id].Snapshot.Study), false, nil
	}
	now := s.now().UTC()
	st := Study{ID: newID(), Plan: clone(req.Plan), Phase: PhaseReady, Budget: req.Plan.Budget, CreatedAt: now, UpdatedAt: now}
	state := state{Snapshot: Snapshot{Study: st}, IdempotencyKey: req.IdempotencyKey}
	state.emit(now, "study.submitted", "", "", "", map[string]string{"digest": req.Plan.Digest, "revision": req.Plan.Revision})
	for _, w := range req.Plan.Waves {
		for _, p := range w.Cells {
			phase, reason := CellQueued, ""
			if len(p.DependsOn) > 0 {
				phase, reason = CellBlocked, "waiting for predecessor evidence"
			}
			a := Attempt{ID: newID(), Number: 1, Phase: phase, CreatedAt: now}
			state.Snapshot.Cells = append(state.Snapshot.Cells, Cell{StudyID: st.ID, WaveID: w.ID, ID: p.ID, Phase: phase, BlockedReason: reason, Worker: p.Worker, Attempts: []Attempt{a}})
			state.emit(now, "cell.created", w.ID, p.ID, a.ID, map[string]string{"phase": string(phase)})
		}
	}
	s.byID[st.ID] = state
	s.keys[req.IdempotencyKey] = st.ID
	return clone(st), true, nil
}
func (st *state) emit(at time.Time, kind, wave, cell, attempt string, detail map[string]string) {
	st.Events = append(st.Events, Event{StudyID: st.Snapshot.Study.ID, Sequence: int64(len(st.Events) + 1), At: at, Kind: kind, WaveID: wave, CellID: cell, AttemptID: attempt, Detail: detail})
}
func (s *MemoryStore) Get(_ context.Context, id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return Snapshot{}, fmt.Errorf("study %q not found", id)
	}
	out := clone(st.Snapshot)
	return out, nil
}
func (s *MemoryStore) List(_ context.Context) ([]Study, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Study, 0, len(s.byID))
	for _, st := range s.byID {
		out = append(out, clone(st.Snapshot.Study))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func (s *MemoryStore) Events(_ context.Context, id string, since int64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("study %q not found", id)
	}
	out := []Event{}
	for _, e := range st.Events {
		if e.Sequence > since {
			out = append(out, e)
		}
	}
	return clone(out), nil
}
func (s *MemoryStore) Retry(_ context.Context, id, cellID string) (Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return Attempt{}, fmt.Errorf("study %q not found", id)
	}
	c := findCell(&st.Snapshot, cellID)
	if c == nil {
		return Attempt{}, fmt.Errorf("cell %q not found", cellID)
	}
	if !terminal(c.Phase) {
		return Attempt{}, fmt.Errorf("cell %q is not terminal", cellID)
	}
	now := s.now().UTC()
	a := Attempt{ID: newID(), Number: len(c.Attempts) + 1, Phase: CellQueued, CreatedAt: now}
	c.Attempts = append(c.Attempts, a)
	c.Phase = CellQueued
	c.BlockedReason = ""
	st.Snapshot.Study.Phase = PhaseRunning
	st.Snapshot.Study.UpdatedAt = now
	st.emit(now, "attempt.retried", c.WaveID, c.ID, a.ID, nil)
	s.byID[id] = st
	return clone(a), nil
}
func (s *MemoryStore) Cancel(_ context.Context, id, cellID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("study %q not found", id)
	}
	c := findCell(&st.Snapshot, cellID)
	if c == nil {
		return fmt.Errorf("cell %q not found", cellID)
	}
	if terminal(c.Phase) {
		return nil
	}
	now := s.now().UTC()
	c.Phase = CellCancelled
	a := &c.Attempts[len(c.Attempts)-1]
	a.Phase = CellCancelled
	a.FinishedAt = &now
	st.Snapshot.Study.UpdatedAt = now
	st.emit(now, "cell.cancelled", c.WaveID, c.ID, a.ID, nil)
	s.byID[id] = st
	return nil
}
func (s *MemoryStore) Record(_ context.Context, id, cellID, attemptID string, result Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("study %q not found", id)
	}
	c := findCell(&st.Snapshot, cellID)
	if c == nil {
		return fmt.Errorf("cell %q not found", cellID)
	}
	a := findAttempt(c, attemptID)
	if a == nil {
		return fmt.Errorf("attempt %q not found", attemptID)
	}
	if terminal(a.Phase) {
		return fmt.Errorf("attempt %q is immutable", attemptID)
	}
	now := s.now().UTC()
	a.Cost = result.Cost
	a.ResultDigest = result.ResultDigest
	a.FailureKind = result.FailureKind
	a.FinishedAt = &now
	if result.FailureKind != "" {
		a.Phase = CellFailed
		c.Phase = CellFailed
	} else {
		a.Phase = CellComplete
		c.Phase = CellComplete
	}
	st.Snapshot.Study.Budget.Used += result.Cost
	st.Snapshot.Study.UpdatedAt = now
	if result.Attention != nil {
		st.Snapshot.Study.Attention = clone(result.Attention)
		st.Snapshot.Study.Phase = PhaseAttention
		st.emit(now, "study.attention", c.WaveID, c.ID, a.ID, map[string]string{"kind": result.Attention.Kind})
	} else {
		release(&st.Snapshot)
		if allTerminal(st.Snapshot.Cells) {
			st.Snapshot.Study.Phase = PhaseComplete
		} else {
			st.Snapshot.Study.Phase = PhaseRunning
		}
	}
	if st.Snapshot.Study.Budget.Limit > 0 && st.Snapshot.Study.Budget.Used >= st.Snapshot.Study.Budget.Limit {
		st.Snapshot.Study.Phase = PhasePaused
		st.emit(now, "study.budget_stopped", c.WaveID, c.ID, a.ID, nil)
	}
	st.emit(now, "attempt.completed", c.WaveID, c.ID, a.ID, map[string]string{"failure_kind": result.FailureKind, "result_digest": result.ResultDigest})
	s.byID[id] = st
	return nil
}
func findCell(s *Snapshot, id string) *Cell {
	for i := range s.Cells {
		if s.Cells[i].ID == id {
			return &s.Cells[i]
		}
	}
	return nil
}
func findAttempt(c *Cell, id string) *Attempt {
	for i := range c.Attempts {
		if c.Attempts[i].ID == id {
			return &c.Attempts[i]
		}
	}
	return nil
}
func release(s *Snapshot) {
	for i := range s.Cells {
		c := &s.Cells[i]
		if c.Phase != CellBlocked {
			continue
		}
		ready := true
		for _, dep := range dependencies(s.Study.Plan, c.ID) {
			d := findCell(s, dep)
			if d == nil || d.Phase != CellComplete {
				ready = false
				break
			}
		}
		if ready {
			c.Phase = CellQueued
			c.BlockedReason = ""
			c.Attempts[len(c.Attempts)-1].Phase = CellQueued
		}
	}
}
func dependencies(p Plan, id string) []string {
	for _, w := range p.Waves {
		for _, c := range w.Cells {
			if c.ID == id {
				return c.DependsOn
			}
		}
	}
	return nil
}
func allTerminal(cells []Cell) bool {
	for _, c := range cells {
		if !terminal(c.Phase) {
			return false
		}
	}
	return true
}
