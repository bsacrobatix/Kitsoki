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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/atomicfile"
	"kitsoki/internal/capsule/receipt"
)

// ErrBusy indicates the state lock is held by a concurrent caller and could
// not be acquired within Store.LockWait. Callers that need a bounded,
// receipt-typed result (rather than a hang or an opaque error) should
// errors.Is against this to distinguish contention from other failures.
var ErrBusy = errors.New("queue: serializer busy")

const (
	Schema       = "capsule-merge-queue/v2"
	legacySchema = "capsule-merge-queue/v1"
)

// TargetPolicy records the authorization model for a protected destination.
// It is durable so workers cannot reinterpret the target from local flags.
type TargetPolicy string

const (
	WaveAutoPolicy        TargetPolicy = "wave-auto"
	StewardApprovedPolicy TargetPolicy = "steward-approved"
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
	AwaitingApproval   Status = "awaiting_approval"
	ReadyToFinalize    Status = "ready_to_finalize"
	Finalizing         Status = "finalizing"
	Reprepare          Status = "reprepare"
	NeedsConflictInput Status = "needs_conflict_input"
	NeedsInput         Status = "needs_input"
	RetryWait          Status = "retry_wait"
	Landed             Status = "landed"
	Rejected           Status = "rejected"

	// Running and Ejected retain source compatibility with v1 callers.
	Running Status = "running"
	Ejected Status = "ejected"
)

// FinalizationPolicy controls whether a green prepared candidate may advance
// directly to the protected CAS or requires an explicit steward approval.
// It is deliberately independent from target policy.
type FinalizationPolicy string

const (
	AutonomousFinalization    FinalizationPolicy = "autonomous"
	StewardReviewFinalization FinalizationPolicy = "steward_review"
)

type Candidate struct {
	ID                       string             `json:"id"`
	ProjectID                string             `json:"project_id"`
	TargetRef                string             `json:"target_ref"`
	TargetBaseSHAAtAdmission string             `json:"target_base_sha_at_admission,omitempty"`
	TargetPolicy             TargetPolicy       `json:"target_policy"`
	Sequence                 uint64             `json:"sequence"`
	Branch                   string             `json:"branch"`
	SHA                      string             `json:"sha"`
	Admission                Admission          `json:"admission"`
	ReceiptID                string             `json:"receipt_id"`
	ReceiptRef               string             `json:"receipt_ref,omitempty"`
	ReceiptDigest            string             `json:"receipt_digest,omitempty"`
	Backend                  string             `json:"backend"`
	Paths                    []string           `json:"paths,omitempty"`
	Position                 int                `json:"position"`
	Status                   Status             `json:"status"`
	Phase                    Status             `json:"phase,omitempty"`
	Submitted                time.Time          `json:"submitted_at"`
	Started                  time.Time          `json:"started_at,omitempty"`
	PhaseStartedAt           time.Time          `json:"phase_started_at,omitempty"`
	Completed                time.Time          `json:"completed_at,omitempty"`
	WorkerID                 string             `json:"worker_id,omitempty"`
	LeaseExpiresAt           time.Time          `json:"lease_expires_at,omitempty"`
	Attempt                  int                `json:"attempt,omitempty"`
	RetryAt                  time.Time          `json:"retry_at,omitempty"`
	BaseSHA                  string             `json:"base_sha,omitempty"`
	TreeSHA                  string             `json:"tree_sha,omitempty"`
	GateVersion              string             `json:"gate_version,omitempty"`
	DependencyFingerprint    string             `json:"dependency_fingerprint,omitempty"`
	RuntimeConfigDigest      string             `json:"runtime_config_digest,omitempty"`
	IntegrationRef           string             `json:"integration_ref,omitempty"`
	WorkspaceID              string             `json:"workspace_id,omitempty"`
	WorkspacePath            string             `json:"workspace_path,omitempty"`
	ConflictContinuation     string             `json:"conflict_continuation,omitempty"`
	GateLog                  string             `json:"gate_log,omitempty"`
	GateEvidence             []string           `json:"gate_evidence,omitempty"`
	FinalizationLog          string             `json:"finalization_log,omitempty"`
	ResultMainSHA            string             `json:"result_main_sha,omitempty"`
	Failure                  string             `json:"failure,omitempty"`
	SpeculativeSHA           string             `json:"speculative_sha,omitempty"` // v1 compatibility
	ValidatedSHA             string             `json:"validated_sha,omitempty"`   // v1 compatibility
	Evidence                 []string           `json:"evidence,omitempty"`
	RetryReason              string             `json:"retry_reason,omitempty"`
	EjectionReason           string             `json:"ejection_reason,omitempty"`
	EmergencySequence        uint64             `json:"emergency_sequence,omitempty"`
	ParkedAt                 time.Time          `json:"parked_at,omitempty"`
	ParkedBy                 string             `json:"parked_by,omitempty"`
	OverrideGate             bool               `json:"override_gate,omitempty"`
	OverrideBy               string             `json:"override_by,omitempty"`
	OverrideReason           string             `json:"override_reason,omitempty"`
	FinalizationPolicy       FinalizationPolicy `json:"finalization_policy,omitempty"`
	ManifestDigest           string             `json:"manifest_digest,omitempty"`
	RuntimeInstance          string             `json:"runtime_instance,omitempty"`
	RuntimeReceipt           string             `json:"runtime_receipt,omitempty"`
	RequiredReceiptIDs       []string           `json:"required_receipt_ids,omitempty"`
	SourceAnchorID           string             `json:"source_anchor_id,omitempty"`
	Approval                 *Approval          `json:"approval,omitempty"`
	EnvRetries               int                `json:"env_retries,omitempty"`
	FirstEnvFailureAt        time.Time          `json:"first_env_failure_at,omitempty"`
}

// Approval is the steward decision bound to the exact prepared state.
type Approval struct {
	Actor       string    `json:"actor"`
	Reason      string    `json:"reason"`
	At          time.Time `json:"at"`
	Fingerprint string    `json:"fingerprint"`
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
	Branch, SHA              string
	TargetRef                string
	TargetBaseSHAAtAdmission string
	TargetPolicy             TargetPolicy
	Receipt                  receipt.Receipt
	ReceiptRef, Backend      string
	Paths                    []string
	Admission                Admission
	FinalizationPolicy       FinalizationPolicy
	ManifestDigest           string
	RuntimeInstance          string
	RuntimeReceipt           string
	RequiredReceiptIDs       []string
	SourceAnchorID           string
	Now                      time.Time
}

// Integration materializes an immutable integration tree. Land remains for
// staging/local compatibility; protected targets use Finalizer instead.
type Integration interface {
	Speculate(context.Context, Candidate, []Candidate) (Speculation, error)
	Land(context.Context, Speculation) error
}
type Speculation struct {
	SHA                 string   `json:"sha"`
	BaseSHA             string   `json:"base_sha,omitempty"`
	RuntimeConfigDigest string   `json:"runtime_config_digest,omitempty"`
	IntegrationRef      string   `json:"integration_ref,omitempty"`
	Evidence            []string `json:"evidence,omitempty"`
	WorkspaceID         string   `json:"workspace_id,omitempty"`
	WorkspacePath       string   `json:"workspace_path,omitempty"`
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
	// TargetRef selects the only candidate partition this worker may mutate.
	// Empty retains supervisor compatibility for legacy in-process callers.
	TargetRef string
	// GateMemo, when set, skips a gate run whose exact (tree, GateVersion,
	// runtime-config digest) tuple already passed — see GateMemo's doc. Nil
	// disables memoization.
	GateMemo GateMemo

	// Retry policy. A red gate or failed speculation moves the candidate to
	// the back of the line in retry_wait with exponential backoff; once
	// MaxAttempts is exhausted the candidate parks as needs_input instead of
	// spinning. Zero values take the defaults below.
	RetryDelay    time.Duration // default 5m
	MaxRetryDelay time.Duration // default 30m
	MaxAttempts   int           // default 5

	// Environmental retry policy (queue.EnvError, see Environmental). A
	// short fixed backoff, bounded by wall-clock time elapsed since the
	// candidate's current environmental-failure streak began rather than by
	// an attempt count, so a transient fetch/lock/workspace-create failure
	// never burns the product-failure attempt budget above. Zero values
	// take the defaults below.
	EnvRetryDelay  time.Duration // default 30s
	MaxEnvDuration time.Duration // default 2h
}

const (
	DefaultRetryDelay     = 5 * time.Minute
	DefaultMaxRetryDelay  = 30 * time.Minute
	DefaultMaxAttempts    = 5
	DefaultEnvRetryDelay  = 30 * time.Second
	DefaultMaxEnvDuration = 2 * time.Hour
)

func (d ProcessDeps) retryDelay() time.Duration {
	return firstDuration(d.RetryDelay, DefaultRetryDelay)
}
func (d ProcessDeps) maxRetryDelay() time.Duration {
	return firstDuration(d.MaxRetryDelay, DefaultMaxRetryDelay)
}
func (d ProcessDeps) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return DefaultMaxAttempts
}
func (d ProcessDeps) envRetryDelay() time.Duration {
	return firstDuration(d.EnvRetryDelay, DefaultEnvRetryDelay)
}
func (d ProcessDeps) maxEnvDuration() time.Duration {
	return firstDuration(d.MaxEnvDuration, DefaultMaxEnvDuration)
}

// HarnessError marks a failure of the queue's own machinery (a resolver or
// gate harness that could not launch) rather than a red result from a harness
// that ran. Harness failures park the candidate immediately as needs_input:
// burning bounded retry attempts on a broken launch path only delays every
// candidate behind it in the FIFO.
type HarnessError struct{ Err error }

func (e HarnessError) Error() string { return "queue harness: " + e.Err.Error() }
func (e HarnessError) Unwrap() error { return e.Err }

// Harness wraps err so the worker classifies it as a harness failure.
func Harness(err error) error {
	if err == nil {
		return nil
	}
	return HarnessError{Err: err}
}

// EnvError marks a failure caused by transient infrastructure state — a
// target not yet fetched into a workspace, lock contention, a
// workspace-create race — rather than a genuinely red gate or a broken
// harness. An environmental failure gets a short fixed backoff and does not
// consume the bounded product-failure attempt budget; a wall-clock bound
// (not an attempt count) still eventually parks a candidate stuck in a
// persistently degraded environment, so this can never spin unbounded
// either. Adapters classify their own errors as environmental at the exact
// git/filesystem operation that failed; the worker never string-matches an
// error to guess its class.
type EnvError struct{ Err error }

func (e EnvError) Error() string { return "queue environment: " + e.Err.Error() }
func (e EnvError) Unwrap() error { return e.Err }

// Environmental wraps err so the worker classifies it as an environmental
// failure instead of a product failure.
func Environmental(err error) error {
	if err == nil {
		return nil
	}
	return EnvError{Err: err}
}

type Repairer interface {
	Repair(context.Context, Speculation, error) ([]string, error)
}

// Finalizer owns the final protected compare-and-swap. It is called only with
// a short durable finalization lease held by the worker.
// Finalizer lands a gated candidate onto the protected target. Invocation is
// at-least-once: a worker that outlives its lease (slow, not dead) may invoke
// Finalize concurrently with the worker that reclaimed the candidate, so
// implementations MUST be compare-and-swap idempotent against the target ref —
// the CAS loser reports Stale, never a second landing. The worker's lease
// fencing then discards the stale result; durable state converges to exactly
// one landing.
type Finalizer interface {
	Finalize(context.Context, Candidate) (FinalizeResult, error)
}
type FinalizeResult struct {
	OldMainSHA         string `json:"old_main_sha,omitempty"`
	NewMainSHA         string `json:"new_main_sha,omitempty"`
	Log                string `json:"log,omitempty"`
	Stale              bool   `json:"stale,omitempty"`
	PreservedWIPBranch string `json:"preserved_wip_branch,omitempty"`
}

type Store struct {
	ProjectRoot string
	LockWait    time.Duration
	// LegacyTargetRef is the required explicit binding used to migrate v1 state.
	LegacyTargetRef                string
	LegacyTargetBaseSHAAtAdmission string
	LegacyTargetPolicy             TargetPolicy
}

func (s Store) Submit(in Submit) (Candidate, error) {
	if err := validate(in); err != nil {
		return Candidate{}, err
	}
	if err := s.publishCandidateRef(in); err != nil {
		return Candidate{}, err
	}
	return s.mutate(func(state *State) (Candidate, error) {
		for _, c := range state.Candidates {
			if c.SHA == in.SHA && c.TargetRef == in.targetRef() && c.admission() == in.admission() && c.ReceiptID == in.Receipt.ReceiptID {
				if strings.TrimSpace(in.SourceAnchorID) != "" && c.SourceAnchorID != strings.TrimSpace(in.SourceAnchorID) {
					return Candidate{}, fmt.Errorf("queue: existing candidate source anchor does not match external submission")
				}
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
		identity := in.identity()
		c := Candidate{ID: candidateID(in.SHA, identity, in.targetRef()), ProjectID: projectID, TargetRef: in.targetRef(), TargetBaseSHAAtAdmission: strings.TrimSpace(in.TargetBaseSHAAtAdmission), TargetPolicy: in.targetPolicy(), Sequence: seq, Branch: in.Branch, SHA: in.SHA, Admission: admission, ReceiptID: receiptID, ReceiptRef: receiptRef, ReceiptDigest: receiptDigest, Backend: defaultBackend(in.Backend), Paths: cleanPaths(in.Paths), Position: int(seq), Status: Queued, Phase: Queued, Submitted: now, FinalizationPolicy: in.finalizationPolicy(), ManifestDigest: strings.TrimSpace(in.ManifestDigest), RuntimeInstance: strings.TrimSpace(in.RuntimeInstance), RuntimeReceipt: strings.TrimSpace(in.RuntimeReceipt), RequiredReceiptIDs: cleanStrings(in.RequiredReceiptIDs), SourceAnchorID: strings.TrimSpace(in.SourceAnchorID)}
		// A resubmission of the same SHA (fresh receipt) supersedes any active
		// prior candidate rather than racing it in the FIFO, and inherits its
		// durable attempt count so bounded retries cannot be reset by
		// resubmitting. (POG carried a retry-ledger sidecar for exactly this.)
		for i := range state.Candidates {
			prior := &state.Candidates[i]
			if prior.SHA != in.SHA || prior.TargetRef != c.TargetRef || terminal(prior.phase()) {
				continue
			}
			if prior.Attempt > c.Attempt {
				c.Attempt = prior.Attempt
			}
			if prior.EmergencySequence != 0 && c.EmergencySequence == 0 {
				c.EmergencySequence = prior.EmergencySequence
			}
			prior.Status, prior.Phase = Rejected, Rejected
			prior.WorkerID, prior.LeaseExpiresAt = "", time.Time{}
			prior.EjectionReason = "superseded_by_resubmission"
			prior.Evidence = append(prior.Evidence, fmt.Sprintf("queue:superseded-by=%s at %s", c.ID, now.Format(time.RFC3339)))
			c.Evidence = append(c.Evidence, fmt.Sprintf("queue:supersedes=%s attempts_inherited=%d", prior.ID, c.Attempt))
		}
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
func terminal(p Status) bool { return p == Landed || p == Rejected }

// parked candidates wait on explicit human input; they are skipped by workers
// and never block later candidates from preparing or finalizing.
func parked(p Status) bool { return p == NeedsInput || p == NeedsConflictInput }

// before reports whether a orders ahead of b for claiming and finalization:
// the emergency lane first (FIFO within itself), then durable Position.
func before(a, b Candidate) bool {
	if (a.EmergencySequence != 0) != (b.EmergencySequence != 0) {
		return a.EmergencySequence != 0
	}
	if a.EmergencySequence != 0 && a.EmergencySequence != b.EmergencySequence {
		return a.EmergencySequence < b.EmergencySequence
	}
	if a.Position != b.Position {
		return a.Position < b.Position
	}
	return a.Sequence < b.Sequence
}

func nextPosition(cs []Candidate) int {
	max := 0
	for _, c := range cs {
		if c.Position > max {
			max = c.Position
		}
	}
	return max + 1
}

func nextEmergencySequence(cs []Candidate) uint64 {
	var max uint64
	for _, c := range cs {
		if c.EmergencySequence > max {
			max = c.EmergencySequence
		}
	}
	return max + 1
}
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
	var out State
	_, err := s.withLock(func(path string) (State, error) {
		state, migrated, err := s.read(path)
		if err != nil {
			return State{}, err
		}
		if migrated {
			if err := write(path, state); err != nil {
				return State{}, err
			}
		}
		out = state
		return state, nil
	})
	return out, err
}
func (s Store) mutate(fn func(*State) (Candidate, error)) (Candidate, error) {
	var out Candidate
	_, err := s.withLock(func(path string) (State, error) {
		state, _, err := s.read(path)
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
	if strings.ContainsAny(in.targetRef(), "\n\r") {
		return fmt.Errorf("queue: target_ref is invalid")
	}
	if policy := in.targetPolicy(); policy != WaveAutoPolicy && policy != StewardApprovedPolicy {
		return fmt.Errorf("queue: unsupported target_policy %q", policy)
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
	switch in.finalizationPolicy() {
	case AutonomousFinalization, StewardReviewFinalization:
	default:
		return fmt.Errorf("queue: unsupported finalization policy %q", in.FinalizationPolicy)
	}
	if in.finalizationPolicy() == StewardReviewFinalization && strings.TrimSpace(in.ManifestDigest) == "" {
		return fmt.Errorf("queue: steward_review finalization requires a manifest digest")
	}
	return nil
}
func (s Store) read(path string) (State, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{Schema: Schema, Candidates: []Candidate{}}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, false, fmt.Errorf("queue: parse state: %w", err)
	}
	if state.Schema == legacySchema {
		if strings.TrimSpace(s.LegacyTargetRef) == "" {
			return State{}, false, fmt.Errorf("queue: legacy state requires explicit migration target_ref")
		}
		for i := range state.Candidates {
			c := &state.Candidates[i]
			c.TargetRef = strings.TrimSpace(s.LegacyTargetRef)
			c.TargetBaseSHAAtAdmission = strings.TrimSpace(s.LegacyTargetBaseSHAAtAdmission)
			c.TargetPolicy = s.legacyTargetPolicy()
			c.Evidence = append(c.Evidence, fmt.Sprintf("queue:migrated-v1 target_ref=%s target_policy=%s", c.TargetRef, c.TargetPolicy))
		}
		state.Schema = Schema
		return normalize(state), true, nil
	}
	if state.Schema != Schema {
		return State{}, false, fmt.Errorf("queue: unsupported state schema %q", state.Schema)
	}
	for _, c := range state.Candidates {
		if strings.TrimSpace(c.TargetRef) == "" || c.TargetPolicy == "" {
			return State{}, false, fmt.Errorf("queue: v2 candidate %s is missing durable target binding", c.ID)
		}
	}
	return normalize(state), false, nil
}
func normalize(state State) State {
	for i := range state.Candidates {
		c := &state.Candidates[i]
		if c.Sequence == 0 {
			c.Sequence = uint64(i + 1)
		}
		// Position is durable queue ordering: failures requeue to the back
		// without rewriting the immutable admission Sequence.
		if c.Position == 0 {
			c.Position = int(c.Sequence)
		}
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
		if c.FinalizationPolicy == "" {
			c.FinalizationPolicy = AutonomousFinalization
		}
		c.RequiredReceiptIDs = cleanStrings(c.RequiredReceiptIDs)
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

func (in Submit) finalizationPolicy() FinalizationPolicy {
	if in.FinalizationPolicy == "" {
		return AutonomousFinalization
	}
	return in.FinalizationPolicy
}

func (in Submit) targetRef() string {
	if strings.TrimSpace(in.TargetRef) == "" {
		return "main"
	}
	return strings.TrimSpace(in.TargetRef)
}

func (in Submit) targetPolicy() TargetPolicy {
	if in.TargetPolicy == "" {
		return WaveAutoPolicy
	}
	return in.TargetPolicy
}

func (s Store) legacyTargetPolicy() TargetPolicy {
	if s.LegacyTargetPolicy == "" {
		return WaveAutoPolicy
	}
	return s.LegacyTargetPolicy
}

func (c Candidate) finalizationPolicy() FinalizationPolicy {
	if c.FinalizationPolicy == "" {
		return AutonomousFinalization
	}
	return c.FinalizationPolicy
}

// identity is the receipt-or-admission identity candidateID hashes together
// with the SHA (and, since target-binding landed, the target ref). Factored
// out so Submit's pre-admission ref anchor and the durable candidate record
// compute the exact same ID.
func (in Submit) identity() string {
	receiptID := in.Receipt.ReceiptID
	if in.admission() == EmergencySkipTestsAdmission {
		receiptID = ""
	}
	if receiptID == "" {
		return string(in.admission())
	}
	return receiptID
}

// candidateRefName is the durable ref namespace that anchors a queued
// candidate's commit object against GC for the candidate's lifetime — the
// same namespace an operator previously had to publish into by hand
// (refs/kitsoki/queue-candidates/<candidate-id>) when a worker retried a SHA
// that was never made resolvable outside the workspace that produced it.
func candidateRefName(id string) string {
	return "refs/kitsoki/queue-candidates/" + id
}

// publishCandidateRef refuses admission of a candidate whose commit is not
// yet resolvable in the project, and otherwise anchors it under
// candidateRefName so a later worker attempt — possibly against a reused or
// stale-fetched workspace — can always find the object regardless of what
// happens to whatever produced it. The candidate object itself must already
// be reachable in s.ProjectRoot before Submit is called (e.g. `capsule
// promote` fetches its dev-workspace commit into the project root first);
// publishCandidateRef only anchors it, it does not transfer it.
//
// Skipped when s.ProjectRoot is not a git repository at all: several queue
// unit tests deliberately exercise pure state-machine semantics against a
// bare temp directory (see the package doc's "unit fakes" testing tier) and
// never intend to touch git. Any git-backed ProjectRoot — every real
// deployment, and the package's real-git end-to-end test tier — gets the
// check.
func (s Store) publishCandidateRef(in Submit) error {
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.SourceAnchorID) != "" {
		anchor, err := s.externalAnchor(context.Background(), strings.TrimSpace(in.SourceAnchorID))
		if err != nil {
			return err
		}
		if anchor.Result.CandidateSHA != in.SHA ||
			anchor.Result.Branch != in.Branch ||
			anchor.Result.TargetRef != in.targetRef() ||
			anchor.Result.ReceiptID != in.Receipt.ReceiptID ||
			anchor.Result.ManifestDigest != strings.TrimSpace(in.ManifestDigest) {
			return fmt.Errorf("queue: external bundle anchor does not match queue submission")
		}
		return nil
	}
	if _, err := gitOutput(context.Background(), root, "rev-parse", "--git-dir"); err != nil {
		return nil
	}
	if _, err := gitOutput(context.Background(), root, "cat-file", "-e", in.SHA+"^{commit}"); err != nil {
		return fmt.Errorf("queue: candidate %s is not a resolvable commit in %s; publish it into the project before submitting: %w", in.SHA, root, err)
	}
	// The ref name must be computed with the exact same inputs Submit's
	// mutate closure uses for the real candidate ID (including targetRef,
	// since target-binding landed) or this anchor points at a ref no
	// candidate actually has.
	ref := candidateRefName(candidateID(in.SHA, in.identity(), in.targetRef()))
	if _, err := gitOutput(context.Background(), root, "update-ref", ref, in.SHA); err != nil {
		return fmt.Errorf("queue: anchor candidate ref %s: %w", ref, err)
	}
	return nil
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
	return atomicfile.WriteFile(path, append(raw, '\n'), 0o600, 0o755)
}
func lock(path string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("queue: acquire serializer: %w", err)
		}
		acquired, err := tryExclusiveFileLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("queue: acquire serializer: %w", err)
		}
		if acquired {
			return func() {
				_ = releaseExclusiveFileLock(f)
				_ = f.Close()
			}, nil
		}
		_ = f.Close()
		if wait <= 0 || time.Now().After(deadline) {
			return nil, fmt.Errorf("queue: acquire serializer: %w", ErrBusy)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func candidateID(sha, receiptID string, target ...string) string {
	sum := sha256.Sum256([]byte(sha + "\n" + receiptID + "\n" + strings.Join(target, "\n")))
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

func cleanStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
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
	case AwaitingApproval:
		next = "steward approval"
	case Landed:
		next = "complete"
	}
	return fmt.Sprintf("%d %s %s owner=%s elapsed=%s base=%s tree=%s next=%s logs=%s", c.Sequence, c.ID, c.phase(), c.WorkerID, elapsed, c.BaseSHA, c.TreeSHA, next, first(c.GateLog, c.FinalizationLog, c.WorkspacePath))
}
