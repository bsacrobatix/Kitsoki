// Package admissionserver provides the authenticated HTTP edge that imports a
// disposable worker's sealed integration result into Kitsoki's durable merge
// queue. The service owns its queue, receipt, and temporary storage outside the
// protected project checkout; admission never downloads into or writes Git
// objects in that checkout.
package admissionserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/atomicfile"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/objectstore"
)

const (
	RequestSchema   = "capsule-queue-remote-admission-request/v1"
	ResponseSchema  = "capsule-queue-remote-admission-response/v1"
	HandoffSchema   = "pog/integration-train-worker-admission-handoff/v1"
	IntentSchema    = "capsule-queue-remote-admission-intent/v1"
	RecordSchema    = "capsule-queue-remote-admission-record/v1"
	maxRequestBytes = int64(2 << 20)
	maxReceiptBytes = int64(1 << 20)
)

var (
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	safeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	safeRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
)

type Config struct {
	Root           string
	ProjectRoot    string
	ProjectID      string
	Token          string
	RequireAuth    bool
	Objects        objectstore.Store
	MaxBundleBytes int64
	MaxConcurrent  int
	Now            func() time.Time
	// Interrupt is a deterministic crash seam used by tests. Production
	// callers leave it nil.
	Interrupt func(stage string) error
}

type HandoffReceipt struct {
	ReceiptID     string `json:"receipt_id"`
	JobID         string `json:"job_id"`
	SourceSHA     string `json:"source_sha"`
	ContentDigest string `json:"content_digest"`
	RawDigest     string `json:"raw_digest"`
}

type HandoffBundle struct {
	Key     string `json:"key"`
	Digest  string `json:"digest"`
	Bytes   int64  `json:"bytes"`
	Head    string `json:"head"`
	BaseSHA string `json:"base_sha"`
}

type Handoff struct {
	Schema        string                     `json:"schema"`
	HandoffKey    string                     `json:"handoff_key"`
	Result        queue.ExternalWorkerResult `json:"result"`
	ResultDigest  string                     `json:"result_digest"`
	Receipt       HandoffReceipt             `json:"receipt"`
	Bundle        HandoffBundle              `json:"bundle"`
	HandoffDigest string                     `json:"handoff_digest"`
}

// Request carries the small sealed control records inline and names the large
// Git bundle through Handoff.Bundle.Key. ReceiptBase64 preserves the exact
// receipt bytes whose digest the worker sealed, including whitespace.
type Request struct {
	Schema        string  `json:"schema"`
	Handoff       Handoff `json:"handoff"`
	ReceiptBase64 string  `json:"receipt_base64"`
	// RunRecordBase64 is optional for backwards-compatible worker handoffs.
	// New sealed candidate admissions supply it so the external authority has
	// the exact verified CI record required by final promotion.
	RunRecordBase64    string                   `json:"run_record_base64,omitempty"`
	TargetBaseSHA      string                   `json:"target_base_sha"`
	TargetPolicy       queue.TargetPolicy       `json:"target_policy,omitempty"`
	FinalizationPolicy queue.FinalizationPolicy `json:"finalization_policy,omitempty"`
	Paths              []string                 `json:"paths,omitempty"`
	RuntimeInstance    string                   `json:"runtime_instance,omitempty"`
	RuntimeReceipt     string                   `json:"runtime_receipt,omitempty"`
	RequiredReceiptIDs []string                 `json:"required_receipt_ids,omitempty"`
}

type Response struct {
	Schema    string                     `json:"schema"`
	Admission AdmissionRecord            `json:"admission"`
	Anchor    queue.ExternalBundleAnchor `json:"anchor"`
	Candidate queue.Candidate            `json:"candidate"`
}

type AdmissionRecord struct {
	Schema           string    `json:"schema"`
	ID               string    `json:"id"`
	SubmissionDigest string    `json:"submission_digest"`
	HandoffDigest    string    `json:"handoff_digest"`
	ExecutionID      string    `json:"execution_id"`
	ReceiptDigest    string    `json:"receipt_digest"`
	BundleKey        string    `json:"bundle_key"`
	BundleDigest     string    `json:"bundle_digest"`
	AnchorID         string    `json:"anchor_id"`
	CandidateID      string    `json:"candidate_id"`
	CreatedAt        time.Time `json:"created_at"`
	RecordDigest     string    `json:"record_digest"`
}

type intent struct {
	Schema           string    `json:"schema"`
	ExecutionID      string    `json:"execution_id"`
	SubmissionDigest string    `json:"submission_digest"`
	AdmissionID      string    `json:"admission_id"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
	RecordDigest     string    `json:"record_digest,omitempty"`
}

type Server struct {
	cfg       Config
	root      string
	project   string
	queueRoot string
	store     queue.Store
	mu        sync.Mutex
	limit     chan struct{}
}

type admissionError struct {
	status int
	code   string
	err    error
}

func (e *admissionError) Error() string { return e.err.Error() }
func (e *admissionError) Unwrap() error { return e.err }

func fail(status int, code, message string, args ...any) error {
	return &admissionError{status: status, code: code, err: fmt.Errorf(message, args...)}
}

func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, fmt.Errorf("queue admission: durable root is required")
	}
	if strings.TrimSpace(cfg.ProjectRoot) == "" {
		return nil, fmt.Errorf("queue admission: protected project root is required")
	}
	if cfg.Objects == nil {
		return nil, fmt.Errorf("queue admission: object store is required")
	}
	if cfg.RequireAuth && strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("queue admission: authentication token is required")
	}
	root, err := canonicalProspectiveDirectory(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("queue admission: durable root: %w", err)
	}
	project, err := canonicalDirectory(cfg.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("queue admission: project root: %w", err)
	}
	if within(project, root) || within(root, project) || root == project {
		return nil, fmt.Errorf("queue admission: durable root must be outside the protected project checkout")
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("queue admission: durable root: %w", err)
	}
	if cfg.MaxBundleBytes <= 0 || cfg.MaxBundleBytes > queue.DefaultMaxExternalBundle {
		cfg.MaxBundleBytes = queue.DefaultMaxExternalBundle
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	for _, dir := range []string{"queue", "receipts", "admissions", "intents", "incoming"} {
		path := filepath.Join(root, dir)
		if err := ensurePrivateDirectory(path); err != nil {
			return nil, fmt.Errorf("queue admission: create private %s: %w", dir, err)
		}
	}
	queueRoot := filepath.Join(root, "queue")
	cfg.Root, cfg.ProjectRoot = root, project
	return &Server{
		cfg: cfg, root: root, project: project, queueRoot: queueRoot,
		store: queue.Store{ProjectRoot: project, QueueRoot: queueRoot},
		limit: make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s must be a real directory", absolute)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

// canonicalProspectiveDirectory resolves every existing ancestor without
// creating the requested path. That ordering is security-significant: a
// misconfigured authority nested inside the protected project is rejected
// before New writes even an empty directory there.
func canonicalProspectiveDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	var suffix []string
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("%s must resolve through real directories", current)
			}
			canonical, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", evalErr
			}
			parts := append([]string{canonical}, suffix...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", statErr
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a real directory", path)
	}
	return os.Chmod(path, 0o700)
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/queue/admissions", s.admitHTTP)
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Kitsoki-Request-ID"))
		if requestID == "" {
			requestID = fmt.Sprintf("admission-%d", s.cfg.Now().UnixNano())
		}
		w.Header().Set("X-Kitsoki-Request-ID", requestID)
		r.Header.Set("X-Kitsoki-Request-ID", requestID)
		if s.cfg.RequireAuth || s.cfg.Token != "" {
			header := r.Header.Get("Authorization")
			got := ""
			if strings.HasPrefix(header, "Bearer ") {
				got = strings.TrimPrefix(header, "Bearer ")
			}
			if len(got) != len(s.cfg.Token) || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
				writeError(w, http.StatusUnauthorized, "unauthorized", "authentication failed", requestID)
				return
			}
		}
		select {
		case s.limit <- struct{}{}:
			defer func() { <-s.limit }()
		default:
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "admission capacity is temporarily exhausted", requestID)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) admitHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		writeAdmissionError(w, fail(http.StatusBadRequest, "malformed_request", "invalid remote admission request: %v", err), requestID(r))
		return
	}
	if err := ensureEOF(decoder); err != nil {
		writeAdmissionError(w, fail(http.StatusBadRequest, "malformed_request", "%v", err), requestID(r))
		return
	}
	response, err := s.Admit(r.Context(), request)
	if err != nil {
		writeAdmissionError(w, err, requestID(r))
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

// Admit validates, durably intents, fetches, independently re-verifies, and
// queue-admits one exact worker handoff. It is safe to call again after any
// injected interruption or process restart with the exact same request.
func (s *Server) Admit(ctx context.Context, request Request) (Response, error) {
	normalized, receiptRaw, parsedReceipt, submissionDigest, err := s.validateRequest(request)
	if err != nil {
		return Response{}, err
	}
	executionID := normalized.Handoff.Result.ExecutionID
	admissionID := "admission-" + strings.TrimPrefix(submissionDigest, "sha256:")[:32]

	s.mu.Lock()
	defer s.mu.Unlock()

	existingIntent, found, err := s.readIntent(executionID)
	if err != nil {
		return Response{}, err
	}
	if found {
		if existingIntent.ExecutionID != executionID || existingIntent.SubmissionDigest != submissionDigest ||
			existingIntent.AdmissionID != admissionID {
			return Response{}, fail(http.StatusConflict, "replay_substitution", "execution %s is already bound to a different immutable admission", executionID)
		}
		if response, ok, readErr := s.readResponse(admissionID, submissionDigest); readErr != nil {
			return Response{}, readErr
		} else if ok {
			if err := s.completeIntent(existingIntent, response.Admission); err != nil {
				return Response{}, err
			}
			return response, nil
		}
	} else {
		existingIntent = intent{
			Schema: IntentSchema, ExecutionID: executionID,
			SubmissionDigest: submissionDigest, AdmissionID: admissionID,
			Status: "pending", CreatedAt: s.cfg.Now().UTC(),
		}
		if err := s.writeIntent(existingIntent); err != nil {
			return Response{}, err
		}
	}
	if err := s.interrupt("after-intent"); err != nil {
		return Response{}, err
	}

	receiptPath, err := s.persistReceipt(parsedReceipt, receiptRaw)
	if err != nil {
		return Response{}, err
	}
	runRecordPath, err := s.persistRunRecord(parsedReceipt, normalized.RunRecordBase64)
	if err != nil {
		return Response{}, err
	}
	bundlePath, err := s.fetchBundle(ctx, normalized.Handoff.Bundle)
	if err != nil {
		return Response{}, err
	}
	defer os.Remove(bundlePath)
	if err := s.interrupt("after-bundle"); err != nil {
		return Response{}, err
	}

	result := normalized.Handoff.Result
	candidate, anchor, err := s.store.AdmitExternalBundle(ctx, queue.ExternalBundleSubmission{
		Result:     result,
		BundlePath: bundlePath,
		Submit: queue.Submit{
			Branch: result.Branch, SHA: result.CandidateSHA,
			TargetRef: result.TargetRef, TargetBaseSHAAtAdmission: normalized.TargetBaseSHA,
			TargetPolicy: normalized.TargetPolicy, Receipt: parsedReceipt, ReceiptRef: receiptPath, RunRecordRef: runRecordPath,
			Backend: "remote-external-worker", Paths: normalized.Paths,
			FinalizationPolicy: normalized.FinalizationPolicy, ManifestDigest: result.ManifestDigest,
			RuntimeInstance: normalized.RuntimeInstance, RuntimeReceipt: normalized.RuntimeReceipt,
			RequiredReceiptIDs: normalized.RequiredReceiptIDs,
		},
	})
	if err != nil {
		return Response{}, fail(http.StatusUnprocessableEntity, "admission_rejected", "queue admission rejected: %v", err)
	}
	if err := s.interrupt("after-queue"); err != nil {
		return Response{}, err
	}

	record := AdmissionRecord{
		Schema: RecordSchema, ID: admissionID, SubmissionDigest: submissionDigest,
		HandoffDigest: normalized.Handoff.HandoffDigest, ExecutionID: executionID,
		ReceiptDigest: normalized.Handoff.Receipt.RawDigest,
		BundleKey:     normalized.Handoff.Bundle.Key, BundleDigest: normalized.Handoff.Bundle.Digest,
		AnchorID: anchor.ID, CandidateID: candidate.ID, CreatedAt: s.cfg.Now().UTC(),
	}
	record.RecordDigest, err = recordDigest(record)
	if err != nil {
		return Response{}, err
	}
	response := Response{Schema: ResponseSchema, Admission: record, Anchor: anchor, Candidate: candidate}
	if err := s.writeResponse(response); err != nil {
		return Response{}, err
	}
	if err := s.interrupt("after-record"); err != nil {
		return Response{}, err
	}
	if err := s.completeIntent(existingIntent, record); err != nil {
		return Response{}, err
	}
	return response, nil
}

func (s *Server) validateRequest(request Request) (Request, []byte, receipt.Receipt, string, error) {
	if request.Schema != RequestSchema {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusBadRequest, "unsupported_schema", "request schema must be %s", RequestSchema)
	}
	if err := validateHandoff(request.Handoff, s.cfg.MaxBundleBytes); err != nil {
		return Request{}, nil, receipt.Receipt{}, "", err
	}
	if !fullSHA(request.TargetBaseSHA) || request.TargetBaseSHA != request.Handoff.Result.BaseSHA {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusBadRequest, "target_mismatch", "target_base_sha must equal the sealed worker base SHA")
	}
	if request.TargetPolicy == "" {
		request.TargetPolicy = queue.WaveAutoPolicy
	}
	if request.TargetPolicy != queue.WaveAutoPolicy && request.TargetPolicy != queue.StewardApprovedPolicy {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusBadRequest, "invalid_policy", "unsupported target policy %q", request.TargetPolicy)
	}
	if request.FinalizationPolicy == "" {
		request.FinalizationPolicy = queue.AutonomousFinalization
	}
	if request.FinalizationPolicy != queue.AutonomousFinalization && request.FinalizationPolicy != queue.StewardReviewFinalization {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusBadRequest, "invalid_policy", "unsupported finalization policy %q", request.FinalizationPolicy)
	}
	receiptRaw, err := decodeReceipt(request.ReceiptBase64)
	if err != nil {
		return Request{}, nil, receipt.Receipt{}, "", err
	}
	if digestBytes(receiptRaw) != request.Handoff.Receipt.RawDigest {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusBadRequest, "receipt_tampered", "receipt raw digest does not match sealed handoff")
	}
	parsedReceipt, err := parseReceipt(receiptRaw)
	if err != nil {
		return Request{}, nil, receipt.Receipt{}, "", err
	}
	result := request.Handoff.Result
	verification := receipt.Verify(parsedReceipt, nil, false)
	if verification.Status != "valid" || !verification.PromotionEligible {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusUnprocessableEntity, "receipt_invalid", "receipt is not valid and promotion eligible")
	}
	if parsedReceipt.ReceiptID != result.ReceiptID || parsedReceipt.JobID != result.JobID ||
		parsedReceipt.Envelope.SourceDigest != result.CandidateSHA ||
		parsedReceipt.Verdict.SourceDigest != result.CandidateSHA {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusUnprocessableEntity, "receipt_mismatch", "receipt does not bind the exact worker result")
	}
	if request.Handoff.Receipt.ReceiptID != parsedReceipt.ReceiptID ||
		request.Handoff.Receipt.JobID != parsedReceipt.JobID ||
		request.Handoff.Receipt.SourceSHA != result.CandidateSHA ||
		request.Handoff.Receipt.ContentDigest != parsedReceipt.Integrity.ContentDigest {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusUnprocessableEntity, "receipt_mismatch", "sealed receipt summary does not match the receipt")
	}
	if s.cfg.ProjectID != "" && parsedReceipt.ProjectID != s.cfg.ProjectID {
		return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusUnprocessableEntity, "project_mismatch", "receipt project %q is not admitted by this service", parsedReceipt.ProjectID)
	}
	if request.RunRecordBase64 != "" {
		raw, err := decodeRunRecord(request.RunRecordBase64)
		if err != nil {
			return Request{}, nil, receipt.Receipt{}, "", err
		}
		run, err := parseRunRecord(raw)
		if err != nil {
			return Request{}, nil, receipt.Receipt{}, "", err
		}
		if !runRecordMatchesReceipt(run, parsedReceipt) {
			return Request{}, nil, receipt.Receipt{}, "", fail(http.StatusUnprocessableEntity, "run_record_mismatch", "run record is not the verified result for the sealed receipt")
		}
	}
	request.Paths = cleanStrings(request.Paths)
	request.RequiredReceiptIDs = cleanStrings(request.RequiredReceiptIDs)
	fingerprint := map[string]any{
		"handoff_digest":       request.Handoff.HandoffDigest,
		"receipt_digest":       request.Handoff.Receipt.RawDigest,
		"run_record_digest":    digestEncoded(request.RunRecordBase64),
		"target_base_sha":      request.TargetBaseSHA,
		"target_policy":        string(request.TargetPolicy),
		"finalization_policy":  string(request.FinalizationPolicy),
		"paths":                request.Paths,
		"runtime_instance":     strings.TrimSpace(request.RuntimeInstance),
		"runtime_receipt":      strings.TrimSpace(request.RuntimeReceipt),
		"required_receipt_ids": request.RequiredReceiptIDs,
	}
	raw, err := canonicalJSON(fingerprint)
	if err != nil {
		return Request{}, nil, receipt.Receipt{}, "", err
	}
	return request, receiptRaw, parsedReceipt, digestBytes(raw), nil
}

func validateHandoff(handoff Handoff, maxBundleBytes int64) error {
	if handoff.Schema != HandoffSchema {
		return fail(http.StatusBadRequest, "unsupported_handoff", "handoff schema must be %s", HandoffSchema)
	}
	result := handoff.Result
	if err := queue.ValidateExternalWorkerResult(result, maxBundleBytes); err != nil {
		return fail(http.StatusBadRequest, "invalid_result", "%v", err)
	}
	for name, value := range map[string]string{
		"execution_id": result.ExecutionID, "job_id": result.JobID, "train_id": result.TrainID,
	} {
		if !safeIDPattern.MatchString(value) {
			return fail(http.StatusBadRequest, "invalid_result", "%s is not safe for remote admission", name)
		}
	}
	if !safeRefPattern.MatchString(result.Branch) || !safeRefPattern.MatchString(result.TargetRef) ||
		!digestPattern.MatchString(result.ReceiptID) ||
		!digestPattern.MatchString(handoff.ResultDigest) ||
		!digestPattern.MatchString(handoff.HandoffDigest) {
		return fail(http.StatusBadRequest, "invalid_result", "worker refs and digests are not safe for remote admission")
	}
	if result.BundleKey != "runs/"+result.ExecutionID+"/wip/refs.bundle" ||
		handoff.HandoffKey != "runs/"+result.ExecutionID+"/artifacts/integration-train-external-admission-handoff.json" {
		return fail(http.StatusBadRequest, "invalid_object_reference", "handoff object keys are not derived from execution_id")
	}
	if handoff.Bundle.Key != result.BundleKey || handoff.Bundle.Digest != result.BundleDigest ||
		handoff.Bundle.Bytes != result.BundleBytes || handoff.Bundle.Head != result.CandidateSHA ||
		handoff.Bundle.BaseSHA != result.BaseSHA {
		return fail(http.StatusBadRequest, "bundle_mismatch", "sealed bundle summary does not match worker result")
	}
	if handoff.Receipt.ReceiptID != result.ReceiptID || handoff.Receipt.JobID != result.JobID ||
		handoff.Receipt.SourceSHA != result.CandidateSHA ||
		!digestPattern.MatchString(handoff.Receipt.ContentDigest) ||
		!digestPattern.MatchString(handoff.Receipt.RawDigest) {
		return fail(http.StatusBadRequest, "receipt_mismatch", "sealed receipt summary does not match worker result")
	}
	resultRaw, err := canonicalJSON(result)
	if err != nil {
		return err
	}
	if handoff.ResultDigest != digestBytes(resultRaw) {
		return fail(http.StatusBadRequest, "handoff_tampered", "result digest does not match handoff")
	}
	unsigned := handoff
	unsigned.HandoffDigest = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return err
	}
	var generic map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return err
	}
	delete(generic, "handoff_digest")
	canonical, err := canonicalJSON(generic)
	if err != nil {
		return err
	}
	if handoff.HandoffDigest != digestBytes(canonical) {
		return fail(http.StatusBadRequest, "handoff_tampered", "handoff digest does not match sealed content")
	}
	return nil
}

func decodeReceipt(encoded string) ([]byte, error) {
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(int(maxReceiptBytes)) {
		return nil, fail(http.StatusBadRequest, "receipt_invalid", "receipt_base64 is required and bounded")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) == 0 || int64(len(raw)) > maxReceiptBytes {
		return nil, fail(http.StatusBadRequest, "receipt_invalid", "receipt_base64 is malformed or oversized")
	}
	return raw, nil
}

func parseReceipt(raw []byte) (receipt.Receipt, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var parsed receipt.Receipt
	if err := decoder.Decode(&parsed); err != nil {
		return receipt.Receipt{}, fail(http.StatusBadRequest, "receipt_invalid", "parse receipt: %v", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return receipt.Receipt{}, fail(http.StatusBadRequest, "receipt_invalid", "%v", err)
	}
	return parsed, nil
}

func decodeRunRecord(encoded string) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(int(maxReceiptBytes)) {
		return nil, fail(http.StatusBadRequest, "run_record_invalid", "run_record_base64 is oversized")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) == 0 || int64(len(raw)) > maxReceiptBytes {
		return nil, fail(http.StatusBadRequest, "run_record_invalid", "run_record_base64 is malformed or oversized")
	}
	return raw, nil
}

func parseRunRecord(raw []byte) (ci.RunRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var run ci.RunRecord
	if err := decoder.Decode(&run); err != nil {
		return ci.RunRecord{}, fail(http.StatusBadRequest, "run_record_invalid", "parse run record: %v", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return ci.RunRecord{}, fail(http.StatusBadRequest, "run_record_invalid", "%v", err)
	}
	return run, nil
}

func runRecordMatchesReceipt(run ci.RunRecord, r receipt.Receipt) bool {
	result := run.Result
	return run.JobID == r.JobID && run.ReceiptID == r.ReceiptID && run.ReceiptVerification == "valid" &&
		string(result.Job.ID) == r.JobID && result.Envelope.Digest == r.Envelope.Digest &&
		result.Envelope.SourceDigest == r.Envelope.SourceDigest && result.Envelope.StoryDigest == r.Envelope.StoryDigest &&
		result.Envelope.Environment.Digest == r.Envelope.Environment.Digest && reflect.DeepEqual(result.Verdict, r.Verdict)
}

func digestEncoded(encoded string) string {
	if encoded == "" {
		return ""
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "invalid"
	}
	return digestBytes(raw)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func (s *Server) persistReceipt(parsed receipt.Receipt, raw []byte) (string, error) {
	path := filepath.Join(s.root, "receipts", strings.TrimPrefix(parsed.ReceiptID, "sha256:")+".json")
	if existing, err := readBoundedRegular(path, maxReceiptBytes); err == nil {
		if !bytes.Equal(existing, raw) {
			return "", fail(http.StatusConflict, "receipt_substitution", "immutable receipt %s changed bytes", parsed.ReceiptID)
		}
		return path, nil
	} else if !os.IsNotExist(errors.Unwrap(err)) && !os.IsNotExist(err) {
		return "", err
	}
	if err := atomicfile.WriteFile(path, raw, 0o600, 0o700); err != nil {
		return "", fmt.Errorf("queue admission: persist receipt: %w", err)
	}
	return path, nil
}

func (s *Server) persistRunRecord(parsed receipt.Receipt, encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := decodeRunRecord(encoded)
	if err != nil {
		return "", err
	}
	run, err := parseRunRecord(raw)
	if err != nil {
		return "", err
	}
	if !runRecordMatchesReceipt(run, parsed) {
		return "", fail(http.StatusUnprocessableEntity, "run_record_mismatch", "run record is not the verified result for the sealed receipt")
	}
	path := filepath.Join(s.root, "run-records", strings.TrimPrefix(parsed.ReceiptID, "sha256:")+".run.json")
	if existing, err := readBoundedRegular(path, maxReceiptBytes); err == nil {
		if !bytes.Equal(existing, raw) {
			return "", fail(http.StatusConflict, "run_record_substitution", "immutable run record for %s changed bytes", parsed.ReceiptID)
		}
		return path, nil
	} else if !os.IsNotExist(errors.Unwrap(err)) && !os.IsNotExist(err) {
		return "", err
	}
	if err := atomicfile.WriteFile(path, raw, 0o600, 0o700); err != nil {
		return "", fmt.Errorf("queue admission: persist run record: %w", err)
	}
	return path, nil
}

func (s *Server) fetchBundle(ctx context.Context, bundle HandoffBundle) (string, error) {
	meta, err := s.cfg.Objects.Head(ctx, bundle.Key)
	if err != nil {
		return "", fail(http.StatusBadGateway, "bundle_unavailable", "head remote bundle: %v", err)
	}
	if meta.Key != bundle.Key || meta.Size != bundle.Bytes || meta.Size <= 0 || meta.Size > s.cfg.MaxBundleBytes {
		return "", fail(http.StatusUnprocessableEntity, "bundle_mismatch", "remote bundle metadata does not match sealed size")
	}
	body, getMeta, err := s.cfg.Objects.Get(ctx, bundle.Key)
	if err != nil {
		return "", fail(http.StatusBadGateway, "bundle_unavailable", "get remote bundle: %v", err)
	}
	defer body.Close()
	if getMeta.Key != bundle.Key || getMeta.Size != bundle.Bytes {
		return "", fail(http.StatusUnprocessableEntity, "bundle_mismatch", "remote bundle GET metadata does not match sealed identity")
	}
	temp, err := os.CreateTemp(filepath.Join(s.root, "incoming"), ".bundle-*.tmp")
	if err != nil {
		return "", fmt.Errorf("queue admission: create private bundle temp: %w", err)
	}
	path := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(body, s.cfg.MaxBundleBytes+1))
	if err != nil {
		return "", fail(http.StatusBadGateway, "bundle_unavailable", "stream remote bundle: %v", err)
	}
	if written != bundle.Bytes {
		return "", fail(http.StatusUnprocessableEntity, "bundle_mismatch", "remote bundle stream size does not match sealed identity")
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != bundle.Digest {
		return "", fail(http.StatusUnprocessableEntity, "bundle_tampered", "remote bundle digest does not match sealed identity")
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func (s *Server) interrupt(stage string) error {
	if s.cfg.Interrupt == nil {
		return nil
	}
	if err := s.cfg.Interrupt(stage); err != nil {
		return fail(http.StatusInternalServerError, "interrupted", "admission interrupted at %s: %v", stage, err)
	}
	return nil
}

func (s *Server) intentPath(executionID string) string {
	sum := sha256.Sum256([]byte(executionID))
	return filepath.Join(s.root, "intents", hex.EncodeToString(sum[:16])+".json")
}

func (s *Server) recordPath(admissionID string) string {
	return filepath.Join(s.root, "admissions", admissionID+".json")
}

func (s *Server) readIntent(executionID string) (intent, bool, error) {
	raw, err := readBoundedRegular(s.intentPath(executionID), 64<<10)
	if os.IsNotExist(errors.Unwrap(err)) || os.IsNotExist(err) {
		return intent{}, false, nil
	}
	if err != nil {
		return intent{}, false, err
	}
	var value intent
	if err := decodeStrict(raw, &value); err != nil {
		return intent{}, false, fmt.Errorf("queue admission: parse intent: %w", err)
	}
	if value.Schema != IntentSchema || value.ExecutionID == "" || value.SubmissionDigest == "" ||
		value.AdmissionID == "" || (value.Status != "pending" && value.Status != "complete") {
		return intent{}, false, fmt.Errorf("queue admission: invalid durable intent")
	}
	return value, true, nil
}

func (s *Server) writeIntent(value intent) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.intentPath(value.ExecutionID), append(raw, '\n'), 0o600, 0o700)
}

func (s *Server) completeIntent(value intent, record AdmissionRecord) error {
	if value.Status == "complete" && value.RecordDigest == record.RecordDigest {
		return nil
	}
	value.Status = "complete"
	value.CompletedAt = s.cfg.Now().UTC()
	value.RecordDigest = record.RecordDigest
	return s.writeIntent(value)
}

func (s *Server) writeResponse(response Response) error {
	raw, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.recordPath(response.Admission.ID), append(raw, '\n'), 0o600, 0o700)
}

func (s *Server) readResponse(admissionID, submissionDigest string) (Response, bool, error) {
	raw, err := readBoundedRegular(s.recordPath(admissionID), 512<<10)
	if os.IsNotExist(errors.Unwrap(err)) || os.IsNotExist(err) {
		return Response{}, false, nil
	}
	if err != nil {
		return Response{}, false, err
	}
	var response Response
	if err := decodeStrict(raw, &response); err != nil {
		return Response{}, false, fmt.Errorf("queue admission: parse record: %w", err)
	}
	record := response.Admission
	digest, err := recordDigest(record)
	if err != nil {
		return Response{}, false, err
	}
	if response.Schema != ResponseSchema || record.Schema != RecordSchema ||
		record.ID != admissionID || record.SubmissionDigest != submissionDigest ||
		record.RecordDigest != digest {
		return Response{}, false, fmt.Errorf("queue admission: immutable admission record failed verification")
	}
	candidate, err := s.store.Get(record.CandidateID)
	if err != nil || candidate.ID != response.Candidate.ID || candidate.SourceAnchorID != record.AnchorID {
		return Response{}, false, fmt.Errorf("queue admission: durable candidate no longer matches admission record")
	}
	anchor, err := s.store.ExternalAnchor(context.Background(), record.AnchorID)
	if err != nil || anchor.ID != response.Anchor.ID || anchor.Result.ExecutionID != record.ExecutionID {
		return Response{}, false, fmt.Errorf("queue admission: durable anchor no longer matches admission record")
	}
	response.Candidate, response.Anchor = candidate, anchor
	return response, true, nil
}

func recordDigest(record AdmissionRecord) (string, error) {
	record.RecordDigest = ""
	raw, err := canonicalJSON(record)
	if err != nil {
		return "", err
	}
	return digestBytes(raw), nil
}

func readBoundedRegular(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("queue admission: inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > max {
		return nil, fmt.Errorf("queue admission: %s must be a bounded regular non-symlink file", path)
	}
	return os.ReadFile(path)
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, generic); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		raw, _ := json.Marshal(typed)
		out.Write(raw)
	case json.Number:
		if _, err := strconv.ParseInt(string(typed), 10, 64); err != nil {
			return fmt.Errorf("queue admission: non-integral canonical number %q", typed)
		}
		out.WriteString(string(typed))
	case []any:
		out.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			raw, _ := json.Marshal(key)
			out.Write(raw)
			out.WriteByte(':')
			if err := writeCanonical(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("queue admission: unsupported canonical JSON type %T", value)
	}
	return nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fullSHA(value string) bool {
	return len(value) == 40 && strings.Trim(value, "0123456789abcdef") == ""
}

func cleanStrings(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	if result == nil {
		result = []string{}
	}
	return result
}

func requestID(r *http.Request) string {
	return r.Header.Get("X-Kitsoki-Request-ID")
}

func writeAdmissionError(w http.ResponseWriter, err error, requestID string) {
	var typed *admissionError
	if errors.As(err, &typed) {
		writeError(w, typed.status, typed.code, typed.err.Error(), requestID)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "remote admission failed", requestID)
}

func writeError(w http.ResponseWriter, status int, code, message, requestID string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message, "request_id": requestID},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
