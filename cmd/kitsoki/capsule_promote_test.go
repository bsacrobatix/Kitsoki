package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/admissionserver"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/capsule/reconcile"
	"kitsoki/internal/capsule/record"
	"kitsoki/internal/objectstore"
)

func TestCapsulePromoteExposesExplicitEmergencySkipTestsFlag(t *testing.T) {
	flag := capsulePromoteCmd().Flags().Lookup("skip-tests")
	if flag == nil {
		t.Fatal("capsule promote is missing --skip-tests")
	}
	if flag.DefValue != "false" {
		t.Fatalf("skip-tests default = %q", flag.DefValue)
	}
}

func TestCapsulePromoteRemoteAdmissionFlagsAreExplicit(t *testing.T) {
	cmd := capsulePromoteCmd()
	for _, name := range []string{"remote-admission-url", "remote-admission-token-env", "remote-bucket-url", "remote-target-base-sha", "remote-train", "remote-status-command"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("capsule promote is missing --%s", name)
		}
	}
}

func TestCapsulePromoteRepairRequiresIndependentReviewAndBoundedStages(t *testing.T) {
	cmd := capsulePromoteCmd()
	for _, name := range []string{"repair-review", "repairer-id", "reviewer-id", "review-policy-digest", "gate-timeout", "capacity-root", "capacity-pool", "capacity"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("capsule promote is missing --%s", name)
		}
	}
	base := capsulePromoteOptions{Pipeline: "change", TargetRef: "main", Wait: true, RepairCommand: "repair"}
	for _, mutate := range []func(*capsulePromoteOptions){
		func(*capsulePromoteOptions) {},
		func(o *capsulePromoteOptions) {
			o.RepairReviewCommand, o.RepairerID, o.ReviewerID, o.ReviewPolicyDigest = "review", "same", "same", "sha256:policy"
		},
	} {
		opts := base
		mutate(&opts)
		if _, err := runCapsulePromote(context.Background(), opts); err == nil {
			t.Fatalf("unsafe repair options accepted: %+v", opts)
		}
	}
	opts := base
	opts.Wait = false
	opts.RepairReviewCommand, opts.RepairerID, opts.ReviewerID, opts.ReviewPolicyDigest = "review", "repairer", "reviewer", "sha256:policy"
	if _, err := runCapsulePromote(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--wait") {
		t.Fatalf("repair without wait err=%v", err)
	}
}

func TestPromotionCapacitySerializesFreshCIAndReleases(t *testing.T) {
	capacity := queue.FileGateCapacity{Root: t.TempDir(), Pool: "promotion-ci", Max: 1, PollInterval: time.Millisecond}
	blocker, err := capacity.AcquireLease(context.Background(), queue.GateAdmissionRequest{WorkerID: "blocker"})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	waitCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = runWithPromotionCapacity(waitCtx, capacity, queue.GateAdmissionRequest{WorkerID: "fresh-ci"}, "test promotion", func(context.Context) (record.Stored, error) {
		called = true
		return record.Stored{}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("contending CI was not serialized: err=%v called=%t", err, called)
	}
	blocker.Release()

	called = false
	if _, err := runWithPromotionCapacity(context.Background(), capacity, queue.GateAdmissionRequest{WorkerID: "fresh-ci"}, "test promotion", func(context.Context) (record.Stored, error) {
		called = true
		return record.Stored{}, nil
	}); err != nil || !called {
		t.Fatalf("released capacity did not admit fresh CI: err=%v called=%t", err, called)
	}
	reacquired, err := capacity.AcquireLease(context.Background(), queue.GateAdmissionRequest{WorkerID: "after-ci"})
	if err != nil {
		t.Fatalf("fresh CI did not release capacity: %v", err)
	}
	reacquired.Release()
}

func TestPromotionCICommandRunnerBorrowsCapacityWithoutNestedDeadlock(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kitsoki")
	build := exec.Command("go", "build", "-o", bin, "./cmd/kitsoki")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build nested CLI: %v\n%s", err, out)
	}
	project := t.TempDir()
	capacity := queue.FileGateCapacity{Root: t.TempDir(), Pool: "promotion-nested", Max: 1}
	_, err := runWithPromotionCapacity(context.Background(), capacity, queue.GateAdmissionRequest{WorkerID: "promotion-ci"}, "test promotion", func(ctx context.Context) (record.Stored, error) {
		command := strconv.Quote(bin) + ` queue gate-run --project . --capacity-root "$KITSOKI_GATE_CAPACITY_ROOT" --capacity-pool "$KITSOKI_GATE_CAPACITY_POOL" --gate-tier change -- sh -c true`
		output, code, err := (promotionCICommandRunner{}).Run(ctx, project, command)
		if err != nil || code != 0 {
			return record.Stored{}, fmt.Errorf("nested gate-run code=%d err=%v output=%s", code, err, output)
		}
		return record.Stored{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHostedCapsulePromotionExecutorRequiresExplicitIntegrationTargetBeforeCredentials(t *testing.T) {
	opts := capsulePromoteOptions{
		WorkspaceID: "hosted-capsule-1", TargetRef: "main",
		RemoteAdmission: remoteAdmissionOptions{
			URL: "http://127.0.0.1:7444", TargetBaseSHA: strings.Repeat("a", 40),
		},
	}
	err := validateHostedCapsulePromotion(opts)
	if err == nil || !strings.Contains(err.Error(), "integration/*") {
		t.Fatalf("hosted executor accepted non-integration target: %v", err)
	}
}

func TestHostedCapsulePromotionExecutorRefusesNonLoopbackAdmission(t *testing.T) {
	opts := capsulePromoteOptions{
		WorkspaceID: "hosted-capsule-1", TargetRef: "integration/train-1",
		RemoteAdmission: remoteAdmissionOptions{
			URL: "https://admission.example.test", TargetBaseSHA: strings.Repeat("a", 40),
		},
	}
	err := validateHostedCapsulePromotion(opts)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("hosted executor accepted non-loopback admission endpoint: %v", err)
	}
}

func TestHostedCapsulePromotionExecutorFlagsKeepCredentialsHostOnly(t *testing.T) {
	cmd := queueCapsulePromotionExecutorCmd()
	for _, name := range []string{"workspace", "target", "admission-url", "admission-token-env", "bucket-key-env", "bucket-secret-env", "target-base-sha", "train", "status-command"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("hosted executor is missing --%s", name)
		}
	}
	for _, name := range []string{"admission-token", "bucket-key", "bucket-secret"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Fatalf("hosted executor must receive %s through an environment name, not an argument", name)
		}
	}
	for _, name := range []string{"workspace-path", "source-path", "bundle", "receipt", "run-record"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Fatalf("hosted executor must resolve registered identity locally, not accept controller material %q", name)
		}
	}
}

func TestCapsuleSourceImporterRequiresRegisteredIdentityAndHostCredentialsByName(t *testing.T) {
	cmd := queueImportCapsuleSourceCmd()
	for _, name := range []string{"project-id", "workspace", "generation", "definition-digest", "branch", "head", "bucket-url", "bucket-key-env", "bucket-secret-env"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("source importer is missing --%s", name)
		}
	}
	for _, name := range []string{"bundle", "source-path", "bucket-key", "bucket-secret"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Fatalf("source importer must stream source and use host credential names only, found --%s", name)
		}
	}
}

func TestRemoteCandidateStatusDistinguishesAdmissionFromTerminalDelivery(t *testing.T) {
	if remoteCandidateTerminal(queue.Queued) {
		t.Fatal("queued remote admission was falsely reported terminal")
	}
	for _, status := range []queue.Status{queue.Landed, queue.Rejected} {
		if !remoteCandidateTerminal(status) {
			t.Fatalf("terminal status %q was not reported terminal", status)
		}
	}
}

func TestPostRemoteAdmissionDistinguishesUnauthorizedAndRateLimited(t *testing.T) {
	t.Setenv("KITSOKI_TEST_REMOTE_TOKEN", "test-token")
	request := admissionserver.Request{Schema: admissionserver.RequestSchema}
	for _, tc := range []struct {
		name, retryAfter string
		code             int
		remoteCode       string
		retryable        bool
	}{
		{name: "unauthorized", code: http.StatusUnauthorized, remoteCode: "unauthorized", retryable: false},
		{name: "rate limited", code: http.StatusTooManyRequests, remoteCode: "rate_limited", retryAfter: "7200", retryable: true},
		{name: "replay substitution", code: http.StatusConflict, remoteCode: "replay_substitution", retryable: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(`{"error":{"code":"` + tc.remoteCode + `","message":"typed failure","request_id":"request-1"}}`))
			}))
			defer server.Close()
			_, err := postRemoteAdmission(context.Background(), remoteAdmissionOptions{URL: server.URL, TokenEnv: "KITSOKI_TEST_REMOTE_TOKEN"}, request)
			var typed *remoteAdmissionHTTPError
			if !errors.As(err, &typed) || typed.Status != tc.code || typed.Code != tc.remoteCode || typed.RequestID != "request-1" || typed.Retryable != tc.retryable {
				t.Fatalf("error did not preserve typed remote status: %#v", err)
			}
			if tc.retryable && typed.RetryAfter != time.Hour {
				t.Fatalf("429 retry-after = %s, want bounded 1h", typed.RetryAfter)
			}
			if !tc.retryable && typed.RetryAfter != 0 {
				t.Fatalf("401 unexpectedly became retryable: %#v", typed)
			}
		})
	}
}

func TestRemoteCapsuleAdmissionRejectsNonIntegrationTargetBeforeObjectStore(t *testing.T) {
	_, err := remoteCapsuleAdmission(context.Background(), capsulePromoteOptions{TargetRef: "main", RemoteAdmission: remoteAdmissionOptions{URL: "https://example.invalid"}}, control.Instance{}, "", "", record.Stored{})
	if err == nil || !strings.Contains(err.Error(), "integration/*") {
		t.Fatalf("non-integration target was accepted: %v", err)
	}
}

func TestRemoteExecutionIdentityIncludesSealedBundleDigest(t *testing.T) {
	instance := control.Instance{ID: "workspace-1", Generation: 2, Head: strings.Repeat("a", 40)}
	opts := capsulePromoteOptions{TargetRef: "integration/train-1"}
	stored := record.Stored{Receipt: receipt.Receipt{ReceiptID: "sha256:" + strings.Repeat("b", 64)}}
	first := remoteExecutionID(instance, opts, stored, "sha256:"+strings.Repeat("c", 64))
	if got := remoteExecutionID(instance, opts, stored, "sha256:"+strings.Repeat("c", 64)); got != first {
		t.Fatalf("exact bundle retry changed execution identity: %s != %s", got, first)
	}
	if got := remoteExecutionID(instance, opts, stored, "sha256:"+strings.Repeat("d", 64)); got == first {
		t.Fatalf("byte-different bundle reused object identity %s", got)
	}
}

func TestPutImmutableObjectRejectsExistingByteSubstitution(t *testing.T) {
	objects := objectstore.NewFake()
	if _, err := objects.Put(context.Background(), "runs/capsule-1/wip/refs.bundle", bytes.NewReader([]byte("wrong")), 5, objectstore.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	err := putImmutableObject(context.Background(), objects, "runs/capsule-1/wip/refs.bundle", []byte("right"), "application/vnd.git.bundle")
	if err == nil || !strings.Contains(err.Error(), "different bytes") {
		t.Fatalf("existing object substitution was accepted: %v", err)
	}
}

// TestCapsulePromoteDoctorCheckReturnsTypedNotReadyForMissingConfig guards
// the receipt-bound promotion path: `capsule promote` must run the same
// bounded, no-spend readiness preflight as `capsule ci doctor` and surface a
// typed not-ready report instead of failing with an opaque error (or, prior
// to this preflight existing, proceeding into a run that could hang).
func TestCapsulePromoteDoctorCheckReturnsTypedNotReadyForMissingConfig(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, ".capsules", "workspaces", "w")
	instance := control.Instance{ID: "w", State: control.StateReady, Generation: 1, DefinitionDigest: "sha256:def", Head: "sha256:source"}

	report, err := capsulePromoteDoctorCheck(context.Background(), root, "change", instance, workspacePath)
	if err != nil {
		t.Fatalf("doctor check returned an error instead of a typed report: %v", err)
	}
	if report.Ready {
		t.Fatalf("expected a not-ready report for a workspace missing .kitsoki/ci.yaml, got %#v", report)
	}
	if len(report.Checks) == 0 {
		t.Fatal("expected at least one typed check explaining why promote is blocked")
	}
}

func TestCapsulePromoteRetryAfterBusyReusesExactReceiptAndQueueCandidate(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("a", 40)
	instance := control.Instance{ID: "w", Generation: 7, Head: sha}
	opts := capsulePromoteOptions{Pipeline: "change", TargetRef: "main", GateCommand: "node scripts/kitsoki-ci.mjs"}
	stored := promoteReuseStored(t, root, instance, opts.Pipeline, "busy-receipt-run")
	if err := persistPromoteReceiptReuse(root, instance, "agent/retry", opts, stored); err != nil {
		t.Fatal(err)
	}
	runs := 0
	reused, wasReused, err := promoteReceiptForAttempt(context.Background(), root, instance, "agent/retry", opts, func(context.Context) (record.Stored, error) {
		runs++
		return record.Stored{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !wasReused || runs != 0 || reused.Receipt.ReceiptID != stored.Receipt.ReceiptID {
		t.Fatalf("retry did not reuse exact receipt: reused=%t runs=%d got=%#v want=%#v", wasReused, runs, reused.Receipt, stored.Receipt)
	}
	q := queue.Store{ProjectRoot: root}
	first, err := q.Submit(queue.Submit{Branch: "agent/retry", SHA: sha, Receipt: reused.Receipt, ReceiptRef: reused.ReceiptPath, Backend: "local", TargetRef: opts.TargetRef})
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Submit(queue.Submit{Branch: "agent/retry", SHA: sha, Receipt: reused.Receipt, ReceiptRef: reused.ReceiptPath, Backend: "local", TargetRef: opts.TargetRef})
	if err != nil {
		t.Fatal(err)
	}
	state, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(state.Candidates) != 1 {
		t.Fatalf("retry duplicated candidate: first=%s second=%s candidates=%#v", first.ID, second.ID, state.Candidates)
	}
}

func TestCapsulePromoteReceiptReuseFailsClosedOnRunSubstitution(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("b", 40)
	instance := control.Instance{ID: "w", Generation: 7, Head: sha}
	opts := capsulePromoteOptions{Pipeline: "change", TargetRef: "main", GateCommand: "node scripts/kitsoki-ci.mjs"}
	stored := promoteReuseStored(t, root, instance, opts.Pipeline, "substituted-run")
	if err := persistPromoteReceiptReuse(root, instance, "agent/retry", opts, stored); err != nil {
		t.Fatal(err)
	}
	run, err := (ci.FileRunStore{ProjectRoot: root}).Get(stored.Receipt.JobID)
	if err != nil {
		t.Fatal(err)
	}
	run.Result.Verdict.StoryDigest = "sha256:substituted"
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(run); err != nil {
		t.Fatal(err)
	}
	runs := 0
	_, _, err = promoteReceiptForAttempt(context.Background(), root, instance, "agent/retry", opts, func(context.Context) (record.Stored, error) {
		runs++
		return record.Stored{}, nil
	})
	if err == nil || runs != 0 || !strings.Contains(err.Error(), "reusable receipt provenance") {
		t.Fatalf("substituted run was not fail-closed: err=%v runs=%d", err, runs)
	}
}

func TestHostedArtifactPromotionRetriesInsteadOfReusingIneligibleExactReceipt(t *testing.T) {
	// The hosted source-artifact executor materializes an immutable Capsule and
	// then uses promoteReceiptForAttempt. An old, exact CI result whose verdict
	// is no longer promotion-eligible is stale reuse, not authorization to
	// promote and not a reason to skip fresh host CI.
	root := t.TempDir()
	sha := strings.Repeat("d", 40)
	instance := control.Instance{ID: "hosted-artifact", Generation: 9, Head: sha}
	opts := capsulePromoteOptions{Pipeline: "change", TargetRef: "integration/train-1", GateCommand: "node scripts/kitsoki-ci.mjs"}
	old := promoteReuseStoredWithEligibility(t, root, instance, opts.Pipeline, "ineligible-host-run", false)
	if err := persistPromoteReceiptReuse(root, instance, "agent/hosted-artifact", opts, old); err != nil {
		t.Fatal(err)
	}
	fresh := promoteReuseStoredWithEligibility(t, root, instance, opts.Pipeline, "fresh-host-run", true)
	runs := 0
	got, reused, err := promoteReceiptForAttempt(context.Background(), root, instance, "agent/hosted-artifact", opts, func(context.Context) (record.Stored, error) {
		runs++
		return fresh, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reused || runs != 1 || got.Receipt.ReceiptID != fresh.Receipt.ReceiptID || got.Receipt.ReceiptID == old.Receipt.ReceiptID {
		t.Fatalf("hosted artifact retry reused ineligible receipt: reused=%t runs=%d got=%s old=%s fresh=%s", reused, runs, got.Receipt.ReceiptID, old.Receipt.ReceiptID, fresh.Receipt.ReceiptID)
	}
	if err := (record.PromotionGate{ProjectRoot: root}).Verify(context.Background(), got.Receipt.ReceiptID, reconcile.Plan{Candidate: sha}); err != nil {
		t.Fatalf("fresh exact CI result was not promotion eligible: %v", err)
	}
}

func TestCapsulePromoteReceiptReuseDoesNotCrossQueueGate(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("c", 40)
	instance := control.Instance{ID: "w", Generation: 7, Head: sha}
	old := capsulePromoteOptions{Pipeline: "change", TargetRef: "main", GateCommand: "node scripts/old-gate.mjs"}
	stored := promoteReuseStored(t, root, instance, old.Pipeline, "old-gate-run")
	if err := persistPromoteReceiptReuse(root, instance, "agent/retry", old, stored); err != nil {
		t.Fatal(err)
	}
	newOpts := old
	newOpts.GateCommand = "node scripts/new-gate.mjs"
	runs := 0
	got, wasReused, err := promoteReceiptForAttempt(context.Background(), root, instance, "agent/retry", newOpts, func(context.Context) (record.Stored, error) {
		runs++
		return stored, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if wasReused || runs != 1 || got.Receipt.ReceiptID != stored.Receipt.ReceiptID {
		t.Fatalf("receipt crossed a changed queue gate: reused=%t runs=%d got=%#v", wasReused, runs, got.Receipt)
	}
}

func promoteReuseStored(t *testing.T, root string, instance control.Instance, pipeline, jobID string) record.Stored {
	return promoteReuseStoredWithEligibility(t, root, instance, pipeline, jobID, true)
}

func promoteReuseStoredWithEligibility(t *testing.T, root string, instance control.Instance, pipeline, jobID string, promotionEligible bool) record.Stored {
	t.Helper()
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: jobID, ProjectID: "test", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: instance.ID, Generation: instance.Generation}, SourceDigest: instance.Head, StoryPath: "stories/ci/app.yaml", StoryDigest: "sha256:story", Environment: lock, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	outcome, checkOutcome := "passed", "passed"
	if !promotionEligible {
		outcome, checkOutcome = "failed", "failed"
	}
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: pipeline, Outcome: outcome, Checks: []ci.Check{{ID: "test", Kind: "deterministic", Outcome: checkOutcome, Evidence: []string{"artifact:test"}}}, PromotionEligible: promotionEligible, SourceDigest: envelope.SourceDigest, StoryDigest: envelope.StoryDigest, EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest}
	run := ci.RunResult{Job: artifactjob.Job{ID: artifactjob.JobID(jobID)}, Envelope: envelope, Verdict: verdict, Execution: executor.Result{VerdictArtifact: "artifact:verdict"}}
	stored, err := record.Persist(root, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: jobID, Result: run, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
		t.Fatal(err)
	}
	return stored
}
