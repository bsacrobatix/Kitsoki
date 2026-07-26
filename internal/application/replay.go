package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var ErrIdempotencyConflict = errors.New("application: idempotency key reused with different input")

type ReplayKey struct {
	HandlerID   string `json:"handler_id"`
	SessionID   string `json:"session_id,omitempty"`
	Key         string `json:"key"`
	InputDigest string `json:"input_digest"`
}

type ReplayStore interface {
	Do(context.Context, ReplayKey, func() (OutcomeEnvelope, error)) (OutcomeEnvelope, error, bool)
}

type replayRecord struct {
	inputDigest string
	outcome     OutcomeEnvelope
	errText     string
	pending     chan struct{}
}

// MemoryReplayStore provides atomic in-process replay and request coalescing.
// Durable adapters may wrap it and journal completed outcomes.
type MemoryReplayStore struct {
	mu      sync.Mutex
	records map[string]*replayRecord
}

func NewMemoryReplayStore() *MemoryReplayStore {
	return &MemoryReplayStore{records: make(map[string]*replayRecord)}
}

func (s *MemoryReplayStore) Do(ctx context.Context, key ReplayKey, execute func() (OutcomeEnvelope, error)) (OutcomeEnvelope, error, bool) {
	identity := replayIdentity(key)
	for {
		s.mu.Lock()
		record, ok := s.records[identity]
		if ok {
			if record.inputDigest != key.InputDigest {
				s.mu.Unlock()
				return OutcomeEnvelope{}, fmt.Errorf("%w: handler %q key %q", ErrIdempotencyConflict, key.HandlerID, key.Key), false
			}
			if record.pending != nil {
				wait := record.pending
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return OutcomeEnvelope{}, ctx.Err(), false
				case <-wait:
					continue
				}
			}
			outcome := cloneOutcome(record.outcome)
			errText := record.errText
			s.mu.Unlock()
			if errText != "" {
				return outcome, errors.New(errText), true
			}
			return outcome, nil, true
		}
		pending := make(chan struct{})
		s.records[identity] = &replayRecord{inputDigest: key.InputDigest, pending: pending}
		s.mu.Unlock()

		outcome, err := execute()
		s.mu.Lock()
		record = s.records[identity]
		if outcome.Receipt.ID == "" {
			delete(s.records, identity)
		} else {
			record.outcome = cloneOutcome(outcome)
			if err != nil {
				record.errText = err.Error()
			}
			record.pending = nil
		}
		close(pending)
		s.mu.Unlock()
		return outcome, err, false
	}
}

// Seed restores a completed replay record from a durable adapter.
func (s *MemoryReplayStore) Seed(key ReplayKey, outcome OutcomeEnvelope, errText string) error {
	if outcome.Receipt.ID == "" {
		return fmt.Errorf("application: cannot seed replay without a receipt")
	}
	identity := replayIdentity(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[identity]; ok && existing.inputDigest != key.InputDigest {
		return fmt.Errorf("%w: handler %q key %q", ErrIdempotencyConflict, key.HandlerID, key.Key)
	}
	s.records[identity] = &replayRecord{
		inputDigest: key.InputDigest, outcome: cloneOutcome(outcome), errText: errText,
	}
	return nil
}

func (s *MemoryReplayStore) Forget(key ReplayKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, replayIdentity(key))
}

func replayIdentity(key ReplayKey) string {
	return key.HandlerID + "\x00" + key.SessionID + "\x00" + key.Key
}

func cloneOutcome(outcome OutcomeEnvelope) OutcomeEnvelope {
	raw, err := json.Marshal(outcome)
	if err != nil {
		return outcome
	}
	var clone OutcomeEnvelope
	if err := json.Unmarshal(raw, &clone); err != nil {
		return outcome
	}
	return clone
}
