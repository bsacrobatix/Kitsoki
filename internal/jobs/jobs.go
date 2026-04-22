// Package jobs implements the background job scheduler (§4).
//
// # Overview
//
// The Scheduler interface accepts JobSpecs and runs them as goroutines, one
// per job. Each job runs a host.Handler (with context for cancellation). The
// supervisor scans for stale running jobs on startup and marks them failed
// with error="process_died_mid_job" (§4.2 recovery).
//
// # Storage
//
// Jobs and notifications are persisted in two new SQLite tables introduced via
// a migration applied on Open(). The store is session-scoped (all rows carry
// session_id); no FK constraints.
//
// # Determinism / replay
//
// Host invocation inputs and outputs are written to the event log (via the
// existing store) so replay can substitute recorded results. The job row is
// materialized current-state; the event log is authoritative.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"hally/internal/app"
	"hally/internal/host"
	"hally/internal/ulid"
)

// JobID is a ULID string uniquely identifying a job.
type JobID = string

// JobStatus is the lifecycle state of a job.
type JobStatus string

const (
	JobRunning           JobStatus = "running"
	JobAwaitingInput     JobStatus = "awaiting_input"
	JobDone              JobStatus = "done"
	JobFailed            JobStatus = "failed"
	JobCancelled         JobStatus = "cancelled"
	ErrProcessDied       string    = "process_died_mid_job"
)

// JobSpec describes a job to be submitted.
type JobSpec struct {
	// SessionID ties the job to a session.
	SessionID app.SessionID
	// Kind is the handler name (e.g. "host.run_tests").
	Kind string
	// OriginState is the room where the job was spawned.
	OriginState app.StatePath
	// OriginProposalID is the proposal that spawned this job (optional).
	OriginProposalID string
	// Payload is the with: args passed to the handler (JSON-serialisable).
	Payload map[string]any
	// Handler is the function to run.
	Handler host.Handler
	// HeartbeatTimeout is how long without a heartbeat before the job is
	// considered stale. Default 60s.
	HeartbeatTimeout time.Duration
}

// JobEvent is emitted on the subscription channel when a job status changes.
type JobEvent struct {
	JobID    JobID
	Status   JobStatus
	Progress any
	Result   *host.Result
	Error    string
}

// Job is the runtime representation of a submitted job.
type Job struct {
	ID               JobID
	SessionID        app.SessionID
	Kind             string
	Status           JobStatus
	OriginState      app.StatePath
	OriginProposalID string
	Payload          map[string]any
	Progress         any
	Result           *host.Result
	Error            string
	RetryCount       int
	CreatedAt        time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time

	// ClarificationSchema is set when status==awaiting_input.
	ClarificationSchema any
	// ClarificationAnswer is set once the user submits an answer.
	ClarificationAnswer any
}

// Scheduler is the interface for submitting and managing background jobs (§4.1).
type Scheduler interface {
	// Submit queues a new job and starts executing it immediately.
	// Returns the JobID on success.
	Submit(ctx context.Context, spec JobSpec) (JobID, error)
	// Cancel requests cancellation of a running job.
	Cancel(ctx context.Context, id JobID) error
	// Subscribe returns a channel that receives events for the given job,
	// and an unsubscribe function. The channel is closed when the job terminates.
	Subscribe(id JobID) (<-chan JobEvent, func())
	// Heartbeat updates the job's progress and updated_at timestamp.
	// Returns ErrJobNotFound if the job doesn't exist.
	Heartbeat(id JobID, progress any) error
}

// ErrJobNotFound is returned when a job ID is not known.
var ErrJobNotFound = fmt.Errorf("jobs: job not found")

// inMemoryScheduler is a goroutine-per-job in-memory implementation.
// For production, the job state would be persisted to SQLite; this implementation
// is suitable for testing and demos where the process does not restart.
type inMemoryScheduler struct {
	mu      sync.RWMutex
	jobs    map[JobID]*runningJob
	cancels map[JobID]context.CancelFunc
}

// runningJob holds runtime state for one job.
type runningJob struct {
	job   Job
	subs  []chan JobEvent
	subMu sync.Mutex
	done  chan struct{}
}

// NewInMemoryScheduler creates a new in-memory Scheduler.
// On startup it cannot recover stale jobs (no persistence); use the SQLite
// variant in production.
func NewInMemoryScheduler() Scheduler {
	return &inMemoryScheduler{
		jobs:    make(map[JobID]*runningJob),
		cancels: make(map[JobID]context.CancelFunc),
	}
}

func (s *inMemoryScheduler) Submit(ctx context.Context, spec JobSpec) (JobID, error) {
	id := ulid.New()
	now := time.Now()

	rj := &runningJob{
		job: Job{
			ID:               id,
			SessionID:        spec.SessionID,
			Kind:             spec.Kind,
			Status:           JobRunning,
			OriginState:      spec.OriginState,
			OriginProposalID: spec.OriginProposalID,
			Payload:          spec.Payload,
			CreatedAt:        now,
			UpdatedAt:        now,
			StartedAt:        &now,
		},
		done: make(chan struct{}),
	}

	jobCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.jobs[id] = rj
	s.cancels[id] = cancel
	s.mu.Unlock()

	// Run the handler in a goroutine.
	go func() {
		defer close(rj.done)

		result, err := spec.Handler(jobCtx, spec.Payload)
		now := time.Now()

		s.mu.Lock()
		rj.job.FinishedAt = &now
		rj.job.UpdatedAt = now

		var ev JobEvent
		ev.JobID = id

		if err != nil {
			if jobCtx.Err() == context.Canceled {
				rj.job.Status = JobCancelled
				ev.Status = JobCancelled
			} else {
				rj.job.Status = JobFailed
				rj.job.Error = err.Error()
				ev.Status = JobFailed
				ev.Error = err.Error()
			}
		} else if result.Error != "" {
			rj.job.Status = JobFailed
			rj.job.Error = result.Error
			rj.job.Result = &result
			ev.Status = JobFailed
			ev.Error = result.Error
			ev.Result = &result
		} else {
			rj.job.Status = JobDone
			rj.job.Result = &result
			ev.Status = JobDone
			ev.Result = &result
		}
		s.mu.Unlock()

		s.fanout(rj, ev)
		delete(s.cancels, id)
	}()

	return id, nil
}

func (s *inMemoryScheduler) Cancel(ctx context.Context, id JobID) error {
	s.mu.RLock()
	cancel, ok := s.cancels[id]
	s.mu.RUnlock()
	if !ok {
		return ErrJobNotFound
	}
	cancel()
	return nil
}

func (s *inMemoryScheduler) Subscribe(id JobID) (<-chan JobEvent, func()) {
	s.mu.RLock()
	rj, ok := s.jobs[id]
	s.mu.RUnlock()

	if !ok {
		// Return a closed channel.
		ch := make(chan JobEvent)
		close(ch)
		return ch, func() {}
	}

	ch := make(chan JobEvent, 8)

	rj.subMu.Lock()
	rj.subs = append(rj.subs, ch)
	rj.subMu.Unlock()

	unsub := func() {
		rj.subMu.Lock()
		defer rj.subMu.Unlock()
		for i, sub := range rj.subs {
			if sub == ch {
				rj.subs = append(rj.subs[:i], rj.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}

	// If job is already done, send a terminal event and close.
	s.mu.RLock()
	status := rj.job.Status
	s.mu.RUnlock()
	if status == JobDone || status == JobFailed || status == JobCancelled {
		go func() {
			ev := JobEvent{JobID: id, Status: status}
			ch <- ev
			unsub()
		}()
	}

	return ch, unsub
}

func (s *inMemoryScheduler) Heartbeat(id JobID, progress any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rj, ok := s.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	rj.job.Progress = progress
	rj.job.UpdatedAt = time.Now()

	ev := JobEvent{JobID: id, Status: rj.job.Status, Progress: progress}
	s.fanoutLocked(rj, ev)
	return nil
}

// Get returns a snapshot of the job (safe to read, not modify).
func (s *inMemoryScheduler) Get(id JobID) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rj, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	// Return a copy.
	return rj.job, true
}

// fanout broadcasts an event to all subscribers (must be called without holding s.mu).
func (s *inMemoryScheduler) fanout(rj *runningJob, ev JobEvent) {
	rj.subMu.Lock()
	defer rj.subMu.Unlock()
	for _, ch := range rj.subs {
		select {
		case ch <- ev:
		default:
			// Drop if buffer full; subscriber is too slow.
		}
	}
}

// fanoutLocked broadcasts an event (called while holding s.mu).
func (s *inMemoryScheduler) fanoutLocked(rj *runningJob, ev JobEvent) {
	rj.subMu.Lock()
	defer rj.subMu.Unlock()
	for _, ch := range rj.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// PayloadJSON serializes the payload to JSON for storage.
func PayloadJSON(payload map[string]any) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
