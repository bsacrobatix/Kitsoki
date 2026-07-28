package workqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	SubmissionReceiptSchema = "kitsoki/work-queue-submission/v1"
	DefaultMaxInputBytes    = 64 << 10
	MaxInputBytes           = 1 << 20
	DefaultMaxAttempts      = 3
	MaxAttempts             = 20
	MaxPriority             = 100
	DefaultMaxItems         = 100
	MaxItems                = 250
	DefaultMaxSnapshotBytes = 1 << 20
	MaxSnapshotBytes        = 4 << 20
	MaxCapabilities         = 16
)

var ErrLimitExceeded = errors.New("workqueue: requested result exceeds configured limit")

// QueueConfig is server-owned policy for a single application queue. Stories
// can name a configured queue but cannot change its execution policy.
type QueueConfig struct {
	MaxInputBytes        int      `yaml:"max_input_bytes,omitempty"`
	MaxAttempts          int      `yaml:"max_attempts,omitempty"`
	RequiredCapabilities []string `yaml:"required_capabilities,omitempty"`
	Priority             int      `yaml:"priority,omitempty"`
	ProducesCode         bool     `yaml:"produces_code,omitempty"`
}

// SubmissionReceipt records a successful enqueue without exposing the job
// payload or worker routing details.
type SubmissionReceipt struct {
	Schema    string `json:"schema"`
	Ref       string `json:"ref"`
	Status    State  `json:"status"`
	Replayed  bool   `json:"replayed"`
	InputHash string `json:"input_hash"`
}

// Projection is the privacy-safe story and operator view of a job.
type Projection struct {
	Ref                  string    `json:"ref"`
	Queue                string    `json:"queue"`
	Status               State     `json:"status"`
	Priority             int       `json:"priority"`
	Attempts             int       `json:"attempts"`
	MaxAttempts          int       `json:"max_attempts"`
	RequiredCapabilities []string  `json:"required_capabilities,omitempty"`
	AvailableAt          time.Time `json:"available_at"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	Receipt              *Receipt  `json:"receipt,omitempty"`
}

type SnapshotRequest struct {
	MaxItems int
	MaxBytes int
}

// Service is the application-bound story-facing broker facade. It deliberately
// has no claim, heartbeat, completion, worker, endpoint, or credential API.
type Service struct {
	store         Store
	applicationID string
	queues        map[string]QueueConfig
}

func NewService(store Store, applicationID string, queues map[string]QueueConfig) (*Service, error) {
	if store == nil || !validIdentity(applicationID) || len(queues) == 0 || len(queues) > 64 {
		return nil, ErrInvalid
	}
	out := &Service{store: store, applicationID: strings.TrimSpace(applicationID), queues: make(map[string]QueueConfig, len(queues))}
	for name, cfg := range queues {
		if !validIdentity(name) {
			return nil, ErrInvalid
		}
		resolved, err := ResolveQueueConfig(cfg)
		if err != nil {
			return nil, err
		}
		out.queues[name] = resolved
	}
	return out, nil
}

// ResolveQueueConfig applies intentionally conservative defaults and rejects
// values that could make a story queue unbounded.
func ResolveQueueConfig(cfg QueueConfig) (QueueConfig, error) {
	if cfg.MaxInputBytes == 0 {
		cfg.MaxInputBytes = DefaultMaxInputBytes
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.MaxInputBytes < 1 || cfg.MaxInputBytes > MaxInputBytes || cfg.MaxAttempts < 1 || cfg.MaxAttempts > MaxAttempts || cfg.Priority < -MaxPriority || cfg.Priority > MaxPriority || len(cfg.RequiredCapabilities) > MaxCapabilities {
		return QueueConfig{}, ErrInvalid
	}
	cfg.RequiredCapabilities = normalized(cfg.RequiredCapabilities)
	for _, capability := range cfg.RequiredCapabilities {
		if !validIdentity(capability) {
			return QueueConfig{}, ErrInvalid
		}
	}
	return cfg, nil
}

func (s *Service) Enqueue(ctx context.Context, applicationID, queue, idempotencyKey string, input json.RawMessage) (SubmissionReceipt, error) {
	if strings.TrimSpace(applicationID) != s.applicationID {
		return SubmissionReceipt{}, ErrNotFound
	}
	cfg, ok := s.queues[strings.TrimSpace(queue)]
	if !ok || !validIdentity(idempotencyKey) {
		return SubmissionReceipt{}, ErrInvalid
	}
	payload, err := normalizeJSON(input, cfg.MaxInputBytes)
	if err != nil {
		return SubmissionReceipt{}, err
	}
	existing, err := s.store.Enqueue(ctx, EnqueueRequest{ApplicationID: s.applicationID, Queue: queue, IdempotencyKey: idempotencyKey, Payload: payload, RequiredCapabilities: cfg.RequiredCapabilities, Priority: cfg.Priority, MaxAttempts: cfg.MaxAttempts, ProducesCode: cfg.ProducesCode})
	if err != nil {
		return SubmissionReceipt{}, err
	}
	return SubmissionReceipt{Schema: SubmissionReceiptSchema, Ref: existing.ID, Status: existing.State, Replayed: existing.Replayed, InputHash: digest(payload)}, nil
}

func (s *Service) Get(ctx context.Context, applicationID, ref string) (Projection, error) {
	if strings.TrimSpace(applicationID) != s.applicationID {
		return Projection{}, ErrNotFound
	}
	j, err := s.store.Get(ctx, strings.TrimSpace(ref))
	if err != nil {
		return Projection{}, err
	}
	if j.ApplicationID != s.applicationID {
		return Projection{}, ErrNotFound
	}
	if _, ok := s.queues[j.Queue]; !ok {
		return Projection{}, ErrNotFound
	}
	return project(j), nil
}

func (s *Service) Snapshot(ctx context.Context, applicationID string, maxItems, maxBytes int) ([]Projection, error) {
	if strings.TrimSpace(applicationID) != s.applicationID {
		return nil, ErrNotFound
	}
	if maxItems == 0 {
		maxItems = DefaultMaxItems
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxSnapshotBytes
	}
	if maxItems < 1 || maxItems > MaxItems || maxBytes < 1 || maxBytes > MaxSnapshotBytes {
		return nil, ErrInvalid
	}
	queues := make([]string, 0, len(s.queues))
	for queue := range s.queues {
		queues = append(queues, queue)
	}
	sort.Strings(queues)
	out := make([]Projection, 0, maxItems)
	bytes := 2
	for _, queue := range queues {
		jobs, err := s.store.List(ctx, ListFilter{ApplicationID: s.applicationID, Queue: queue, Limit: maxItems + 1})
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if len(out) >= maxItems {
				return nil, ErrLimitExceeded
			}
			item := project(job)
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("workqueue projection: %w", err)
			}
			separator := 0
			if len(out) > 0 {
				separator = 1
			}
			if bytes+len(raw)+separator > maxBytes {
				return nil, ErrLimitExceeded
			}
			bytes += len(raw)
			if separator > 0 {
				bytes++
			}
			out = append(out, item)
		}
	}
	return out, nil
}

func normalizeJSON(raw []byte, max int) ([]byte, error) {
	if len(raw) == 0 || len(raw) > max {
		return nil, ErrInvalid
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, ErrInvalid
	}
	normalized, err := json.Marshal(value)
	if err != nil || len(normalized) > max {
		return nil, ErrInvalid
	}
	return normalized, nil
}

func project(j Job) Projection {
	p := Projection{Ref: j.ID, Queue: j.Queue, Status: j.State, Priority: j.Priority, Attempts: j.Attempts, MaxAttempts: j.MaxAttempts, RequiredCapabilities: append([]string(nil), j.RequiredCapabilities...), AvailableAt: j.AvailableAt, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt}
	if j.Receipt != nil {
		r := *j.Receipt
		r.ApplicationID = ""
		r.WorkerID = ""
		p.Receipt = &r
	}
	return p
}

func digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validIdentity(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, `/\\`) && !strings.Contains(value, "..") && !strings.Contains(value, "://")
}
