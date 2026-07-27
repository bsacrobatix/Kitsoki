package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"kitsoki/internal/capsule/admissionserver"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/record"
	"kitsoki/internal/objectstore"
)

const remoteAdmissionExecutionSchema = "capsule-remote-admission-execution/v1"

type remoteAdmissionOptions struct {
	URL           string
	TokenEnv      string
	BucketURL     string
	KeyEnv        string
	SecretEnv     string
	TargetBaseSHA string
	TrainID       string
	StatusCommand string
}

type remoteAdmissionResult struct {
	Schema      string                   `json:"schema"`
	ExecutionID string                   `json:"execution_id"`
	BundleKey   string                   `json:"bundle_key"`
	HandoffKey  string                   `json:"handoff_key"`
	Response    admissionserver.Response `json:"response"`
	Status      remoteCandidateStatus    `json:"status"`
}

// remoteCandidateStatus is a snapshot, not a poll. The external admission
// record is durable, while this explicit operator command is the supported
// way to observe later worker state on the queue authority host.
type remoteCandidateStatus struct {
	CandidateID   string       `json:"candidate_id"`
	Phase         queue.Status `json:"phase"`
	Terminal      bool         `json:"terminal"`
	StatusCommand string       `json:"status_command"`
}

type remoteAdmissionHTTPError struct {
	Status     int
	Code       string
	RequestID  string
	Message    string
	RetryAfter time.Duration
	Retryable  bool
}

func (e *remoteAdmissionHTTPError) Error() string {
	return fmt.Sprintf("capsule promote: remote admission HTTP %d (%s, request %s): %s", e.Status, e.Code, e.RequestID, e.Message)
}

func remoteCapsuleAdmission(ctx context.Context, opts capsulePromoteOptions, instance control.Instance, workspacePath, branch string, stored record.Stored) (remoteAdmissionResult, error) {
	if err := validateRemoteAdmissionOptions(opts); err != nil {
		return remoteAdmissionResult{}, err
	}
	remote := opts.RemoteAdmission
	if stored.Receipt.ReceiptID == "" || stored.Receipt.JobID == "" || stored.Receipt.Envelope.SourceDigest != instance.Head {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: remote admission requires an exact receipt-bound candidate")
	}
	run, err := (ci.FileRunStore{ProjectRoot: opts.ProjectRoot}).Get(stored.Receipt.JobID)
	if err != nil {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: read exact CI run record: %w", err)
	}
	if run.ReceiptID != stored.Receipt.ReceiptID || run.ReceiptVerification != "valid" || run.Result.Envelope.SourceDigest != instance.Head {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: CI run record does not bind the exact reusable receipt")
	}
	receiptRaw, err := os.ReadFile(stored.ReceiptPath)
	if err != nil {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: read exact receipt bytes: %w", err)
	}
	runRaw, err := json.Marshal(run)
	if err != nil {
		return remoteAdmissionResult{}, err
	}
	bundle, err := executor.GitCommitBundle(ctx, workspacePath, instance.Head, 0)
	if err != nil {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: seal remote source bundle: %w", err)
	}
	executionID := remoteExecutionID(instance, opts, stored, bundle.Digest)
	result := queue.ExternalWorkerResult{
		Schema: queue.ExternalWorkerResultSchema, ExecutionID: executionID, JobID: stored.Receipt.JobID,
		TrainID: remote.TrainID, ManifestDigest: remoteManifestDigest(instance, opts, stored),
		Branch: branch, CandidateSHA: instance.Head, BaseSHA: remote.TargetBaseSHA, TargetRef: opts.TargetRef,
		ReceiptID: stored.Receipt.ReceiptID, BundleDigest: bundle.Digest, BundleBytes: bundle.Size,
		BundleKey: "runs/" + executionID + "/wip/refs.bundle",
	}
	handoff, err := admissionserver.SealHandoff(result, receiptRaw)
	if err != nil {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: seal remote admission handoff: %w", err)
	}
	objects, err := remoteObjectStore(remote)
	if err != nil {
		return remoteAdmissionResult{}, err
	}
	if err := putImmutableObject(ctx, objects, result.BundleKey, bundle.Data, "application/vnd.git.bundle"); err != nil {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: publish sealed bundle: %w", err)
	}
	handoffRaw, err := json.MarshalIndent(handoff, "", "  ")
	if err != nil {
		return remoteAdmissionResult{}, err
	}
	handoffRaw = append(handoffRaw, '\n')
	if err := putImmutableObject(ctx, objects, handoff.HandoffKey, handoffRaw, "application/json"); err != nil {
		return remoteAdmissionResult{}, fmt.Errorf("capsule promote: publish sealed handoff: %w", err)
	}
	request := admissionserver.Request{
		Schema: admissionserver.RequestSchema, Handoff: handoff,
		ReceiptBase64: base64.StdEncoding.EncodeToString(receiptRaw), RunRecordBase64: base64.StdEncoding.EncodeToString(runRaw),
		TargetBaseSHA: remote.TargetBaseSHA,
	}
	response, err := postRemoteAdmission(ctx, remote, request)
	if err != nil {
		return remoteAdmissionResult{}, err
	}
	return remoteAdmissionResult{Schema: remoteAdmissionExecutionSchema, ExecutionID: executionID, BundleKey: result.BundleKey, HandoffKey: handoff.HandoffKey, Response: response, Status: remoteCandidateStatus{CandidateID: response.Candidate.ID, Phase: response.Candidate.Status, Terminal: remoteCandidateTerminal(response.Candidate.Status), StatusCommand: remote.StatusCommand}}, nil
}

// validateRemoteAdmissionOptions runs before CI dispatch. Missing credentials
// or an ambiguous destination must never consume a worker attempt.
func validateRemoteAdmissionOptions(opts capsulePromoteOptions) error {
	remote := opts.RemoteAdmission
	if strings.TrimSpace(remote.URL) == "" {
		return fmt.Errorf("capsule promote: remote admission URL is required")
	}
	if !strings.HasPrefix(opts.TargetRef, "integration/") || strings.TrimPrefix(opts.TargetRef, "integration/") == "" {
		return fmt.Errorf("capsule promote: remote admission target must be an explicit integration/* ref")
	}
	if !fullGitSHA(remote.TargetBaseSHA) {
		return fmt.Errorf("capsule promote: --remote-target-base-sha must be a lowercase full Git SHA")
	}
	if strings.TrimSpace(remote.TrainID) == "" || strings.ContainsAny(remote.TrainID, " /\\\t\n") {
		return fmt.Errorf("capsule promote: --remote-train must be a safe non-empty train id")
	}
	if strings.TrimSpace(remote.StatusCommand) == "" || strings.ContainsAny(remote.StatusCommand, "\r\n") ||
		!strings.Contains(remote.StatusCommand, "queue status") || !strings.Contains(remote.StatusCommand, "--json") {
		return fmt.Errorf("capsule promote: --remote-status-command must be the exact hosted `kitsoki queue status ... --json` command")
	}
	if strings.TrimSpace(remote.BucketURL) == "" {
		return fmt.Errorf("capsule promote: --remote-bucket-url is required for remote admission")
	}
	if strings.TrimSpace(remote.TokenEnv) == "" || strings.TrimSpace(os.Getenv(remote.TokenEnv)) == "" {
		return fmt.Errorf("capsule promote: remote admission token is missing from %s", remote.TokenEnv)
	}
	if strings.TrimSpace(remote.KeyEnv) == "" || strings.TrimSpace(remote.SecretEnv) == "" {
		return fmt.Errorf("capsule promote: remote object-store credential environment names are required")
	}
	if _, err := remoteObjectStore(remote); err != nil {
		return fmt.Errorf("capsule promote: remote object store preflight: %w", err)
	}
	return nil
}

func remoteObjectStore(remote remoteAdmissionOptions) (objectstore.Store, error) {
	config, err := objectstore.ParseBucketURL(remote.BucketURL)
	if err != nil {
		return nil, err
	}
	config.KeyEnv, config.SecretEnv = remote.KeyEnv, remote.SecretEnv
	return objectstore.NewSpaces(config, nil)
}

func putImmutableObject(ctx context.Context, objects objectstore.Store, key string, raw []byte, contentType string) error {
	meta, err := objects.Head(ctx, key)
	if err == nil {
		if meta.Key != key || meta.Size != int64(len(raw)) {
			return fmt.Errorf("immutable object %s already has different size", key)
		}
		body, gotMeta, getErr := objects.Get(ctx, key)
		if getErr != nil {
			return getErr
		}
		defer body.Close()
		existing, readErr := io.ReadAll(io.LimitReader(body, int64(len(raw))+1))
		if readErr != nil {
			return readErr
		}
		if gotMeta.Key != key || gotMeta.Size != int64(len(raw)) || !bytes.Equal(existing, raw) {
			return fmt.Errorf("immutable object %s already exists with different bytes", key)
		}
		return nil
	}
	if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	if _, err = objects.Put(ctx, key, bytes.NewReader(raw), int64(len(raw)), objectstore.PutOptions{ContentType: contentType}); err != nil {
		return err
	}
	// Object stores do not give this minimal interface an If-None-Match
	// primitive. Verify the value immediately; because the key includes the
	// sealed bundle digest, a conflicting writer receives a distinct key and
	// cannot replace this identity.
	return putImmutableObject(ctx, objects, key, raw, contentType)
}

func postRemoteAdmission(ctx context.Context, remote remoteAdmissionOptions, request admissionserver.Request) (admissionserver.Response, error) {
	token := strings.TrimSpace(os.Getenv(remote.TokenEnv))
	if token == "" {
		return admissionserver.Response{}, fmt.Errorf("capsule promote: remote admission token is missing from %s", remote.TokenEnv)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return admissionserver.Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(remote.URL, "/")+"/v1/queue/admissions", bytes.NewReader(raw))
	if err != nil {
		return admissionserver.Response{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return admissionserver.Response{}, fmt.Errorf("capsule promote: submit remote admission: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return admissionserver.Response{}, err
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		typed := &remoteAdmissionHTTPError{Status: response.StatusCode, Code: failure.Error.Code, RequestID: failure.Error.RequestID, Message: failure.Error.Message}
		if response.StatusCode == http.StatusTooManyRequests {
			typed.Retryable = true
			typed.RetryAfter = boundedRetryAfter(response.Header.Get("Retry-After"))
		}
		return admissionserver.Response{}, typed
	}
	var admitted admissionserver.Response
	if err := json.Unmarshal(body, &admitted); err != nil {
		return admissionserver.Response{}, fmt.Errorf("capsule promote: parse remote admission response: %w", err)
	}
	if admitted.Schema != admissionserver.ResponseSchema || admitted.Admission.CandidateID == "" {
		return admissionserver.Response{}, fmt.Errorf("capsule promote: remote admission response is missing durable identity")
	}
	return admitted, nil
}

func remoteExecutionID(instance control.Instance, opts capsulePromoteOptions, stored record.Stored, bundleDigest string) string {
	// The object key is derived from the sealed bundle digest as well as the
	// receipt-bound candidate. A retry therefore reuses exactly one object,
	// while a byte-different bundle can never overwrite its predecessor.
	identity := strings.Join([]string{instance.ID, fmt.Sprint(instance.Generation), opts.TargetRef, stored.Receipt.ReceiptID, instance.Head, bundleDigest}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "capsule-" + hex.EncodeToString(sum[:16])
}

func boundedRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func remoteManifestDigest(instance control.Instance, opts capsulePromoteOptions, stored record.Stored) string {
	identity := strings.Join([]string{instance.ID, fmt.Sprint(instance.Generation), opts.TargetRef, opts.RemoteAdmission.TargetBaseSHA, opts.RemoteAdmission.TrainID, stored.Receipt.ReceiptID, instance.Head}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fullGitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func remoteCandidateTerminal(status queue.Status) bool {
	return status == queue.Landed || status == queue.Rejected
}
