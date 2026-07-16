package study

import (
	"fmt"
	"strings"
	"time"

	"kitsoki/internal/ulid"
)

type Phase string

const (
	PhaseDraft        Phase = "draft"
	PhaseReady        Phase = "ready"
	PhaseRunning      Phase = "running"
	PhaseAttention    Phase = "attention"
	PhasePaused       Phase = "paused"
	PhaseSynthesizing Phase = "synthesizing"
	PhaseReviewReady  Phase = "review-ready"
	PhaseComplete     Phase = "complete"
)

type CellPhase string

const (
	CellBlocked       CellPhase = "blocked"
	CellQueued        CellPhase = "queued"
	CellProvisioning  CellPhase = "provisioning"
	CellNavigating    CellPhase = "navigating"
	CellEvaluating    CellPhase = "evaluating"
	CellCapturing     CellPhase = "capturing"
	CellEvidenceCheck CellPhase = "evidence-check"
	CellJudging       CellPhase = "judging"
	CellNormalizing   CellPhase = "normalizing"
	CellComplete      CellPhase = "complete"
	CellFailed        CellPhase = "failed"
	CellCancelled     CellPhase = "cancelled"
)

type Attention struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	CellID string `json:"cell_id,omitempty"`
}

type Budget struct {
	Limit    float64 `json:"limit"`
	Used     float64 `json:"used"`
	Currency string  `json:"currency,omitempty"`
}

type Plan struct {
	Revision       string            `json:"revision"`
	Digest         string            `json:"digest"`
	Waves          []Wave            `json:"waves"`
	Budget         Budget            `json:"budget"`
	Policy         map[string]string `json:"policy,omitempty"`
	WorkerBindings map[string]string `json:"worker_bindings,omitempty"`
}

type Wave struct {
	ID    string     `json:"wave_id"`
	Order int        `json:"order"`
	Cells []CellPlan `json:"cells"`
}

type CellPlan struct {
	ID        string   `json:"cell_id"`
	DependsOn []string `json:"depends_on,omitempty"`
	Worker    string   `json:"worker,omitempty"`
}

type Study struct {
	ID        string     `json:"study_id"`
	Plan      Plan       `json:"plan"`
	Phase     Phase      `json:"phase"`
	Attention *Attention `json:"attention,omitempty"`
	Budget    Budget     `json:"budget"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type Cell struct {
	StudyID       string    `json:"study_id"`
	WaveID        string    `json:"wave_id"`
	ID            string    `json:"cell_id"`
	Phase         CellPhase `json:"phase"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
	Worker        string    `json:"worker,omitempty"`
	Attempts      []Attempt `json:"attempts"`
}

type Attempt struct {
	ID           string     `json:"attempt_id"`
	Number       int        `json:"number"`
	Phase        CellPhase  `json:"phase"`
	Cost         float64    `json:"cost"`
	ResultDigest string     `json:"result_digest,omitempty"`
	FailureKind  string     `json:"failure_kind,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

type Event struct {
	StudyID   string            `json:"study_id"`
	Sequence  int64             `json:"sequence"`
	At        time.Time         `json:"at"`
	Kind      string            `json:"kind"`
	WaveID    string            `json:"wave_id,omitempty"`
	CellID    string            `json:"cell_id,omitempty"`
	AttemptID string            `json:"attempt_id,omitempty"`
	Detail    map[string]string `json:"detail,omitempty"`
}

type Snapshot struct {
	Study Study  `json:"study"`
	Cells []Cell `json:"cells"`
}
type SubmitRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Plan           Plan   `json:"plan"`
}
type Result struct {
	ResultDigest string     `json:"result_digest,omitempty"`
	FailureKind  string     `json:"failure_kind,omitempty"`
	Cost         float64    `json:"cost"`
	Attention    *Attention `json:"attention,omitempty"`
}

func (p Plan) Validate() error {
	if strings.TrimSpace(p.Revision) == "" || strings.TrimSpace(p.Digest) == "" {
		return fmt.Errorf("study plan requires immutable revision and digest")
	}
	if len(p.Waves) == 0 {
		return fmt.Errorf("study plan requires at least one wave")
	}
	if p.Budget.Limit < 0 {
		return fmt.Errorf("study budget limit must not be negative")
	}
	waves, cells := map[string]bool{}, map[string]bool{}
	last := -1
	for _, w := range p.Waves {
		if w.ID == "" || w.Order <= last {
			return fmt.Errorf("waves require unique ids in strictly increasing order")
		}
		last = w.Order
		if waves[w.ID] {
			return fmt.Errorf("duplicate wave %q", w.ID)
		}
		waves[w.ID] = true
		for _, c := range w.Cells {
			if c.ID == "" || cells[c.ID] {
				return fmt.Errorf("cells require unique non-empty ids")
			}
			cells[c.ID] = true
		}
	}
	for _, w := range p.Waves {
		for _, c := range w.Cells {
			for _, dep := range c.DependsOn {
				if !cells[dep] {
					return fmt.Errorf("cell %q depends on unknown cell %q", c.ID, dep)
				}
			}
		}
	}
	return nil
}

func terminal(p CellPhase) bool { return p == CellComplete || p == CellFailed || p == CellCancelled }
func newID() string             { return ulid.New() }
