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
	"kitsoki/internal/capsule/reconcile"
	"kitsoki/internal/capsule/record"
)

const (
	PromoteExistingSchema           = "capsule-promote-existing/v1"
	PromoteExistingStatusObserved   = "observed"
	PromoteExistingStatusCertified  = "certified"
	PromoteExistingStatusQueued     = "queued"
	PromoteExistingStatusNeedsInput = "needs_input"
)

// PromoteExistingRequest identifies one immutable cross-target promotion.
// GateCommand is recorded as part of the durable authority evidence, but this
// primitive deliberately never runs the gate or processes the destination
// queue. The target-partitioned queue worker remains the sole protected-ref
// finalizer.
type PromoteExistingRequest struct {
	SourceTarget      string `json:"source_target"`
	LandedSHA         string `json:"landed_sha"`
	DestinationTarget string `json:"destination_target"`
	Pipeline          string `json:"pipeline"`
	GateCommand       string `json:"gate_command"`
}

type ExistingSHACertification struct {
	Key                    string
	Request                PromoteExistingRequest
	SourceCandidate        Candidate
	SourceLandingReceiptID string
	JobID                  string
}

type ExistingSHACertifier interface {
	Certify(context.Context, ExistingSHACertification) (record.Stored, error)
}

type ExistingSHACertifierFunc func(context.Context, ExistingSHACertification) (record.Stored, error)

func (f ExistingSHACertifierFunc) Certify(ctx context.Context, in ExistingSHACertification) (record.Stored, error) {
	return f(ctx, in)
}

// PromoteExistingRecord is the restart authority for one exact tuple. It is
// written before certification starts, after certification, and after queue
// admission so a controller restart can resume without inventing a fresh
// receipt or candidate identity.
type PromoteExistingRecord struct {
	Schema                       string                 `json:"schema"`
	Key                          string                 `json:"key"`
	Status                       string                 `json:"status"`
	FailureClass                 string                 `json:"failure_class,omitempty"`
	Failure                      string                 `json:"failure,omitempty"`
	Request                      PromoteExistingRequest `json:"request"`
	SourceCandidateID            string                 `json:"source_candidate_id"`
	SourceLandingReceiptID       string                 `json:"source_landing_receipt_id"`
	SourceTargetSHAAtObservation string                 `json:"source_target_sha_at_observation"`
	DestinationSHAAtObservation  string                 `json:"destination_sha_at_observation"`
	CIJobID                      string                 `json:"ci_job_id"`
	CIReceiptID                  string                 `json:"ci_receipt_id,omitempty"`
	CIReceiptRef                 string                 `json:"ci_receipt_ref,omitempty"`
	DestinationCandidateID       string                 `json:"destination_candidate_id,omitempty"`
	Evidence                     []string               `json:"evidence,omitempty"`
	CreatedAt                    time.Time              `json:"created_at"`
	UpdatedAt                    time.Time              `json:"updated_at"`
}

type PromoteExistingResult struct {
	Schema    string                `json:"schema"`
	Status    string                `json:"status"`
	Record    PromoteExistingRecord `json:"record"`
	Candidate Candidate             `json:"candidate,omitempty"`
}

type PromoteExistingAuthority struct {
	ProjectRoot string
	// QueueRoot selects the one durable queue authority shared by admission,
	// promotion records, locks, and destination processing. Empty preserves
	// the project-local <project>/.capsules/queue default.
	QueueRoot string
	Certifier ExistingSHACertifier
	Now       func() time.Time
	LockWait  time.Duration
}

// PromoteExisting admits an exact, receipt-proven source-target landing to a
// destination queue. It never offers a waiver, runs a protected CAS, or
// substitutes a mutable workspace head for LandedSHA.
func (a PromoteExistingAuthority) Promote(ctx context.Context, request PromoteExistingRequest) (PromoteExistingResult, error) {
	root, err := filepath.Abs(a.ProjectRoot)
	if err != nil {
		return PromoteExistingResult{}, err
	}
	request = normalizePromoteExistingRequest(request)
	if err := validatePromoteExistingRequest(request); err != nil {
		return PromoteExistingResult{}, err
	}
	if err := ValidateGateCommand(root, request.DestinationTarget, request.GateCommand); err != nil {
		return PromoteExistingResult{}, fmt.Errorf("queue promote-existing: destination gate policy: %w", err)
	}
	if a.Certifier == nil {
		return PromoteExistingResult{}, fmt.Errorf("queue promote-existing: certifier is required")
	}
	a.ProjectRoot = root
	queueRoot, err := (Store{ProjectRoot: root, QueueRoot: a.QueueRoot}).queueRoot()
	if err != nil {
		return PromoteExistingResult{}, err
	}
	a.QueueRoot = queueRoot
	key := promoteExistingKey(request)
	unlock, err := a.lock(key)
	if err != nil {
		return PromoteExistingResult{}, err
	}
	defer unlock()

	currentSource, err := gitRefSHA(ctx, root, request.SourceTarget)
	if err != nil {
		return a.reject(key, request, PromoteExistingRecord{}, "source_target_unavailable", err.Error())
	}
	currentDestination, err := gitRefSHA(ctx, root, request.DestinationTarget)
	if err != nil {
		return a.reject(key, request, PromoteExistingRecord{}, "destination_target_unavailable", err.Error())
	}

	existing, found, err := a.read(key)
	if err != nil {
		return PromoteExistingResult{}, err
	}
	if found {
		if existing.Request != request {
			return a.reject(key, request, existing, "request_mismatch", "durable promotion request does not match retry")
		}
		if existing.Status == PromoteExistingStatusNeedsInput {
			return PromoteExistingResult{Schema: PromoteExistingSchema, Status: existing.Status, Record: existing}, nil
		}
		if currentSource != existing.SourceTargetSHAAtObservation || currentSource != request.LandedSHA {
			return a.reject(key, request, existing, "target_moved", fmt.Sprintf("source target %s moved from %s to %s", request.SourceTarget, existing.SourceTargetSHAAtObservation, currentSource))
		}
		if existing.DestinationCandidateID != "" {
			candidate, err := a.store().Get(existing.DestinationCandidateID)
			if err != nil {
				return a.reject(key, request, existing, "candidate_missing", err.Error())
			}
			if candidate.SHA != request.LandedSHA || candidate.TargetRef != request.DestinationTarget || candidate.ReceiptID != existing.CIReceiptID {
				return a.reject(key, request, existing, "candidate_mismatch", "durable destination candidate does not match promotion authority")
			}
			// A controller restart after the destination CAS observes the
			// destination at ResultMainSHA. That is successful replay, not
			// target movement; retain the one candidate/receipt identity.
			if candidate.phase() == Landed && candidate.ResultMainSHA == currentDestination {
				return PromoteExistingResult{Schema: PromoteExistingSchema, Status: PromoteExistingStatusQueued, Record: existing, Candidate: candidate}, nil
			}
			if currentDestination != existing.DestinationSHAAtObservation {
				return a.reject(key, request, existing, "target_moved", fmt.Sprintf("destination target %s moved from %s to %s", request.DestinationTarget, existing.DestinationSHAAtObservation, currentDestination))
			}
			return PromoteExistingResult{Schema: PromoteExistingSchema, Status: PromoteExistingStatusQueued, Record: existing, Candidate: candidate}, nil
		}
		if currentDestination != existing.DestinationSHAAtObservation {
			return a.reject(key, request, existing, "target_moved", fmt.Sprintf("destination target %s moved from %s to %s", request.DestinationTarget, existing.DestinationSHAAtObservation, currentDestination))
		}
	}

	sourceCandidate, err := a.sourceLanding(ctx, request)
	if err != nil {
		return a.reject(key, request, existing, "source_landing_unproven", err.Error())
	}
	if found && (existing.SourceCandidateID != sourceCandidate.ID || existing.SourceLandingReceiptID != sourceCandidate.ReceiptID) {
		return a.reject(key, request, existing, "receipt_mismatch", "source landing candidate or receipt changed after observation")
	}
	if !found {
		now := a.now()
		existing = PromoteExistingRecord{
			Schema:                       PromoteExistingSchema,
			Key:                          key,
			Status:                       PromoteExistingStatusObserved,
			Request:                      request,
			SourceCandidateID:            sourceCandidate.ID,
			SourceLandingReceiptID:       sourceCandidate.ReceiptID,
			SourceTargetSHAAtObservation: currentSource,
			DestinationSHAAtObservation:  currentDestination,
			CIJobID:                      "promote-existing-" + key[len("sha256:"):len("sha256:")+24],
			Evidence: []string{
				fmt.Sprintf("source:%s landed=%s candidate=%s receipt=%s", request.SourceTarget, request.LandedSHA, sourceCandidate.ID, sourceCandidate.ReceiptID),
				fmt.Sprintf("destination:%s observed=%s", request.DestinationTarget, currentDestination),
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := a.write(existing); err != nil {
			return PromoteExistingResult{}, err
		}
	}

	var stored record.Stored
	if existing.CIReceiptID == "" {
		stored, err = a.Certifier.Certify(ctx, ExistingSHACertification{
			Key: key, Request: request, SourceCandidate: sourceCandidate,
			SourceLandingReceiptID: sourceCandidate.ReceiptID, JobID: existing.CIJobID,
		})
		if err != nil {
			return PromoteExistingResult{}, err
		}
		if err := validateExistingCertification(stored, request.LandedSHA); err != nil {
			return a.reject(key, request, existing, "certification_mismatch", err.Error())
		}
		existing.Status = PromoteExistingStatusCertified
		existing.CIReceiptID = stored.Receipt.ReceiptID
		existing.CIReceiptRef = stored.ReceiptPath
		existing.Evidence = append(existing.Evidence, fmt.Sprintf("ci:job=%s receipt=%s source=%s", existing.CIJobID, existing.CIReceiptID, request.LandedSHA))
		existing.UpdatedAt = a.now()
		if err := a.write(existing); err != nil {
			return PromoteExistingResult{}, err
		}
	} else {
		stored, err = existingStored(root, existing)
		if err != nil {
			return a.reject(key, request, existing, "certification_missing", err.Error())
		}
	}

	afterSource, err := gitRefSHA(ctx, root, request.SourceTarget)
	if err != nil || afterSource != existing.SourceTargetSHAAtObservation {
		return a.reject(key, request, existing, "target_moved", fmt.Sprintf("source target %s moved during certification from %s to %s", request.SourceTarget, existing.SourceTargetSHAAtObservation, afterSource))
	}
	afterDestination, err := gitRefSHA(ctx, root, request.DestinationTarget)
	if err != nil || afterDestination != existing.DestinationSHAAtObservation {
		return a.reject(key, request, existing, "target_moved", fmt.Sprintf("destination target %s moved during certification from %s to %s", request.DestinationTarget, existing.DestinationSHAAtObservation, afterDestination))
	}

	candidate, err := a.store().Submit(Submit{
		Branch:                   promoteExistingBranch(request),
		SHA:                      request.LandedSHA,
		TargetRef:                request.DestinationTarget,
		TargetBaseSHAAtAdmission: existing.DestinationSHAAtObservation,
		Receipt:                  stored.Receipt,
		ReceiptRef:               stored.ReceiptPath,
		Backend:                  "local",
		RequiredReceiptIDs:       []string{stored.Receipt.ReceiptID, sourceCandidate.ReceiptID},
	})
	if err != nil {
		return PromoteExistingResult{}, err
	}
	existing.Status = PromoteExistingStatusQueued
	existing.DestinationCandidateID = candidate.ID
	existing.Evidence = append(existing.Evidence, fmt.Sprintf("queue:target=%s candidate=%s sha=%s required_receipts=%s", request.DestinationTarget, candidate.ID, candidate.SHA, strings.Join(candidate.RequiredReceiptIDs, ",")))
	existing.UpdatedAt = a.now()
	if err := a.write(existing); err != nil {
		return PromoteExistingResult{}, err
	}
	return PromoteExistingResult{Schema: PromoteExistingSchema, Status: PromoteExistingStatusQueued, Record: existing, Candidate: candidate}, nil
}

func (a PromoteExistingAuthority) sourceLanding(ctx context.Context, request PromoteExistingRequest) (Candidate, error) {
	state, err := a.store().List()
	if err != nil {
		return Candidate{}, err
	}
	matches := make([]Candidate, 0, 1)
	for _, candidate := range state.Candidates {
		if candidate.TargetRef == request.SourceTarget && candidate.phase() == Landed && candidate.ResultMainSHA == request.LandedSHA && candidate.ReceiptID != "" {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return Candidate{}, fmt.Errorf("no receipt-bearing landed queue result for %s at %s", request.SourceTarget, request.LandedSHA)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Sequence > matches[j].Sequence })
	candidate := matches[0]
	if err := (record.PromotionGate{ProjectRoot: a.ProjectRoot}).Verify(ctx, candidate.ReceiptID, reconcile.Plan{Candidate: candidate.SHA}); err != nil {
		return Candidate{}, fmt.Errorf("verify source landing receipt %s: %w", candidate.ReceiptID, err)
	}
	return candidate, nil
}

func validateExistingCertification(stored record.Stored, sha string) error {
	verified := receipt.Verify(stored.Receipt, nil, false)
	if verified.Status != "valid" || !verified.PromotionEligible {
		return fmt.Errorf("new CI receipt is not valid and promotion eligible")
	}
	if stored.Receipt.Envelope.SourceDigest != sha {
		return fmt.Errorf("new CI receipt source %s does not match landed SHA %s", stored.Receipt.Envelope.SourceDigest, sha)
	}
	if strings.TrimSpace(stored.ReceiptPath) == "" {
		return fmt.Errorf("new CI receipt path is missing")
	}
	return nil
}

func existingStored(root string, existing PromoteExistingRecord) (record.Stored, error) {
	raw, err := os.ReadFile(existing.CIReceiptRef)
	if err != nil {
		return record.Stored{}, err
	}
	var r receipt.Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return record.Stored{}, err
	}
	stored := record.Stored{Receipt: r, ReceiptPath: existing.CIReceiptRef}
	if r.ReceiptID != existing.CIReceiptID {
		return record.Stored{}, fmt.Errorf("persisted CI receipt identity changed")
	}
	if err := validateExistingCertification(stored, existing.Request.LandedSHA); err != nil {
		return record.Stored{}, err
	}
	if err := (record.PromotionGate{ProjectRoot: root}).Verify(context.Background(), r.ReceiptID, reconcile.Plan{Candidate: existing.Request.LandedSHA}); err != nil {
		return record.Stored{}, err
	}
	return stored, nil
}

func (a PromoteExistingAuthority) reject(key string, request PromoteExistingRequest, existing PromoteExistingRecord, class, message string) (PromoteExistingResult, error) {
	if existing.Schema == "" {
		now := a.now()
		existing = PromoteExistingRecord{
			Schema: PromoteExistingSchema, Key: key, Request: request,
			CIJobID:   "promote-existing-" + key[len("sha256:"):len("sha256:")+24],
			CreatedAt: now,
		}
	}
	existing.Status = PromoteExistingStatusNeedsInput
	existing.FailureClass = class
	existing.Failure = message
	existing.Evidence = append(existing.Evidence, fmt.Sprintf("needs_input:%s %s", class, message))
	existing.UpdatedAt = a.now()
	if err := a.write(existing); err != nil {
		return PromoteExistingResult{}, err
	}
	return PromoteExistingResult{Schema: PromoteExistingSchema, Status: PromoteExistingStatusNeedsInput, Record: existing}, nil
}

func (a PromoteExistingAuthority) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizePromoteExistingRequest(in PromoteExistingRequest) PromoteExistingRequest {
	in.SourceTarget = strings.TrimSpace(in.SourceTarget)
	in.LandedSHA = strings.TrimSpace(in.LandedSHA)
	in.DestinationTarget = strings.TrimSpace(in.DestinationTarget)
	in.Pipeline = strings.TrimSpace(in.Pipeline)
	in.GateCommand = strings.TrimSpace(in.GateCommand)
	return in
}

func validatePromoteExistingRequest(in PromoteExistingRequest) error {
	for name, value := range map[string]string{
		"source target": in.SourceTarget, "destination target": in.DestinationTarget,
		"pipeline": in.Pipeline, "gate command": in.GateCommand,
	} {
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("queue promote-existing: %s is required and must be one line", name)
		}
	}
	if in.SourceTarget == in.DestinationTarget {
		return fmt.Errorf("queue promote-existing: source and destination targets must differ")
	}
	if len(in.LandedSHA) != 40 || strings.Trim(in.LandedSHA, "0123456789abcdef") != "" {
		return fmt.Errorf("queue promote-existing: landed SHA must be a lowercase full git SHA")
	}
	return nil
}

func promoteExistingKey(in PromoteExistingRequest) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{in.SourceTarget, in.LandedSHA, in.DestinationTarget, in.Pipeline, in.GateCommand}, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func promoteExistingBranch(in PromoteExistingRequest) string {
	target := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(in.SourceTarget)
	return "promote-existing/" + target + "/" + in.LandedSHA[:12]
}

func gitRefSHA(ctx context.Context, root, ref string) (string, error) {
	value, err := gitOutput(ctx, root, "rev-parse", "--verify", "refs/heads/"+ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve target %s: %w", ref, err)
	}
	return strings.TrimSpace(value), nil
}

func (a PromoteExistingAuthority) statePath(key string) string {
	return filepath.Join(a.queueRoot(), "promote-existing", strings.TrimPrefix(key, "sha256:")+".json")
}

func (a PromoteExistingAuthority) queueRoot() string {
	root, err := a.storeUnnormalized().queueRoot()
	if err != nil {
		return filepath.Join(a.ProjectRoot, ".capsules", "queue")
	}
	return root
}

func (a PromoteExistingAuthority) store() Store {
	return Store{ProjectRoot: a.ProjectRoot, QueueRoot: a.queueRoot()}
}

func (a PromoteExistingAuthority) storeUnnormalized() Store {
	return Store{ProjectRoot: a.ProjectRoot, QueueRoot: a.QueueRoot}
}

func (a PromoteExistingAuthority) read(key string) (PromoteExistingRecord, bool, error) {
	raw, err := os.ReadFile(a.statePath(key))
	if errors.Is(err, os.ErrNotExist) {
		return PromoteExistingRecord{}, false, nil
	}
	if err != nil {
		return PromoteExistingRecord{}, false, err
	}
	var out PromoteExistingRecord
	if err := json.Unmarshal(raw, &out); err != nil {
		return PromoteExistingRecord{}, false, fmt.Errorf("queue promote-existing: parse durable record: %w", err)
	}
	if out.Schema != PromoteExistingSchema || out.Key != key {
		return PromoteExistingRecord{}, false, fmt.Errorf("queue promote-existing: durable record identity mismatch")
	}
	return out, true, nil
}

func (a PromoteExistingAuthority) write(in PromoteExistingRecord) error {
	raw, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(a.statePath(in.Key), append(raw, '\n'), 0o600, 0o755)
}

func (a PromoteExistingAuthority) lock(key string) (func(), error) {
	path := filepath.Join(a.queueRoot(), "promote-existing", strings.TrimPrefix(key, "sha256:")+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(a.LockWait)
	for {
		acquired, err := tryExclusiveFileLock(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if acquired {
			return func() {
				_ = releaseExclusiveFileLock(file)
				_ = file.Close()
			}, nil
		}
		if a.LockWait <= 0 || time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("queue promote-existing: authority busy: %w", ErrBusy)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
