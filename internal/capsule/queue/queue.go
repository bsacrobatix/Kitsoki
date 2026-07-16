// Package queue persists receipt-bound merge candidates and coordinates their
// preparation and protected finalization. Expensive preparation never holds
// the state lock; protected ref updates remain injected and compare-and-swap
// based.
package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/capsule/receipt"
)

const (
	Schema = "capsule-merge-queue/v1"
)

// Admission records how a candidate was authorized to enter the queue.
// ReceiptAdmission is the normal path. EmergencySkipTestsAdmission is an
// explicit operator override used only by `capsule promote --skip-tests`; it
// remains visible in the durable queue record and never masquerades as CI.
type Admission string

const (
	ReceiptAdmission            Admission = "receipt"
	EmergencySkipTestsAdmission Admission = "emergency_skip_tests"
)

type Status string

const (
	Queued             Status = "queued"
	Preparing          Status = "preparing"
	WaitingForFIFO     Status = "waiting_for_fifo"
	Gating             Status = "gating"
	ReadyToFinalize    Status = "ready_to_finalize"
	Finalizing         Status = "finalizing"
	Reprepare          Status = "reprepare"
	NeedsConflictInput Status = "needs_conflict_input"
	RetryWait          Status = "retry_wait"
	Landed             Status = "landed"
	Rejected           Status = "rejected"

	// Running and Ejected retain source compatibility with v1 callers.
	Running Status = "running"
	Ejected Status = "ejected"
)

type Candidate struct {
	ID                    string    `json:"id"`
	ProjectID             string    `json:"project_id"`
	Sequence              uint64    `json:"sequence"`
	Branch                string    `json:"branch"`
	SHA                   string    `json:"sha"`
	Admission             Admission `json:"admission"`
	ReceiptID             string    `json:"receipt_id"`
	ReceiptRef            string    `json:"receipt_ref,omitempty"`
	ReceiptDigest         string    `json:"receipt_digest,omitempty"`
	Backend               string    `json:"backend"`
	Paths                 []string  `json:"paths,omitempty"`
	Position              int       `json:"position"`
	Status                Status    `json:"status"`
	Phase                 Status    `json:"phase,omitempty"`
	Submitted             time.Time `json:"submitted_at"`
	Started               time.Time `json:"started_at,omitempty"`
	PhaseStartedAt        time.Time `json:"phase_started_at,omitempty"`
	Completed             time.Time `json:"completed_at,omitempty"`
	WorkerID              string    `json:"worker_id,omitempty"`
	LeaseExpiresAt        time.Time `json:"lease_expires_at,omitempty"`
	Attempt               int       `json:"attempt,omitempty"`
	RetryAt               time.Time `json:"retry_at,omitempty"`
	BaseSHA               string    `json:"base_sha,omitempty"`
	TreeSHA               string    `json:"tree_sha,omitempty"`
	GateVersion           string    `json:"gate_version,omitempty"`
	DependencyFingerprint string    `json:"dependency_fingerprint,omitempty"`
	IntegrationRef        string    `json:"integration_ref,omitempty"`
	WorkspaceID           string    `json:"workspace_id,omitempty"`
	WorkspacePath         string    `json:"workspace_path,omitempty"`
	ConflictContinuation  string    `json:"conflict_continuation,omitempty"`
	GateLog               string    `json:"gate_log,omitempty"`
	GateEvidence          []string  `json:"gate_evidence,omitempty"`
	FinalizationLog       string    `json:"finalization_log,omitempty"`
	ResultMainSHA         string    `json:"result_main_sha,omitempty"`
	Failure               string    `json:"failure,omitempty"`
	SpeculativeSHA        string    `json:"speculative_sha,omitempty"` // v1 compatibility
	ValidatedSHA          string    `json:"validated_sha,omitempty"`   // v1 compatibility
	Evidence              []string  `json:"evidence,omitempty"`
	RetryReason           string    `json:"retry_reason,omitempty"`
	EjectionReason        string    `json:"ejection_reason,omitempty"`
}

type State struct {
	Schema     string      `json:"schema"`
	Candidates []Candidate `json:"candidates"`
}

type PathScopeManifest struct {
	Schema string   `json:"schema"`
	Paths  []string `json:"paths,omitempty"`
}
type Submit struct {
	Branch, SHA         string
	Receipt             receipt.Receipt
	ReceiptRef, Backend string
	Paths               []string
	Admission           Admission
	Now                 time.Time
}

// Integration materializes an immutable integration tree. Land remains for
// staging/local compatibility; protected targets use Finalizer instead.
type Integration interface {
	Speculate(context.Context, Candidate, []Candidate) (Speculation, error)
	Land(context.Context, Speculation) error
}
type Speculation struct {
	SHA            string   `json:"sha"`
	BaseSHA        string   `json:"base_sha,omitempty"`
	IntegrationRef string   `json:"integration_ref,omitempty"`
	Evidence       []string `json:"evidence,omitempty"`
	WorkspaceID    string   `json:"workspace_id,omitempty"`
	WorkspacePath  string   `json:"workspace_path,omitempty"`
}
type Gate interface {
	Run(context.Context, Speculation) (GateResult, error)
}
type GateResult struct {
	Passed                bool     `json:"passed"`
	Evidence              []string `json:"evidence,omitempty"`
	Log                   string   `json:"log,omitempty"`
	GateVersion           string   `json:"gate_version,omitempty"`
	DependencyFingerprint string   `json:"dependency_fingerprint,omitempty"`
}
type ProcessDeps struct {
	Integration           Integration
	Gate                  Gate
	Repairer              Repairer
	Finalizer             Finalizer
	Now                   func() time.Time
	WorkerID              string
	Lease                 time.Duration
	GateVersion           string
	DependencyFingerprint string
}

type Repairer interface {
	Repair(context.Context, Speculation, error) ([]string, error)
}

// Finalizer owns the final protected compare-and-swap. It is called only with
// a short durable finalization lease held by the worker.
type Finalizer interface {
	Finalize(context.Context, Candidate) (FinalizeResult, error)
}
type FinalizeResult struct {
	OldMainSHA string `json:"old_main_sha,omitempty"`
	NewMainSHA string `json:"new_main_sha,omitempty"`
	Log        string `json:"log,omitempty"`
	Stale      bool   `json:"stale,omitempty"`
}

type Store struct {
	ProjectRoot string
	LockWait    time.Duration
}

func (s Store) Submit(in Submit) (Candidate, error) {
	if err := validate(in); err != nil {
		return Candidate{}, err
	}
	return s.mutate(func(state *State) (Candidate, error) {
		for _, c := range state.Candidates {
			if c.SHA == in.SHA && c.admission() == in.admission() && c.ReceiptID == in.Receipt.ReceiptID {
				return c, nil
			}
		}
		now := in.Now.UTC()
		if now.IsZero() {
			now = time.Now().UTC()
		}
		seq := nextSequence(state.Candidates)
		admission := in.admission()
		receiptID, receiptRef, receiptDigest, projectID := in.Receipt.ReceiptID, in.ReceiptRef, in.Receipt.Integrity.ContentDigest, in.Receipt.ProjectID
		if admission == EmergencySkipTestsAdmission {
			receiptID, receiptRef, receiptDigest = "", "", string(admission)
			projectID = filepath.Base(mustAbs(s.ProjectRoot))
		}
		identity := receiptID
		if identity == "" {
			identity = string(admission)
		}
		c := Candidate{ID: candidateID(in.SHA, identity), ProjectID: projectID, Sequence: seq, Branch: in.Branch, SHA: in.SHA, Admission: admission, ReceiptID: receiptID, ReceiptRef: receiptRef, ReceiptDigest: receiptDigest, Backend: defaultBackend(in.Backend), Paths: cleanPaths(in.Paths), Position: int(seq), Status: Queued, Phase: Queued, Submitted: now}
		state.Candidates = append(state.Candidates, c)
		return c, nil
	})
}

func (s Store) List() (State, error) { return s.readState() }

// Process is retained for existing callers. It drains available work through a
// worker; each gate still runs after the short state claim has been released.
func (s Store) Process(ctx context.Context, deps ProcessDeps) (State, error) {
	w := Worker{Store: s, Deps: deps}
	for {
		progressed, err := w.RunOnce(ctx)
		if err != nil {
			return State{}, err
		}
		if !progressed {
			return s.List()
		}
	}
}

func nextSequence(cs []Candidate) uint64 {
	var max uint64
	for _, c := range cs {
		if c.Sequence > max {
			max = c.Sequence
		}
	}
	return max + 1
}
func activeAhead(cs []Candidate) []Candidate {
	out := make([]Candidate, 0, len(cs))
	for _, c := range cs {
		if !terminal(c.phase()) {
			out = append(out, c)
		}
	}
	return out
}
func terminal(p Status) bool { return p == Landed || p == Rejected }
func (c Candidate) phase() Status {
	if c.Phase != "" {
		return c.Phase
	}
	return c.Status
}
func now(deps ProcessDeps) time.Time {
	if deps.Now != nil {
		return deps.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Store) readState() (State, error) {
	_, path, err := s.paths()
	if err != nil {
		return State{}, err
	}
	return read(path)
}
func (s Store) mutate(fn func(*State) (Candidate, error)) (Candidate, error) {
	var out Candidate
	_, err := s.withLock(func(path string) (State, error) {
		state, err := read(path)
		if err != nil {
			return State{}, err
		}
		out, err = fn(&state)
		if err != nil {
			return State{}, err
		}
		return state, write(path, state)
	})
	return out, err
}
func (s Store) withLock(fn func(string) (State, error)) (State, error) {
	dir, path, err := s.paths()
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return State{}, err
	}
	unlock, err := lock(filepath.Join(dir, "state.lock"), s.LockWait)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	return fn(path)
}
func (s Store) paths() (string, string, error) {
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(root, ".capsules", "queue")
	return dir, filepath.Join(dir, "state.json"), nil
}

func validate(in Submit) error {
	if strings.TrimSpace(in.Branch) == "" || strings.ContainsAny(in.Branch, "\n\r") {
		return fmt.Errorf("queue: branch is required")
	}
	if len(in.SHA) != 40 || strings.Trim(in.SHA, "0123456789abcdef") != "" {
		return fmt.Errorf("queue: candidate SHA must be a lowercase full git SHA")
	}
	switch in.admission() {
	case ReceiptAdmission:
		if got := receipt.Verify(in.Receipt, nil, false); got.Status != "valid" || !got.PromotionEligible {
			return fmt.Errorf("queue: receipt is not a valid promotion-eligible capsule CI receipt")
		}
		if in.Receipt.Envelope.SourceDigest != in.SHA {
			return fmt.Errorf("queue: receipt candidate SHA does not match submission")
		}
	case EmergencySkipTestsAdmission:
		if in.Receipt.ReceiptID != "" {
			return fmt.Errorf("queue: emergency skip-tests admission cannot carry a Capsule CI receipt")
		}
	default:
		return fmt.Errorf("queue: unsupported candidate admission %q", in.Admission)
	}
	return nil
}
func read(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{Schema: Schema, Candidates: []Candidate{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("queue: parse state: %w", err)
	}
	if state.Schema != Schema {
		return State{}, fmt.Errorf("queue: unsupported state schema %q", state.Schema)
	}
	return normalize(state), nil
}
func normalize(state State) State {
	for i := range state.Candidates {
		c := &state.Candidates[i]
		if c.Sequence == 0 {
			c.Sequence = uint64(i + 1)
		}
		c.Position = int(c.Sequence)
		if c.Phase == "" {
			c.Phase = c.Status
		}
		if c.Phase == Running {
			c.Phase = Reprepare
		}
		if c.Status == Ejected {
			c.Status, c.Phase = Rejected, Rejected
		}
		if c.Phase == "" {
			c.Phase = Queued
		}
		if c.Status == "" {
			c.Status = c.Phase
		}
		if c.ReceiptDigest == "" {
			c.ReceiptDigest = c.ReceiptID
		}
		if c.Admission == "" {
			c.Admission = ReceiptAdmission
		}
	}
	state.Schema = Schema
	sort.SliceStable(state.Candidates, func(i, j int) bool { return state.Candidates[i].Sequence < state.Candidates[j].Sequence })
	return state
}

func (in Submit) admission() Admission {
	if in.Admission == "" {
		return ReceiptAdmission
	}
	return in.Admission
}

func (c Candidate) admission() Admission {
	if c.Admission == "" {
		return ReceiptAdmission
	}
	return c.Admission
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
func write(path string, state State) error {
	state = normalize(state)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func lock(path string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return func() { _ = f.Close(); _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("queue: acquire serializer: %w", err)
		}
		if wait <= 0 || time.Now().After(deadline) {
			return nil, fmt.Errorf("queue: acquire serializer: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func candidateID(sha, receiptID string) string {
	sum := sha256.Sum256([]byte(sha + "\n" + receiptID))
	return "queue-" + hex.EncodeToString(sum[:])[:12]
}
func defaultBackend(v string) string {
	if strings.TrimSpace(v) == "" {
		return "local"
	}
	return v
}
func cleanPaths(paths []string) []string {
	out := append([]string(nil), paths...)
	sort.Strings(out)
	return out
}

// StatusLine is the stable, single-line human representation used by the CLI.
func StatusLine(c Candidate, at time.Time) string {
	elapsed := at.Sub(c.Submitted).Round(time.Second)
	if c.Submitted.IsZero() {
		elapsed = 0
	}
	next := "worker"
	switch c.phase() {
	case NeedsConflictInput:
		next = "conflict input"
	case RetryWait:
		next = "retry"
	case ReadyToFinalize:
		next = "finalize"
	case Landed:
		next = "complete"
	}
	return fmt.Sprintf("%d %s %s owner=%s elapsed=%s base=%s tree=%s next=%s logs=%s", c.Sequence, c.ID, c.phase(), c.WorkerID, elapsed, c.BaseSHA, c.TreeSHA, next, first(c.GateLog, c.FinalizationLog, c.WorkspacePath))
}
