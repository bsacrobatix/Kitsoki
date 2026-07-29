package applicationjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/artifactjob"
)

var (
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	jobRefPattern   = regexp.MustCompile(`^aj_[0-9a-f]{32}$`)
	handlePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}:[A-Za-z0-9._~-]+$`)
)

// Backend adapts the existing Application Event and per-session scheduler
// surfaces. It does not introduce a second execution queue.
type Backend interface {
	Dispatch(context.Context, DispatchRequest) (DispatchResult, error)
	Status(context.Context, Record) (ChildSnapshot, bool, error)
	Cancel(context.Context, Record) error
}

// Service owns public job identity, replay, bounds, and result projection.
type Service struct {
	Records   Store
	Jobs      artifactjob.Store
	Backend   Backend
	Templates map[string]map[string]Template
	Now       func() time.Time

	// ScheduleAfter is injectable so runtime-bound behavior has deterministic
	// tests. The default delegates to time.AfterFunc.
	ScheduleAfter func(time.Duration, func())

	mu sync.Mutex
}

func ValidateTemplate(name string, template Template) error {
	if err := boundedIdentity("template", name); err != nil {
		return err
	}
	if err := boundedIdentity("application_id", template.ApplicationID); err != nil {
		return err
	}
	if err := boundedIdentity("event", template.Event); err != nil {
		return err
	}
	if len(template.ArtifactOutputs) == 0 || len(template.ArtifactOutputs) > MaxArtifactOutputs {
		return fmt.Errorf("application job template %q artifact_outputs must be within 1..%d", name, MaxArtifactOutputs)
	}
	outputs := make(map[string]struct{}, len(template.ArtifactOutputs))
	for _, output := range template.ArtifactOutputs {
		if err := boundedIdentity("artifact output", output); err != nil {
			return err
		}
		if _, exists := outputs[output]; exists {
			return fmt.Errorf("application job template %q duplicates artifact output %q", name, output)
		}
		outputs[output] = struct{}{}
	}
	if _, ok := outputs[template.PrimaryOutput]; !ok {
		return fmt.Errorf("application job template %q primary_output must name an artifact output", name)
	}
	if template.Bounds.MaxInputBytes <= 0 || template.Bounds.MaxInputBytes > MaxInputBytes {
		return fmt.Errorf("application job template %q max_input_bytes must be within 1..%d", name, MaxInputBytes)
	}
	if template.Bounds.MaxRuntimeSeconds <= 0 || template.Bounds.MaxRuntimeSeconds > MaxRuntimeSeconds {
		return fmt.Errorf(
			"application job template %q max_runtime_seconds must be within 1..%d",
			name,
			MaxRuntimeSeconds,
		)
	}
	return nil
}

func (s *Service) Submit(
	ctx context.Context,
	callerApplicationID string,
	templateID string,
	input json.RawMessage,
) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Result{}, err
	}
	template, err := s.template(callerApplicationID, templateID)
	if err != nil {
		return Result{}, err
	}
	normalized, err := validatePublicInput(input, template.Bounds.MaxInputBytes)
	if err != nil {
		return Result{}, err
	}
	inputDigest, err := appplatform.DigestJSON(normalized)
	if err != nil {
		return Result{}, fmt.Errorf("application job input digest: %w", err)
	}
	jobRef := stableJobRef(callerApplicationID, templateID, inputDigest)
	if existing, getErr := s.Jobs.Get(ctx, artifactjob.JobID(jobRef)); getErr == nil {
		record, recordErr := s.Records.Get(ctx, jobRef)
		if recordErr != nil || record.CallerApplicationID != callerApplicationID ||
			record.TemplateID != templateID || record.InputDigest != inputDigest {
			return Result{}, fmt.Errorf("application job replay identity conflicts with durable state")
		}
		return s.statusLocked(ctx, existing, record, "submit", true)
	} else if !errors.Is(getErr, artifactjob.ErrNotFound) {
		return Result{}, fmt.Errorf("application job replay lookup failed: %w", getErr)
	}

	job, err := s.Jobs.Register(ctx, artifactjob.RegisterRequest{
		ID:         artifactjob.JobID(jobRef),
		AppID:      callerApplicationID,
		Origin:     artifactjob.Origin{Kind: "application-job", Ref: "application-job:" + callerApplicationID + ":" + templateID},
		Status:     artifactjob.StatusRunning,
		Summary:    "Story Application background job",
		Phase:      templateID,
		Visibility: artifactjob.VisibilityPrivate,
		Owner:      "application-job:" + callerApplicationID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("register application job: %w", err)
	}
	// The registered record is intentionally discarded: BindChild below returns
	// the authoritative one once the child dispatch is known.
	_, err = s.Records.Register(ctx, Record{
		JobRef:              jobRef,
		CallerApplicationID: callerApplicationID,
		TemplateID:          templateID,
		TargetApplicationID: template.ApplicationID,
		TargetEvent:         template.Event,
		ArtifactOutputs:     append([]string(nil), template.ArtifactOutputs...),
		PrimaryOutput:       template.PrimaryOutput,
		MaxInputBytes:       template.Bounds.MaxInputBytes,
		MaxRuntimeSeconds:   template.Bounds.MaxRuntimeSeconds,
		InputDigest:         inputDigest,
		CreatedAt:           job.CreatedAt,
	})
	if err != nil {
		s.fail(ctx, jobRef, "mapping_unavailable")
		return Result{}, fmt.Errorf("persist application job mapping: %w", err)
	}
	dispatched, err := s.Backend.Dispatch(ctx, DispatchRequest{
		JobRef:              jobRef,
		CallerApplicationID: callerApplicationID,
		TemplateID:          templateID,
		Template:            template,
		Input:               normalized,
	})
	if err != nil || dispatched.RouteID == "" || dispatched.ChildID == "" {
		s.fail(ctx, jobRef, "dispatch_failed")
		if err != nil {
			return Result{}, fmt.Errorf("application job dispatch failed: %w", err)
		}
		// Backend reported success but returned an unusable binding; name which
		// half is missing so the failure is diagnosable without the backend log.
		return Result{}, fmt.Errorf(
			"application job dispatch failed: backend returned route_id=%q child_id=%q",
			dispatched.RouteID, dispatched.ChildID,
		)
	}
	record, err := s.Records.BindChild(ctx, jobRef, dispatched)
	if err != nil {
		_ = s.Backend.Cancel(ctx, Record{
			TargetRouteID: dispatched.RouteID, TargetSessionID: dispatched.SessionID,
			ChildJobID: dispatched.ChildID,
		})
		s.fail(ctx, jobRef, "mapping_unavailable")
		return Result{}, fmt.Errorf("persist application job child mapping: %w", err)
	}
	s.scheduleRuntimeBound(record)
	return s.result(job, record, "submit", false, ""), nil
}

func (s *Service) Status(ctx context.Context, callerApplicationID, jobRef string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Result{}, err
	}
	job, record, err := s.loadOwned(ctx, callerApplicationID, jobRef)
	if err != nil {
		return Result{}, err
	}
	return s.statusLocked(ctx, job, record, "status", false)
}

func (s *Service) Cancel(ctx context.Context, callerApplicationID, jobRef string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Result{}, err
	}
	job, record, err := s.loadOwned(ctx, callerApplicationID, jobRef)
	if err != nil {
		return Result{}, err
	}
	job, record, err = s.refreshLocked(ctx, job, record)
	if err != nil {
		return Result{}, err
	}
	if terminal(job.Status) {
		return s.result(job, record, "cancel", true, publicReason(job)), nil
	}
	if err := s.Backend.Cancel(ctx, record); err != nil {
		return Result{}, fmt.Errorf("cancel application job: %w", err)
	}
	status := artifactjob.StatusCancelled
	finished := s.now().UTC()
	job, err = s.Jobs.Update(ctx, job.ID, artifactjob.Update{
		Status: &status, FinishedAt: &finished,
	})
	if err != nil {
		return Result{}, fmt.Errorf("persist application job cancellation: %w", err)
	}
	return s.result(job, record, "cancel", false, ""), nil
}

func (s *Service) statusLocked(
	ctx context.Context,
	job artifactjob.Job,
	record Record,
	operation string,
	replayed bool,
) (Result, error) {
	job, record, err := s.refreshLocked(ctx, job, record)
	if err != nil {
		return Result{}, err
	}
	return s.result(job, record, operation, replayed, publicReason(job)), nil
}

func (s *Service) refreshLocked(
	ctx context.Context,
	job artifactjob.Job,
	record Record,
) (artifactjob.Job, Record, error) {
	if terminal(job.Status) {
		return job, record, nil
	}
	if record.ChildJobID == "" {
		s.fail(ctx, record.JobRef, "dispatch_incomplete")
		job, _ = s.Jobs.Get(ctx, job.ID)
		return job, record, nil
	}
	deadline := job.CreatedAt.Add(time.Duration(record.MaxRuntimeSeconds) * time.Second)
	if !s.now().Before(deadline) {
		_ = s.Backend.Cancel(ctx, record)
		s.fail(ctx, record.JobRef, "runtime_bound_exceeded")
		job, _ = s.Jobs.Get(ctx, job.ID)
		return job, record, nil
	}
	child, found, err := s.Backend.Status(ctx, record)
	if err != nil {
		return artifactjob.Job{}, Record{}, fmt.Errorf("load application job status: %w", err)
	}
	if !found {
		status := artifactjob.StatusInterrupted
		reason := "worker_state_unavailable"
		finished := s.now().UTC()
		job, err = s.Jobs.Update(ctx, job.ID, artifactjob.Update{
			Status: &status, InterruptedReason: &reason, FinishedAt: &finished,
		})
		return job, record, err
	}
	switch child.Status {
	case "running":
		if job.Status == artifactjob.StatusAwaitingInput {
			status := artifactjob.StatusRunning
			job, err = s.Jobs.Update(ctx, job.ID, artifactjob.Update{Status: &status})
			return job, record, err
		}
		return job, record, nil
	case "awaiting_input":
		status := artifactjob.StatusAwaitingInput
		job, err = s.Jobs.Update(ctx, job.ID, artifactjob.Update{Status: &status})
		return job, record, err
	case "cancelled":
		status := artifactjob.StatusCancelled
		finished := s.now().UTC()
		job, err = s.Jobs.Update(ctx, job.ID, artifactjob.Update{Status: &status, FinishedAt: &finished})
		return job, record, err
	case "failed":
		s.fail(ctx, record.JobRef, "child_failed")
		job, _ = s.Jobs.Get(ctx, job.ID)
		return job, record, nil
	case "done":
		artifacts, primary, outputErr := projectArtifacts(record, child.Output)
		if outputErr != nil {
			s.fail(ctx, record.JobRef, "invalid_artifact_output")
			job, _ = s.Jobs.Get(ctx, job.ID)
			return job, record, nil
		}
		record, err = s.Records.Complete(ctx, record.JobRef, artifacts, primary)
		if err != nil {
			s.fail(ctx, record.JobRef, "artifact_persistence_failed")
			return artifactjob.Job{}, Record{}, fmt.Errorf("persist application job artifacts: %w", err)
		}
		status := artifactjob.StatusDone
		finished := s.now().UTC()
		job, err = s.Jobs.Update(ctx, job.ID, artifactjob.Update{
			Status: &status, TerminalArtifactHandle: &primary, FinishedAt: &finished,
		})
		return job, record, err
	default:
		return artifactjob.Job{}, Record{}, fmt.Errorf(
			"application job child returned an invalid status %q", child.Status)
	}
}

func (s *Service) loadOwned(
	ctx context.Context,
	callerApplicationID string,
	jobRef string,
) (artifactjob.Job, Record, error) {
	if err := boundedIdentity("caller application id", callerApplicationID); err != nil {
		return artifactjob.Job{}, Record{}, err
	}
	if !jobRefPattern.MatchString(jobRef) {
		return artifactjob.Job{}, Record{}, fmt.Errorf("application job reference is invalid")
	}
	job, err := s.Jobs.Get(ctx, artifactjob.JobID(jobRef))
	if errors.Is(err, artifactjob.ErrNotFound) {
		return artifactjob.Job{}, Record{}, fmt.Errorf("application job not found")
	}
	if err != nil {
		return artifactjob.Job{}, Record{}, fmt.Errorf("load application job: %w", err)
	}
	record, err := s.Records.Get(ctx, jobRef)
	if errors.Is(err, ErrNotFound) {
		return artifactjob.Job{}, Record{}, fmt.Errorf("application job mapping not found")
	}
	if err != nil {
		return artifactjob.Job{}, Record{}, fmt.Errorf("load application job mapping: %w", err)
	}
	if job.AppID != callerApplicationID || record.CallerApplicationID != callerApplicationID {
		return artifactjob.Job{}, Record{}, fmt.Errorf("application job not found")
	}
	return job, record, nil
}

func (s *Service) template(callerApplicationID, templateID string) (Template, error) {
	if err := boundedIdentity("caller application id", callerApplicationID); err != nil {
		return Template{}, err
	}
	if err := boundedIdentity("template", templateID); err != nil {
		return Template{}, err
	}
	templates := s.Templates[callerApplicationID]
	template, ok := templates[templateID]
	if !ok {
		return Template{}, fmt.Errorf("application job template is not configured")
	}
	if err := ValidateTemplate(templateID, template); err != nil {
		return Template{}, err
	}
	return template, nil
}

func (s *Service) ready() error {
	if s == nil || s.Records == nil || s.Jobs == nil || s.Backend == nil {
		return fmt.Errorf("application jobs are unavailable outside daemon mode")
	}
	return nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) scheduleRuntimeBound(record Record) {
	schedule := s.ScheduleAfter
	if schedule == nil {
		schedule = func(delay time.Duration, run func()) {
			time.AfterFunc(delay, run)
		}
	}
	schedule(time.Duration(record.MaxRuntimeSeconds)*time.Second, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		job, err := s.Jobs.Get(ctx, artifactjob.JobID(record.JobRef))
		if err != nil || terminal(job.Status) {
			return
		}
		_ = s.Backend.Cancel(ctx, record)
		s.fail(ctx, record.JobRef, "runtime_bound_exceeded")
	})
}

func (s *Service) fail(ctx context.Context, jobRef, reason string) {
	status := artifactjob.StatusFailed
	finished := s.now().UTC()
	_, _ = s.Jobs.Update(ctx, artifactjob.JobID(jobRef), artifactjob.Update{
		Status: &status, InterruptedReason: &reason, FinishedAt: &finished,
	})
}

func (s *Service) result(
	job artifactjob.Job,
	record Record,
	operation string,
	replayed bool,
	reason string,
) Result {
	result := Result{
		JobRef: string(job.ID), Status: string(job.Status),
		Artifacts: append([]string(nil), record.Artifacts...),
		Primary:   record.PrimaryHandle, Reason: reason,
	}
	result.Receipt = newReceipt(result.JobRef, operation, result.Status, replayed)
	return result
}

func newReceipt(jobRef, operation, status string, replayed bool) Receipt {
	payload := strings.Join([]string{
		ReceiptSchema, jobRef, operation, status, fmt.Sprintf("%t", replayed),
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return Receipt{
		Schema: ReceiptSchema, ID: "ajr_" + hex.EncodeToString(sum[:16]),
		JobRef: jobRef, Operation: operation, Status: status, Replayed: replayed,
	}
}

func stableJobRef(callerApplicationID, templateID, inputDigest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"kitsoki/application-job/v1", callerApplicationID, templateID, inputDigest,
	}, "\x00")))
	return "aj_" + hex.EncodeToString(sum[:16])
}

func projectArtifacts(record Record, output map[string]any) ([]string, string, error) {
	artifacts := make([]string, 0, len(record.ArtifactOutputs))
	byField := make(map[string]string, len(record.ArtifactOutputs))
	for _, field := range record.ArtifactOutputs {
		handle, ok := output[field].(string)
		if !ok || len(handle) > MaxArtifactRefBytes || !handlePattern.MatchString(handle) ||
			strings.Contains(handle, "..") || strings.Contains(handle, "://") ||
			strings.ContainsAny(handle, `/\`) {
			return nil, "", fmt.Errorf("artifact output is not an opaque handle")
		}
		artifacts = append(artifacts, handle)
		byField[field] = handle
	}
	primary := byField[record.PrimaryOutput]
	if primary == "" {
		return nil, "", fmt.Errorf("primary artifact output is missing")
	}
	return artifacts, primary, nil
}

func validatePublicInput(raw json.RawMessage, maxBytes int) (json.RawMessage, error) {
	normalized, err := appplatform.NormalizeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("application job input must be valid JSON: %w", err)
	}
	if len(normalized) > maxBytes {
		return nil, fmt.Errorf("application job input exceeds configured bound")
	}
	var value any
	if err := json.Unmarshal(normalized, &value); err != nil {
		return nil, fmt.Errorf("application job input must be valid JSON")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("application job input must be a JSON object")
	}
	if err := rejectInternalKeys(object, 0); err != nil {
		return nil, err
	}
	return normalized, nil
}

// ValidateEventInput repeats the public input checks at the injected registry
// boundary before a configured event is dispatched.
func ValidateEventInput(raw json.RawMessage, maxBytes int) (json.RawMessage, error) {
	return validatePublicInput(raw, maxBytes)
}

func rejectInternalKeys(value any, depth int) error {
	if depth > 32 {
		return fmt.Errorf("application job input nesting exceeds 32")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := normalizePublicKey(key)
			if forbiddenPublicKey(normalized) {
				return fmt.Errorf("application job input contains reserved key %q", key)
			}
			if err := rejectInternalKeys(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectInternalKeys(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizePublicKey(key string) string {
	var normalized strings.Builder
	var previousLetterOrDigit bool
	var previousLowerOrDigit bool
	for _, current := range key {
		switch {
		case unicode.IsUpper(current):
			if previousLowerOrDigit {
				normalized.WriteByte('_')
			}
			normalized.WriteRune(unicode.ToLower(current))
			previousLetterOrDigit = true
			previousLowerOrDigit = false
		case unicode.IsLower(current) || unicode.IsDigit(current):
			normalized.WriteRune(unicode.ToLower(current))
			previousLetterOrDigit = true
			previousLowerOrDigit = true
		default:
			if previousLetterOrDigit {
				normalized.WriteByte('_')
			}
			previousLetterOrDigit = false
			previousLowerOrDigit = false
		}
	}
	return strings.Trim(normalized.String(), "_")
}

func forbiddenPublicKey(key string) bool {
	if forbiddenInputKeys[key] {
		return true
	}
	for _, part := range strings.Split(key, "_") {
		if forbiddenInputKeys[part] {
			return true
		}
	}
	return false
}

var forbiddenInputKeys = map[string]bool{
	"application_id":   true,
	"event":            true,
	"artifact_outputs": true,
	"primary_output":   true,
	"bounds":           true,
	"child":            true,
	"child_id":         true,
	"child_job":        true,
	"child_job_id":     true,
	"session":          true,
	"session_id":       true,
	"path":             true,
	"story_path":       true,
	"url":              true,
	"command":          true,
	"provider":         true,
	"actor":            true,
	"transport":        true,
}

func boundedIdentity(label, value string) error {
	if value == "" || len(value) > MaxIdentityBytes || !identityPattern.MatchString(value) ||
		strings.Contains(value, "..") || strings.Contains(value, "://") ||
		strings.ContainsAny(value, `/\`) {
		return fmt.Errorf("%s is empty, malformed, or unbounded", label)
	}
	return nil
}

func terminal(status artifactjob.Status) bool {
	switch status {
	case artifactjob.StatusDone, artifactjob.StatusFailed, artifactjob.StatusCancelled,
		artifactjob.StatusInterrupted, artifactjob.StatusArchived:
		return true
	default:
		return false
	}
}

func publicReason(job artifactjob.Job) string {
	switch job.InterruptedReason {
	case "daemon_restarted", "worker_state_unavailable", "runtime_bound_exceeded",
		"dispatch_failed", "dispatch_incomplete", "child_failed",
		"invalid_artifact_output", "artifact_persistence_failed", "mapping_unavailable":
		return job.InterruptedReason
	default:
		return ""
	}
}
