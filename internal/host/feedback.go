package host

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	feedbackMaxReports      = 200
	feedbackMaxResponseSize = 256 * 1024
	feedbackMaxRefSize      = 180
	feedbackMaxPathSize     = 4096
	feedbackMaxTitleSize    = 256
	feedbackMaxSummarySize  = 64 * 1024
)

// FeedbackScope is authoritative application identity resolved by the server.
// Host arguments cannot select a different application, owner, or revision.
type FeedbackScope struct {
	ApplicationID string
	Owner         string
	Revision      string
}

// ReviewedFeedbackReport is the backend's privacy-reviewed projection. Owner
// is used only to verify the server scope and is never returned to the caller.
type ReviewedFeedbackReport struct {
	Ref        string
	Kind       string
	Title      string
	Summary    string
	Producer   string
	Reviewed   bool
	AppID      string
	Owner      string
	Revision   string
	ReceiptRef string
	ReviewedAt time.Time
}

// FeedbackListRequest carries the fixed server scope and the caller's bounded
// selector to the reviewed-report store.
type FeedbackListRequest struct {
	Scope    FeedbackScope
	Actor    string
	Selector string
	Limit    int
}

// FeedbackDispatchRequest carries an authenticated, explicitly idempotent
// request to a governed launcher.
type FeedbackDispatchRequest struct {
	Scope           FeedbackScope
	Actor           string
	ReportRef       string
	DispatchID      string
	ResumeMode      string
	ResumeWorkspace string
	RetryBrief      string
}

// FeedbackDispatchReceipt identifies the durable job created or recovered by
// an idempotent dispatch.
type FeedbackDispatchReceipt struct {
	JobID string
}

// FeedbackBackend is the governed daemon integration seam. Dispatch
// implementations must resolve ReportRef to a reviewed report within Scope,
// deduplicate by DispatchID, and validate workspace and retry-brief references
// against server-owned managed roots.
type FeedbackBackend interface {
	ListReviewed(context.Context, FeedbackListRequest) ([]ReviewedFeedbackReport, error)
	Dispatch(context.Context, FeedbackDispatchRequest) (FeedbackDispatchReceipt, error)
}

// NewFeedbackHandler returns an authenticated, application-scoped feedback
// provider. The backend is injected; no repository, story, or launcher path is
// inferred by this package.
func NewFeedbackHandler(backend FeedbackBackend, scope FeedbackScope) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if backend == nil {
			return Result{Error: "host.feedback: backing service is unavailable"}, nil
		}
		if err := validateFeedbackScope(scope); err != nil {
			return Result{Error: "host.feedback: " + err.Error()}, nil
		}
		actor := strings.TrimSpace(ActorFromContext(ctx))
		if actor == "" {
			return Result{Error: "host.feedback: authenticated actor is required"}, nil
		}

		op, _ := args["op"].(string)
		switch strings.TrimSpace(op) {
		case "list_reviewed":
			return feedbackListReviewed(ctx, backend, scope, actor, args)
		case "dispatch":
			return feedbackDispatch(ctx, backend, scope, actor, args)
		default:
			return Result{}, fmt.Errorf("host.feedback: unknown op %q", op)
		}
	}
}

// FeedbackHandler is the fail-closed builtin sentinel. Daemon session
// construction replaces it only when that application has a registered
// backend.
var FeedbackHandler = NewFeedbackHandler(nil, FeedbackScope{})

func feedbackListReviewed(
	ctx context.Context,
	backend FeedbackBackend,
	scope FeedbackScope,
	actor string,
	args map[string]any,
) (Result, error) {
	selector, err := requiredFeedbackReference(args, "scope", 128)
	if err != nil {
		return Result{}, err
	}
	limit, err := requiredFeedbackLimit(args, "limit")
	if err != nil {
		return Result{}, err
	}

	reports, err := backend.ListReviewed(ctx, FeedbackListRequest{
		Scope: scope, Actor: actor, Selector: selector, Limit: limit + 1,
	})
	if err != nil {
		return Result{}, fmt.Errorf("host.feedback.list_reviewed: %w", err)
	}
	if len(reports) > limit {
		return Result{}, fmt.Errorf(
			"host.feedback.list_reviewed: selected more than %d reports; refusing to truncate",
			limit,
		)
	}
	seen := make(map[string]struct{}, len(reports))
	for _, report := range reports {
		if err := validateReviewedFeedbackReport(report, scope); err != nil {
			return Result{}, fmt.Errorf("host.feedback.list_reviewed: %w", err)
		}
		if _, duplicate := seen[report.Ref]; duplicate {
			return Result{}, fmt.Errorf(
				"host.feedback.list_reviewed: backend returned duplicate report ref %q",
				report.Ref,
			)
		}
		seen[report.Ref] = struct{}{}
	}
	sort.Slice(reports, func(i, j int) bool {
		if !reports[i].ReviewedAt.Equal(reports[j].ReviewedAt) {
			return reports[i].ReviewedAt.After(reports[j].ReviewedAt)
		}
		return reports[i].Ref < reports[j].Ref
	})

	rows := make([]any, 0, len(reports))
	for _, report := range reports {
		rows = append(rows, map[string]any{
			"ref":      report.Ref,
			"kind":     report.Kind,
			"title":    report.Title,
			"summary":  report.Summary,
			"producer": report.Producer,
			"reviewed": true,
			"source": map[string]any{
				"application_id": report.AppID,
				"revision":       report.Revision,
			},
			"receipt": map[string]any{
				"ref":    report.ReceiptRef,
				"status": "reviewed",
			},
			"reviewed_at": report.ReviewedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	data := map[string]any{"reports": rows, "revision": scope.Revision}
	encoded, err := json.Marshal(data)
	if err != nil {
		return Result{}, fmt.Errorf("host.feedback.list_reviewed: encode size check: %w", err)
	}
	if len(encoded) > feedbackMaxResponseSize {
		return Result{}, fmt.Errorf(
			"host.feedback.list_reviewed: encoded response is %d bytes, exceeds %d; refusing to truncate",
			len(encoded), feedbackMaxResponseSize,
		)
	}
	return Result{Data: data}, nil
}

func feedbackDispatch(
	ctx context.Context,
	backend FeedbackBackend,
	scope FeedbackScope,
	actor string,
	args map[string]any,
) (Result, error) {
	reportRef, err := requiredFeedbackReference(args, "report_ref", feedbackMaxRefSize)
	if err != nil {
		return Result{}, err
	}
	dispatchID, err := requiredFeedbackToken(args, "dispatch_id", feedbackMaxRefSize)
	if err != nil {
		return Result{}, err
	}
	resumeMode, err := requiredFeedbackString(args, "resume_mode", 32, false)
	if err != nil {
		return Result{}, err
	}
	switch resumeMode {
	case "fresh", "reused-workspace", "resumed-session":
	default:
		return Result{}, fmt.Errorf("host.feedback.dispatch: unsupported resume_mode %q", resumeMode)
	}
	resumeWorkspace, err := requiredFeedbackLocator(args, "resume_workspace")
	if err != nil {
		return Result{}, err
	}
	retryBrief, err := requiredFeedbackLocator(args, "retry_brief")
	if err != nil {
		return Result{}, err
	}
	if resumeMode == "fresh" && resumeWorkspace != "" {
		return Result{}, fmt.Errorf("host.feedback.dispatch: resume_workspace must be empty for fresh dispatch")
	}
	if resumeMode != "fresh" && resumeWorkspace == "" {
		return Result{}, fmt.Errorf("host.feedback.dispatch: resume_workspace is required for %s dispatch", resumeMode)
	}

	receipt, err := backend.Dispatch(ctx, FeedbackDispatchRequest{
		Scope:           scope,
		Actor:           actor,
		ReportRef:       reportRef,
		DispatchID:      dispatchID,
		ResumeMode:      resumeMode,
		ResumeWorkspace: resumeWorkspace,
		RetryBrief:      retryBrief,
	})
	if err != nil {
		return Result{}, fmt.Errorf("host.feedback.dispatch: %w", err)
	}
	if !validFeedbackToken(receipt.JobID, feedbackMaxRefSize) {
		return Result{}, fmt.Errorf("host.feedback.dispatch: backend returned an unsafe job id")
	}
	return Result{Data: map[string]any{"job_id": receipt.JobID}}, nil
}

func validateFeedbackScope(scope FeedbackScope) error {
	switch {
	case !validFeedbackToken(scope.ApplicationID, 128):
		return fmt.Errorf("server could not resolve a privacy-safe application id")
	case strings.TrimSpace(scope.Owner) == "":
		return fmt.Errorf("server could not resolve the application owner")
	case !validFeedbackToken(scope.Revision, 128):
		return fmt.Errorf("server could not resolve a privacy-safe application revision")
	default:
		return nil
	}
}

func validateReviewedFeedbackReport(report ReviewedFeedbackReport, scope FeedbackScope) error {
	switch {
	case !report.Reviewed:
		return fmt.Errorf("backend returned unreviewed report %q", report.Ref)
	case report.AppID != scope.ApplicationID || report.Owner != scope.Owner:
		return fmt.Errorf("backend returned report %q outside the resolved scope", report.Ref)
	case !validFeedbackReference(report.Ref, feedbackMaxRefSize):
		return fmt.Errorf("backend returned an unsafe report ref")
	case !validFeedbackToken(report.Kind, 64):
		return fmt.Errorf("backend returned unsafe kind for report %q", report.Ref)
	case report.Title != "" && !validFeedbackText(report.Title, feedbackMaxTitleSize):
		return fmt.Errorf("backend returned unsafe title for report %q", report.Ref)
	case !validFeedbackText(report.Summary, feedbackMaxSummarySize):
		return fmt.Errorf("backend returned unsafe summary for report %q", report.Ref)
	case !validFeedbackToken(report.Producer, 128):
		return fmt.Errorf("backend returned unsafe producer for report %q", report.Ref)
	case !validFeedbackToken(report.Revision, 128):
		return fmt.Errorf("backend returned unsafe revision for report %q", report.Ref)
	case !validFeedbackReference(report.ReceiptRef, feedbackMaxRefSize):
		return fmt.Errorf("backend returned unsafe receipt ref for report %q", report.Ref)
	case report.ReviewedAt.IsZero():
		return fmt.Errorf("backend returned no review time for report %q", report.Ref)
	default:
		return nil
	}
}

func requiredFeedbackLimit(args map[string]any, name string) (int, error) {
	raw, ok := args[name]
	if !ok {
		return 0, fmt.Errorf("host.feedback.list_reviewed: missing required arg %q", name)
	}
	var value int
	switch typed := raw.(type) {
	case int:
		value = typed
	case int64:
		if int64(int(typed)) != typed {
			return 0, fmt.Errorf("host.feedback.list_reviewed: %q is outside the supported integer range", name)
		}
		value = int(typed)
	case float64:
		if math.Trunc(typed) != typed || typed > float64(math.MaxInt) || typed < float64(math.MinInt) {
			return 0, fmt.Errorf("host.feedback.list_reviewed: %q must be an integer", name)
		}
		value = int(typed)
	default:
		return 0, fmt.Errorf("host.feedback.list_reviewed: %q must be an integer, got %T", name, raw)
	}
	if value < 1 || value > feedbackMaxReports {
		return 0, fmt.Errorf(
			"host.feedback.list_reviewed: %q must be between 1 and %d, got %d",
			name, feedbackMaxReports, value,
		)
	}
	return value, nil
}

func requiredFeedbackReference(args map[string]any, name string, max int) (string, error) {
	value, err := requiredFeedbackString(args, name, max, false)
	if err != nil {
		return "", err
	}
	if !validFeedbackReference(value, max) {
		return "", fmt.Errorf("host.feedback: %q is not a safe opaque reference", name)
	}
	return value, nil
}

func requiredFeedbackToken(args map[string]any, name string, max int) (string, error) {
	value, err := requiredFeedbackString(args, name, max, false)
	if err != nil {
		return "", err
	}
	if !validFeedbackToken(value, max) {
		return "", fmt.Errorf("host.feedback: %q is not a safe opaque identifier", name)
	}
	return value, nil
}

func requiredFeedbackString(args map[string]any, name string, max int, allowEmpty bool) (string, error) {
	raw, ok := args[name]
	if !ok {
		return "", fmt.Errorf("host.feedback: missing required arg %q", name)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("host.feedback: %q must be a string, got %T", name, raw)
	}
	value = strings.TrimSpace(value)
	if value == "" && !allowEmpty {
		return "", fmt.Errorf("host.feedback: %q must not be empty", name)
	}
	if len(value) > max || !validFeedbackText(value, max) {
		return "", fmt.Errorf("host.feedback: %q exceeds its safe text boundary", name)
	}
	return value, nil
}

func requiredFeedbackLocator(args map[string]any, name string) (string, error) {
	value, err := requiredFeedbackString(args, name, feedbackMaxPathSize, true)
	if err != nil {
		return "", err
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("host.feedback: %q exceeds its safe text boundary", name)
		}
	}
	return value, nil
}

func validFeedbackReference(value string, max int) bool {
	if value == "" || len(value) > max || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." ||
			!validFeedbackToken(segment, max) {
			return false
		}
	}
	return true
}

func validFeedbackToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '+', r == ':', r == '@':
		default:
			return false
		}
	}
	return true
}

func validFeedbackText(value string, max int) bool {
	if len(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
