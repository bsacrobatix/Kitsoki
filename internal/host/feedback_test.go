package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

type feedbackBackendStub struct {
	listFn     func(context.Context, FeedbackListRequest) ([]ReviewedFeedbackReport, error)
	dispatchFn func(context.Context, FeedbackDispatchRequest) (FeedbackDispatchReceipt, error)
}

func (s feedbackBackendStub) ListReviewed(
	ctx context.Context,
	request FeedbackListRequest,
) ([]ReviewedFeedbackReport, error) {
	return s.listFn(ctx, request)
}

func (s feedbackBackendStub) Dispatch(
	ctx context.Context,
	request FeedbackDispatchRequest,
) (FeedbackDispatchReceipt, error) {
	return s.dispatchFn(ctx, request)
}

func TestFeedbackListReviewedIsScopedPrivateBoundedAndDeterministic(t *testing.T) {
	scope := FeedbackScope{ApplicationID: "review-app", Owner: "private-owner", Revision: "1.2.3"}
	now := time.Date(2026, 7, 26, 3, 4, 5, 6, time.UTC)
	var gotRequest FeedbackListRequest
	backend := feedbackBackendStub{
		listFn: func(_ context.Context, request FeedbackListRequest) ([]ReviewedFeedbackReport, error) {
			gotRequest = request
			return []ReviewedFeedbackReport{
				reviewedReport(scope, "feedback/ref-b", now),
				reviewedReport(scope, "feedback/ref-a", now),
			}, nil
		},
		dispatchFn: unexpectedFeedbackDispatch(t),
	}
	handler := NewFeedbackHandler(backend, scope)
	ctx := WithActor(context.Background(), "private-actor")
	args := map[string]any{"op": "list_reviewed", "scope": "current", "limit": 2}

	first, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("list reviewed: %v", err)
	}
	second, err := handler(ctx, args)
	if err != nil {
		t.Fatalf("list reviewed second call: %v", err)
	}
	if gotRequest.Scope != scope || gotRequest.Actor != "private-actor" ||
		gotRequest.Selector != "current" || gotRequest.Limit != 3 {
		t.Fatalf("backend request = %#v", gotRequest)
	}
	firstJSON, err := json.Marshal(first.Data)
	if err != nil {
		t.Fatalf("marshal first response: %v", err)
	}
	secondJSON, err := json.Marshal(second.Data)
	if err != nil {
		t.Fatalf("marshal second response: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("response changed without a backend change:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
	for _, secret := range []string{"private-owner", "private-actor", "/private/storage", "source-repository"} {
		if strings.Contains(string(firstJSON), secret) {
			t.Fatalf("response leaked %q: %s", secret, firstJSON)
		}
	}

	if got := first.Data["revision"]; got != scope.Revision {
		t.Fatalf("revision = %v", got)
	}
	rows := first.Data["reports"].([]any)
	if len(rows) != 2 ||
		rows[0].(map[string]any)["ref"] != "feedback/ref-a" ||
		rows[1].(map[string]any)["ref"] != "feedback/ref-b" {
		t.Fatalf("reports = %#v", rows)
	}
	row := rows[0].(map[string]any)
	source := row["source"].(map[string]any)
	if source["application_id"] != scope.ApplicationID || source["revision"] != scope.Revision {
		t.Fatalf("source = %#v", source)
	}
	receipt := row["receipt"].(map[string]any)
	if receipt["ref"] != "receipt:feedback/ref-a" || receipt["status"] != "reviewed" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestFeedbackListReviewedRefusesToTruncateRowsOrBytes(t *testing.T) {
	scope := FeedbackScope{ApplicationID: "app", Owner: "owner", Revision: "1"}
	now := time.Unix(1, 0).UTC()
	reports := []ReviewedFeedbackReport{
		reviewedReport(scope, "feedback/a", now),
		reviewedReport(scope, "feedback/b", now),
	}
	handler := NewFeedbackHandler(feedbackBackendStub{
		listFn: func(context.Context, FeedbackListRequest) ([]ReviewedFeedbackReport, error) {
			return reports, nil
		},
		dispatchFn: unexpectedFeedbackDispatch(t),
	}, scope)
	ctx := WithActor(context.Background(), "actor")

	_, err := handler(ctx, map[string]any{"op": "list_reviewed", "scope": "current", "limit": 1})
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("row overflow error = %v", err)
	}

	large := make([]ReviewedFeedbackReport, 5)
	for i := range large {
		large[i] = reviewedReport(scope, "feedback/"+string(rune('a'+i)), now)
		large[i].Summary = strings.Repeat("x", feedbackMaxSummarySize)
	}
	handler = NewFeedbackHandler(feedbackBackendStub{
		listFn: func(context.Context, FeedbackListRequest) ([]ReviewedFeedbackReport, error) {
			return large, nil
		},
		dispatchFn: unexpectedFeedbackDispatch(t),
	}, scope)
	_, err = handler(ctx, map[string]any{"op": "list_reviewed", "scope": "current", "limit": 5})
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("byte overflow error = %v", err)
	}
}

func TestFeedbackListReviewedRejectsUnreviewedOrOutOfScopeData(t *testing.T) {
	scope := FeedbackScope{ApplicationID: "app", Owner: "owner", Revision: "1"}
	tests := []struct {
		name   string
		mutate func(*ReviewedFeedbackReport)
		want   string
	}{
		{name: "unreviewed", mutate: func(report *ReviewedFeedbackReport) {
			report.Reviewed = false
		}, want: "unreviewed"},
		{name: "wrong owner", mutate: func(report *ReviewedFeedbackReport) {
			report.Owner = "other"
		}, want: "outside the resolved scope"},
		{name: "unsafe ref", mutate: func(report *ReviewedFeedbackReport) {
			report.Ref = "../private"
		}, want: "unsafe report ref"},
		{name: "control text", mutate: func(report *ReviewedFeedbackReport) {
			report.Summary = "reviewed\x00secret"
		}, want: "unsafe summary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := reviewedReport(scope, "feedback/ref", time.Unix(1, 0).UTC())
			test.mutate(&report)
			handler := NewFeedbackHandler(feedbackBackendStub{
				listFn: func(context.Context, FeedbackListRequest) ([]ReviewedFeedbackReport, error) {
					return []ReviewedFeedbackReport{report}, nil
				},
				dispatchFn: unexpectedFeedbackDispatch(t),
			}, scope)
			_, err := handler(
				WithActor(context.Background(), "actor"),
				map[string]any{"op": "list_reviewed", "scope": "current", "limit": 1},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFeedbackDispatchPropagatesAuthenticatedScopeAndIdempotency(t *testing.T) {
	scope := FeedbackScope{ApplicationID: "app", Owner: "owner", Revision: "1"}
	launches := 0
	jobs := map[string]string{}
	var requests []FeedbackDispatchRequest
	handler := NewFeedbackHandler(feedbackBackendStub{
		listFn: unexpectedFeedbackList(t),
		dispatchFn: func(_ context.Context, request FeedbackDispatchRequest) (FeedbackDispatchReceipt, error) {
			requests = append(requests, request)
			jobID, ok := jobs[request.DispatchID]
			if !ok {
				launches++
				jobID = "feedback-job-1"
				jobs[request.DispatchID] = jobID
			}
			return FeedbackDispatchReceipt{
				JobID: jobID,
				Receipts: []appplatform.Receipt{
					canonicalFeedbackReceipt(t, request),
				},
			}, nil
		},
	}, scope)
	args := map[string]any{
		"op":               "dispatch",
		"report_ref":       "feedback/ref:1",
		"dispatch_id":      "dispatch:1",
		"resume_mode":      "reused-workspace",
		"resume_workspace": "/managed/workspace/one",
		"retry_brief":      "/managed/artifacts/retry.md",
	}
	ctx := WithActor(context.Background(), "operator@example.test")
	for i := 0; i < 2; i++ {
		result, err := handler(ctx, args)
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		if got := result.Data["job_id"]; got != "feedback-job-1" {
			t.Fatalf("job_id = %v", got)
		}
	}
	if launches != 1 || len(requests) != 2 {
		t.Fatalf("launches = %d, requests = %d", launches, len(requests))
	}
	want := FeedbackDispatchRequest{
		Scope: scope, Actor: "operator@example.test",
		ReportRef: "feedback/ref:1", DispatchID: "dispatch:1",
		ResumeMode: "reused-workspace", ResumeWorkspace: "/managed/workspace/one",
		RetryBrief: "/managed/artifacts/retry.md",
	}
	if requests[0] != want {
		t.Fatalf("dispatch request = %#v, want %#v", requests[0], want)
	}
}

func TestFeedbackFailsClosedAndRejectsInvalidDispatch(t *testing.T) {
	scope := FeedbackScope{ApplicationID: "app", Owner: "owner", Revision: "1"}
	backend := feedbackBackendStub{
		listFn: unexpectedFeedbackList(t),
		dispatchFn: func(context.Context, FeedbackDispatchRequest) (FeedbackDispatchReceipt, error) {
			return FeedbackDispatchReceipt{JobID: "job"}, nil
		},
	}
	args := map[string]any{
		"op": "dispatch", "report_ref": "feedback/ref", "dispatch_id": "dispatch-1",
		"resume_mode": "fresh", "resume_workspace": "", "retry_brief": "",
	}
	result, err := FeedbackHandler(WithActor(context.Background(), "actor"), args)
	if err != nil || !strings.Contains(result.Error, "unavailable") {
		t.Fatalf("unavailable result = %#v, %v", result, err)
	}
	result, err = NewFeedbackHandler(backend, scope)(context.Background(), args)
	if err != nil || !strings.Contains(result.Error, "authenticated actor") {
		t.Fatalf("anonymous result = %#v, %v", result, err)
	}

	tests := []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{name: "unsafe dispatch id", change: func(args map[string]any) {
			args["dispatch_id"] = "../dispatch"
		}, want: "safe opaque identifier"},
		{name: "unknown mode", change: func(args map[string]any) {
			args["resume_mode"] = "attach-anything"
		}, want: "unsupported resume_mode"},
		{name: "fresh workspace", change: func(args map[string]any) {
			args["resume_workspace"] = "/managed/workspace"
		}, want: "must be empty"},
		{name: "resume without workspace", change: func(args map[string]any) {
			args["resume_mode"] = "resumed-session"
		}, want: "resume_workspace is required"},
		{name: "control in brief", change: func(args map[string]any) {
			args["retry_brief"] = "secret\x00path"
		}, want: "safe text boundary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testArgs := make(map[string]any, len(args))
			for name, value := range args {
				testArgs[name] = value
			}
			test.change(testArgs)
			_, err := NewFeedbackHandler(backend, scope)(
				WithActor(context.Background(), "actor"),
				testArgs,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFeedbackBackendFailureAndUnsafeReceiptFailClosed(t *testing.T) {
	scope := FeedbackScope{ApplicationID: "app", Owner: "owner", Revision: "1"}
	args := map[string]any{
		"op": "dispatch", "report_ref": "feedback/ref", "dispatch_id": "dispatch-1",
		"resume_mode": "fresh", "resume_workspace": "", "retry_brief": "",
	}
	handler := NewFeedbackHandler(feedbackBackendStub{
		listFn: unexpectedFeedbackList(t),
		dispatchFn: func(context.Context, FeedbackDispatchRequest) (FeedbackDispatchReceipt, error) {
			return FeedbackDispatchReceipt{}, errors.New("store unavailable")
		},
	}, scope)
	_, err := handler(WithActor(context.Background(), "actor"), args)
	if err == nil || !strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("backend error = %v", err)
	}

	handler = NewFeedbackHandler(feedbackBackendStub{
		listFn: unexpectedFeedbackList(t),
		dispatchFn: func(context.Context, FeedbackDispatchRequest) (FeedbackDispatchReceipt, error) {
			return FeedbackDispatchReceipt{JobID: "../private"}, nil
		},
	}, scope)
	_, err = handler(WithActor(context.Background(), "actor"), args)
	if err == nil || !strings.Contains(err.Error(), "unsafe job id") {
		t.Fatalf("unsafe receipt error = %v", err)
	}

	handler = NewFeedbackHandler(feedbackBackendStub{
		listFn: unexpectedFeedbackList(t),
		dispatchFn: func(_ context.Context, request FeedbackDispatchRequest) (FeedbackDispatchReceipt, error) {
			receipt := canonicalFeedbackReceipt(t, request)
			receipt.Actor = "forged"
			return FeedbackDispatchReceipt{
				JobID: "job", Receipts: []appplatform.Receipt{receipt},
			}, nil
		},
	}, scope)
	_, err = handler(WithActor(context.Background(), "actor"), args)
	if err == nil || !strings.Contains(err.Error(), "forged canonical") {
		t.Fatalf("forged receipt error = %v", err)
	}
}

func TestFeedbackRegistrationSchemaAndEffect(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	result, err := registry.Invoke(
		WithActor(context.Background(), "actor"),
		"host.feedback.list_reviewed",
		map[string]any{"scope": "current", "limit": 1},
	)
	if err != nil {
		t.Fatalf("invoke builtin: %v", err)
	}
	if !strings.Contains(result.Error, "unavailable") {
		t.Fatalf("builtin error = %q", result.Error)
	}

	class, deterministic := ClassifyDispatchedCall(
		"host.feedback",
		map[string]any{"op": "list_reviewed"},
	)
	if class != effect.Read || !deterministic {
		t.Fatalf("list classification = (%q, %v)", class, deterministic)
	}
	class, deterministic = ClassifyDispatchedCall("host.feedback.list_reviewed", nil)
	if class != effect.Read || !deterministic {
		t.Fatalf("list leaf classification = (%q, %v)", class, deterministic)
	}
	class, deterministic = ClassifyDispatchedCall("host.feedback.dispatch", nil)
	if class != effect.External || deterministic {
		t.Fatalf("dispatch leaf classification = (%q, %v)", class, deterministic)
	}

	schemas := opschema.Builtins()
	list, ok := schemas.Lookup("host.feedback", "list_reviewed")
	if !ok || list.Input["scope"].Type != "string" || list.Input["limit"].Type != "int" ||
		list.Output["reports"].Type != "list" || list.Output["revision"].Type != "string" {
		t.Fatalf("list opschema = %#v, %v", list, ok)
	}
	dispatch, ok := schemas.Lookup("host.feedback", "dispatch")
	if !ok || dispatch.Input["dispatch_id"].Type != "string" ||
		dispatch.Input["resume_workspace"].Type != "string" ||
		dispatch.Output["job_id"].Type != "string" ||
		dispatch.Output["receipts"].Type != "list" {
		t.Fatalf("dispatch opschema = %#v, %v", dispatch, ok)
	}
}

func canonicalFeedbackReceipt(
	t *testing.T,
	request FeedbackDispatchRequest,
) appplatform.Receipt {
	t.Helper()
	receipt, err := appplatform.FinalizeReceipt(appplatform.Receipt{
		HandlerID: "feedback.apply", SemanticRef: "feedback.apply",
		SessionID: "feedback-job-1", Actor: request.Actor,
		Effect: appplatform.EffectWrite,
		Routing: appplatform.RoutingReceipt{
			Requested: appplatform.RoutingExact,
			Resolved:  appplatform.RoutingExact,
		},
		Budget: appplatform.BudgetDecision{
			Allowed: true, Code: "not_applicable",
		},
		IdempotencyKey: request.DispatchID,
		InputDigest:    "sha256:input",
		OutputDigest:   "sha256:output",
		Transport:      appplatform.TransportEvent,
		Outcome:        "ok",
	})
	if err != nil {
		t.Fatalf("finalize canonical receipt: %v", err)
	}
	return receipt
}

func reviewedReport(scope FeedbackScope, ref string, reviewedAt time.Time) ReviewedFeedbackReport {
	return ReviewedFeedbackReport{
		Ref: ref, Kind: "bug", Title: "Reviewed title",
		Summary: "Reviewed summary", Producer: "feedback-intake",
		Reviewed: true, AppID: scope.ApplicationID, Owner: scope.Owner,
		Revision: scope.Revision, ReceiptRef: "receipt:" + ref,
		ReviewedAt: reviewedAt,
	}
}

func unexpectedFeedbackList(t *testing.T) func(
	context.Context,
	FeedbackListRequest,
) ([]ReviewedFeedbackReport, error) {
	t.Helper()
	return func(context.Context, FeedbackListRequest) ([]ReviewedFeedbackReport, error) {
		t.Fatal("unexpected ListReviewed call")
		return nil, nil
	}
}

func unexpectedFeedbackDispatch(t *testing.T) func(
	context.Context,
	FeedbackDispatchRequest,
) (FeedbackDispatchReceipt, error) {
	t.Helper()
	return func(context.Context, FeedbackDispatchRequest) (FeedbackDispatchReceipt, error) {
		t.Fatal("unexpected Dispatch call")
		return FeedbackDispatchReceipt{}, nil
	}
}
