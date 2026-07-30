// Package delivery provides the single management contract for durable code
// delivery. It deliberately owns no state: the Capsule merge queue remains the
// sole ledger and protected-ref authority.
package delivery

import (
	"context"
	"fmt"
	"strings"

	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
)

// Action is the next management action implied by durable queue state. It is a
// projection, not another state machine.
type Action string

const (
	ActionProcessing Action = "processing"
	ActionRepair     Action = "repair"
	ActionReview     Action = "review"
	ActionRetry      Action = "retry"
	ActionTerminal   Action = "terminal"
)

// Result is shared by the CLI, MCP, and Starlark host adapters.
type Result struct {
	Candidate *queue.Candidate            `json:"candidate,omitempty"`
	State     *queue.State                `json:"state,omitempty"`
	Anchor    *queue.ExternalBundleAnchor `json:"anchor,omitempty"`
	Action    Action                      `json:"next_action,omitempty"`
}

// SubmitRequest admits retained, verified bundle bytes through the queue's
// existing durable_bundle path. The service does not copy, validate, or retain
// bundle bytes itself.
type SubmitRequest struct {
	Result     queue.ExternalWorkerResult `json:"result"`
	BundlePath string                     `json:"bundle_path"`
	Submit     Submit                     `json:"submit"`
}

// Submit is the transport-stable admission subset. Admission is intentionally
// absent because this service always selects durable_bundle.
type Submit struct {
	Branch                   string                   `json:"branch"`
	SHA                      string                   `json:"sha"`
	TargetRef                string                   `json:"target_ref"`
	TargetBaseSHAAtAdmission string                   `json:"target_base_sha_at_admission"`
	TargetPolicy             queue.TargetPolicy       `json:"target_policy"`
	Receipt                  receipt.Receipt          `json:"receipt"`
	ReceiptRef               string                   `json:"receipt_ref,omitempty"`
	RunRecordRef             string                   `json:"run_record_ref,omitempty"`
	Backend                  string                   `json:"backend,omitempty"`
	Paths                    []string                 `json:"paths,omitempty"`
	FinalizationPolicy       queue.FinalizationPolicy `json:"finalization_policy,omitempty"`
	ManifestDigest           string                   `json:"manifest_digest,omitempty"`
	RuntimeInstance          string                   `json:"runtime_instance,omitempty"`
	RuntimeReceipt           string                   `json:"runtime_receipt,omitempty"`
	RequiredReceiptIDs       []string                 `json:"required_receipt_ids,omitempty"`
	AdmissionID              string                   `json:"admission_id,omitempty"`
	RequiredGateTier         string                   `json:"required_gate_tier,omitempty"`
}

type queueStore interface {
	AdmitExternalBundle(context.Context, queue.ExternalBundleSubmission) (queue.Candidate, queue.ExternalBundleAnchor, error)
	Get(string) (queue.Candidate, error)
	List() (queue.State, error)
	Kick(queue.Op) (queue.Candidate, error)
	Park(queue.Op) (queue.Candidate, error)
	Resume(queue.Op) (queue.Candidate, error)
	Reject(queue.Op) (queue.Candidate, error)
}

// Service is a stateless facade over the one durable queue ledger.
type Service struct {
	store queueStore
}

func New(store queue.Store) Service {
	return Service{store: store}
}

func (s Service) Submit(ctx context.Context, in SubmitRequest) (Result, error) {
	if s.store == nil {
		return Result{}, fmt.Errorf("delivery: queue store is unavailable")
	}
	submit := in.Submit.queueSubmit()
	candidate, anchor, err := s.store.AdmitExternalBundle(ctx, queue.ExternalBundleSubmission{
		Result: in.Result, BundlePath: in.BundlePath, Submit: submit,
	})
	if err != nil {
		return Result{}, err
	}
	return candidateResult(candidate, &anchor), nil
}

func (s Submit) queueSubmit() queue.Submit {
	return queue.Submit{
		Branch: s.Branch, SHA: s.SHA,
		TargetRef: s.TargetRef, TargetBaseSHAAtAdmission: s.TargetBaseSHAAtAdmission,
		TargetPolicy: s.TargetPolicy, Receipt: s.Receipt, ReceiptRef: s.ReceiptRef,
		RunRecordRef: s.RunRecordRef, Backend: s.Backend, Paths: append([]string(nil), s.Paths...),
		Admission: queue.DurableBundleAdmission, FinalizationPolicy: s.FinalizationPolicy,
		ManifestDigest: s.ManifestDigest, RuntimeInstance: s.RuntimeInstance,
		RuntimeReceipt: s.RuntimeReceipt, RequiredReceiptIDs: append([]string(nil), s.RequiredReceiptIDs...),
		AdmissionID: s.AdmissionID, RequiredGateTier: s.RequiredGateTier,
	}
}

func (s Service) Get(id string) (Result, error) {
	if s.store == nil {
		return Result{}, fmt.Errorf("delivery: queue store is unavailable")
	}
	candidate, err := s.store.Get(strings.TrimSpace(id))
	if err != nil {
		return Result{}, err
	}
	return candidateResult(candidate, nil), nil
}

func (s Service) Status() (Result, error) {
	if s.store == nil {
		return Result{}, fmt.Errorf("delivery: queue store is unavailable")
	}
	state, err := s.store.List()
	if err != nil {
		return Result{}, err
	}
	return Result{State: &state}, nil
}

// Retry normalizes the two recoverable queue states into one public verb.
func (s Service) Retry(op queue.Op) (Result, error) {
	current, err := s.Get(op.ID)
	if err != nil {
		return Result{}, err
	}
	var candidate queue.Candidate
	switch candidatePhase(*current.Candidate) {
	case queue.RetryWait:
		candidate, err = s.store.Kick(op)
	case queue.NeedsInput, queue.NeedsConflictInput:
		candidate, err = s.store.Resume(op)
	default:
		return Result{}, fmt.Errorf("delivery: retry requires retry_wait, needs_input, or needs_conflict_input; candidate %s is %s", op.ID, candidatePhase(*current.Candidate))
	}
	if err != nil {
		return Result{}, err
	}
	return candidateResult(candidate, nil), nil
}

// Cancel is recoverable: it parks work while retaining the branch, bundle,
// receipts, and evidence. Reject is the explicit terminal decision.
func (s Service) Cancel(op queue.Op) (Result, error) {
	candidate, err := s.store.Park(op)
	if err != nil {
		return Result{}, err
	}
	return candidateResult(candidate, nil), nil
}

func (s Service) Reject(op queue.Op) (Result, error) {
	candidate, err := s.store.Reject(op)
	if err != nil {
		return Result{}, err
	}
	return candidateResult(candidate, nil), nil
}

func candidateResult(candidate queue.Candidate, anchor *queue.ExternalBundleAnchor) Result {
	return Result{Candidate: &candidate, Anchor: anchor, Action: NextAction(candidate)}
}

// NextAction projects repair/review progression from existing queue facts.
// Repair and review execution is owned by the configured Story/workqueue jobs;
// this value never makes a queue transition by itself.
func NextAction(candidate queue.Candidate) Action {
	switch candidatePhase(candidate) {
	case queue.Landed, queue.Rejected:
		return ActionTerminal
	case queue.NeedsInput, queue.NeedsConflictInput:
		return ActionRepair
	case queue.AwaitingApproval:
		return ActionReview
	case queue.RetryWait:
		return ActionRetry
	default:
		return ActionProcessing
	}
}

func candidatePhase(candidate queue.Candidate) queue.Status {
	if candidate.Phase != "" {
		return candidate.Phase
	}
	return candidate.Status
}
