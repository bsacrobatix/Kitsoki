package admissionserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/objectstore"
)

type admissionFixture struct {
	controller  string
	serviceRoot string
	worker      string
	bundlePath  string
	bundleRaw   []byte
	objects     *objectstore.Fake
	request     Request
	base        string
	candidate   string
}

func TestRemoteAdmissionAuthenticatesBeforeRateLimiting(t *testing.T) {
	fixture := newAdmissionFixture(t)
	server := newFixtureServer(t, fixture, Config{Token: "secret", RequireAuth: true, MaxConcurrent: 1})
	server.limit <- struct{}{}
	t.Cleanup(func() { <-server.limit })

	t.Run("bad auth is 401 even when capacity is exhausted", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1/queue/admissions", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer wrong")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"unauthorized"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "rate_limited") {
			t.Fatalf("401 was misclassified as 429: %s", response.Body.String())
		}
	})

	t.Run("authenticated overload is 429", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1/queue/admissions", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "1" ||
			!strings.Contains(response.Body.String(), `"code":"rate_limited"`) {
			t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})
}

func TestPOGWorkerHandoffCanonicalDigestInterop(t *testing.T) {
	candidate, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	digest := func(character string) string { return "sha256:" + strings.Repeat(character, 64) }
	result := queue.ExternalWorkerResult{
		Schema: queue.ExternalWorkerResultSchema, ExecutionID: "vmpool-interop-1",
		JobID: "job-interop-1", TrainID: "train-interop-1",
		ManifestDigest: digest("c"), Branch: "integration/trains/interop-1",
		CandidateSHA: candidate, BaseSHA: base, TargetRef: "staging/local",
		ReceiptID: digest("d"), BundleDigest: digest("e"), BundleBytes: 123,
		BundleKey: "runs/vmpool-interop-1/wip/refs.bundle",
	}
	handoff := Handoff{
		Schema:     HandoffSchema,
		HandoffKey: "runs/vmpool-interop-1/artifacts/integration-train-external-admission-handoff.json",
		Result:     result,
		// These constants were produced by the POG worker producer's
		// recursive key-sorted canonical JSON algorithm. Keeping them fixed
		// makes this a cross-language contract test rather than a Go
		// implementation testing itself.
		ResultDigest: "sha256:97536298db0d232c5c0129602812487813ff8afef2869355ff1e3edaeb213895",
		Receipt: HandoffReceipt{
			ReceiptID: digest("d"), JobID: "job-interop-1", SourceSHA: candidate,
			ContentDigest: digest("d"), RawDigest: digest("f"),
		},
		Bundle: HandoffBundle{
			Key: result.BundleKey, Digest: digest("e"), Bytes: 123,
			Head: candidate, BaseSHA: base,
		},
		HandoffDigest: "sha256:1cebc11663da1af0f72fb0e81218f3327f10ce756037960e4e8306ae539de495",
	}
	if err := validateHandoff(handoff, queue.DefaultMaxExternalBundle); err != nil {
		t.Fatalf("POG canonical handoff rejected: %v", err)
	}
}

func TestRemoteAdmissionHTTPAcceptsSealedRequest(t *testing.T) {
	fixture := newAdmissionFixture(t)
	server := newFixtureServer(t, fixture, Config{})
	raw, err := json.Marshal(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/queue/admissions", bytes.NewReader(raw))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Kitsoki-Request-ID") == "" {
		t.Fatal("response omitted request identity")
	}
	var decoded Response
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != ResponseSchema || decoded.Admission.ID == "" ||
		decoded.Anchor.ID == "" || decoded.Candidate.ID == "" {
		t.Fatalf("incomplete response: %#v", decoded)
	}
}

func TestRemoteAdmissionPersistsOutsideProjectAndExactReplayIsIdempotent(t *testing.T) {
	fixture := newAdmissionFixture(t)
	server := newFixtureServer(t, fixture, Config{})
	refsBefore := git(t, fixture.controller, "for-each-ref", "--format=%(refname) %(objectname)")
	objectsBefore := git(t, fixture.controller, "count-objects", "-v")
	if gitSucceeds(fixture.controller, "cat-file", "-e", fixture.candidate+"^{commit}") {
		t.Fatal("candidate unexpectedly existed in protected project before admission")
	}

	first, err := server.Admit(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.Admit(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Admission.ID != second.Admission.ID ||
		first.Admission.RecordDigest != second.Admission.RecordDigest ||
		first.Anchor.ID != second.Anchor.ID || first.Candidate.ID != second.Candidate.ID {
		t.Fatalf("exact replay changed immutable IDs:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.Candidate.SourceAnchorID != first.Anchor.ID {
		t.Fatalf("candidate source anchor = %q, want %q", first.Candidate.SourceAnchorID, first.Anchor.ID)
	}
	if first.Candidate.ReceiptRef == "" || first.Candidate.RunRecordRef == "" {
		t.Fatalf("candidate omitted external evidence references: %#v", first.Candidate)
	}
	for _, path := range []string{first.Candidate.ReceiptRef, first.Candidate.RunRecordRef} {
		if !strings.HasPrefix(path, fixture.serviceRoot+string(filepath.Separator)) {
			t.Fatalf("external evidence escaped service authority: %s", path)
		}
	}
	if refsAfter := git(t, fixture.controller, "for-each-ref", "--format=%(refname) %(objectname)"); refsAfter != refsBefore {
		t.Fatalf("protected project refs changed during remote admission:\nbefore=%s\nafter=%s", refsBefore, refsAfter)
	}
	if objectsAfter := git(t, fixture.controller, "count-objects", "-v"); objectsAfter != objectsBefore {
		t.Fatalf("protected project object DB changed during remote admission:\nbefore=%s\nafter=%s", objectsBefore, objectsAfter)
	}
	if gitSucceeds(fixture.controller, "cat-file", "-e", fixture.candidate+"^{commit}") {
		t.Fatal("remote admission imported candidate into protected project object DB")
	}
	privateRepo := filepath.Join(fixture.serviceRoot, "queue", "external-objects.git")
	if got := git(t, privateRepo, "rev-parse", first.Anchor.Ref); got != fixture.candidate {
		t.Fatalf("private anchor ref = %s, want %s", got, fixture.candidate)
	}
	for _, path := range []string{
		filepath.Join(fixture.serviceRoot, "queue", "state.json"),
		filepath.Join(fixture.serviceRoot, "admissions", first.Admission.ID+".json"),
	} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("private durable file %s mode=%v err=%v", path, mode(info), err)
		}
	}

	substitution := fixture.request
	substitution.Paths = []string{"different/path"}
	_, err = server.Admit(context.Background(), substitution)
	assertAdmissionError(t, err, http.StatusConflict, "replay_substitution")

	substitution = fixture.request
	runRaw, err := base64.StdEncoding.DecodeString(substitution.RunRecordBase64)
	if err != nil {
		t.Fatal(err)
	}
	// Different bytes with the same verified record must not be accepted as a
	// replay substitution for an execution that already has an immutable intent.
	substitution.RunRecordBase64 = base64.StdEncoding.EncodeToString(append(runRaw, '\n'))
	_, err = server.Admit(context.Background(), substitution)
	assertAdmissionError(t, err, http.StatusConflict, "replay_substitution")
}

func TestRemoteAdmissionRejectsMalformedTamperedAndSubstitutedInputs(t *testing.T) {
	t.Run("target base substitution", func(t *testing.T) {
		fixture := newAdmissionFixture(t)
		server := newFixtureServer(t, fixture, Config{})
		request := fixture.request
		request.TargetBaseSHA = strings.Repeat("f", 40)
		_, err := server.Admit(context.Background(), request)
		assertAdmissionError(t, err, http.StatusBadRequest, "target_mismatch")
		assertQueueEmpty(t, server)
	})

	t.Run("run record source substitution", func(t *testing.T) {
		fixture := newAdmissionFixture(t)
		server := newFixtureServer(t, fixture, Config{})
		raw, err := base64.StdEncoding.DecodeString(fixture.request.RunRecordBase64)
		if err != nil {
			t.Fatal(err)
		}
		var run ci.RunRecord
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		run.Result.Envelope.SourceDigest = strings.Repeat("e", 40)
		raw, err = json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		request := fixture.request
		request.RunRecordBase64 = base64.StdEncoding.EncodeToString(raw)
		_, err = server.Admit(context.Background(), request)
		assertAdmissionError(t, err, http.StatusUnprocessableEntity, "run_record_mismatch")
		assertQueueEmpty(t, server)
	})

	t.Run("unknown request field", func(t *testing.T) {
		fixture := newAdmissionFixture(t)
		server := newFixtureServer(t, fixture, Config{})
		raw, err := json.Marshal(fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]any
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		request["surprise"] = true
		raw, _ = json.Marshal(request)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/queue/admissions", bytes.NewReader(raw)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "malformed_request") {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("handoff tamper", func(t *testing.T) {
		fixture := newAdmissionFixture(t)
		server := newFixtureServer(t, fixture, Config{})
		request := fixture.request
		request.Handoff.Bundle.Digest = "sha256:" + strings.Repeat("f", 64)
		_, err := server.Admit(context.Background(), request)
		assertAdmissionError(t, err, http.StatusBadRequest, "bundle_mismatch")
		assertQueueEmpty(t, server)
	})

	t.Run("receipt tamper", func(t *testing.T) {
		fixture := newAdmissionFixture(t)
		server := newFixtureServer(t, fixture, Config{})
		raw, err := base64.StdEncoding.DecodeString(fixture.request.ReceiptBase64)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, ' ')
		request := fixture.request
		request.ReceiptBase64 = base64.StdEncoding.EncodeToString(raw)
		_, err = server.Admit(context.Background(), request)
		assertAdmissionError(t, err, http.StatusBadRequest, "receipt_tampered")
		assertQueueEmpty(t, server)
	})

	t.Run("remote bundle tamper", func(t *testing.T) {
		fixture := newAdmissionFixture(t)
		server := newFixtureServer(t, fixture, Config{})
		tampered := bytes.Repeat([]byte{'x'}, len(fixture.bundleRaw))
		if _, err := fixture.objects.Put(context.Background(), fixture.request.Handoff.Bundle.Key, bytes.NewReader(tampered), int64(len(tampered)), objectstore.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		_, err := server.Admit(context.Background(), fixture.request)
		assertAdmissionError(t, err, http.StatusUnprocessableEntity, "bundle_tampered")
		assertQueueEmpty(t, server)
	})
}

func TestRemoteAdmissionRecoversEveryDurableInterruption(t *testing.T) {
	for _, stage := range []string{"after-intent", "after-bundle", "after-queue", "after-record"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newAdmissionFixture(t)
			interrupted := false
			first := newFixtureServer(t, fixture, Config{Interrupt: func(got string) error {
				if got == stage && !interrupted {
					interrupted = true
					return context.Canceled
				}
				return nil
			}})
			if _, err := first.Admit(context.Background(), fixture.request); err == nil {
				t.Fatalf("%s interruption unexpectedly succeeded", stage)
			}
			restarted := newFixtureServer(t, fixture, Config{})
			response, err := restarted.Admit(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("restart after %s: %v", stage, err)
			}
			replayed, err := restarted.Admit(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("replay after %s: %v", stage, err)
			}
			if response.Admission.ID != replayed.Admission.ID ||
				response.Candidate.ID != replayed.Candidate.ID || response.Anchor.ID != replayed.Anchor.ID {
				t.Fatalf("recovery after %s changed identities", stage)
			}
			entries, err := os.ReadDir(filepath.Join(fixture.serviceRoot, "incoming"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("incoming temp leak after %s: entries=%v err=%v", stage, entries, err)
			}
			state, err := restarted.store.List()
			if err != nil || len(state.Candidates) != 1 {
				t.Fatalf("queue after %s = %#v err=%v", stage, state, err)
			}
		})
	}
}

func TestRemoteAdmissionRequiresAuthorityOutsideProject(t *testing.T) {
	fixture := newAdmissionFixture(t)
	path := filepath.Join(fixture.controller, "state")
	_, err := New(Config{
		Root: path, ProjectRoot: fixture.controller,
		Objects: fixture.objects,
	})
	if err == nil || !strings.Contains(err.Error(), "outside the protected project") {
		t.Fatalf("inside-project authority error = %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected inside-project authority created state: %v", statErr)
	}
}

func newAdmissionFixture(t *testing.T) admissionFixture {
	t.Helper()
	root := t.TempDir()
	controller := filepath.Join(root, "controller")
	serviceRoot := filepath.Join(root, "authority")
	worker := filepath.Join(root, "worker")
	if err := os.MkdirAll(controller, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, controller, "init", "-b", "main")
	git(t, controller, "config", "user.name", "Controller")
	git(t, controller, "config", "user.email", "controller@example.invalid")
	if err := os.WriteFile(filepath.Join(controller, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, controller, "add", "base.txt")
	git(t, controller, "commit", "-m", "base")
	base := git(t, controller, "rev-parse", "HEAD")
	run(t, root, "git", "clone", "--quiet", controller, worker)
	git(t, worker, "config", "user.name", "Worker")
	git(t, worker, "config", "user.email", "worker@example.invalid")
	if err := os.WriteFile(filepath.Join(worker, "fix.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, worker, "add", "fix.txt")
	git(t, worker, "commit", "-m", "worker result")
	candidate := git(t, worker, "rev-parse", "HEAD")
	bundlePath := filepath.Join(root, "refs.bundle")
	git(t, worker, "bundle", "create", bundlePath, "HEAD")
	bundleRaw, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}

	ciReceipt := buildReceipt(t, candidate)
	receiptRaw, err := json.MarshalIndent(ciReceipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	receiptRaw = append(receiptRaw, '\n')
	runRaw, err := json.Marshal(ci.RunRecord{
		JobID:     ciReceipt.JobID,
		Result:    ci.RunResult{Job: artifactjob.Job{ID: artifactjob.JobID(ciReceipt.JobID)}, Envelope: ciReceipt.Envelope, Verdict: ciReceipt.Verdict},
		ReceiptID: ciReceipt.ReceiptID, ReceiptVerification: "valid",
	})
	if err != nil {
		t.Fatal(err)
	}
	executionID := "vmpool-worker-train-1"
	result := queue.ExternalWorkerResult{
		Schema: queue.ExternalWorkerResultSchema, ExecutionID: executionID,
		JobID: ciReceipt.JobID, TrainID: "train-worker-owned-1",
		ManifestDigest: "sha256:" + strings.Repeat("b", 64),
		Branch:         "integration/trains/worker-owned-1", CandidateSHA: candidate,
		BaseSHA: base, TargetRef: "main", ReceiptID: ciReceipt.ReceiptID,
		BundleDigest: digestBytes(bundleRaw), BundleBytes: int64(len(bundleRaw)),
		BundleKey: "runs/" + executionID + "/wip/refs.bundle",
	}
	resultRaw, err := canonicalJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	handoff := Handoff{
		Schema:     HandoffSchema,
		HandoffKey: "runs/" + executionID + "/artifacts/integration-train-external-admission-handoff.json",
		Result:     result, ResultDigest: digestBytes(resultRaw),
		Receipt: HandoffReceipt{
			ReceiptID: ciReceipt.ReceiptID, JobID: ciReceipt.JobID, SourceSHA: candidate,
			ContentDigest: ciReceipt.Integrity.ContentDigest, RawDigest: digestBytes(receiptRaw),
		},
		Bundle: HandoffBundle{
			Key: result.BundleKey, Digest: result.BundleDigest, Bytes: result.BundleBytes,
			Head: candidate, BaseSHA: base,
		},
	}
	unsignedRaw, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	var unsigned map[string]any
	decoder := json.NewDecoder(bytes.NewReader(unsignedRaw))
	decoder.UseNumber()
	if err := decoder.Decode(&unsigned); err != nil {
		t.Fatal(err)
	}
	delete(unsigned, "handoff_digest")
	canonical, err := canonicalJSON(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	handoff.HandoffDigest = digestBytes(canonical)
	request := Request{
		Schema: RequestSchema, Handoff: handoff,
		ReceiptBase64:   base64.StdEncoding.EncodeToString(receiptRaw),
		RunRecordBase64: base64.StdEncoding.EncodeToString(runRaw),
		TargetBaseSHA:   base, TargetPolicy: queue.WaveAutoPolicy,
		FinalizationPolicy: queue.AutonomousFinalization,
	}
	objects := objectstore.NewFake()
	if _, err := objects.Put(context.Background(), result.BundleKey, bytes.NewReader(bundleRaw), int64(len(bundleRaw)), objectstore.PutOptions{ContentType: "application/x-git-bundle"}); err != nil {
		t.Fatal(err)
	}
	return admissionFixture{
		controller: controller, serviceRoot: serviceRoot, worker: worker,
		bundlePath: bundlePath, bundleRaw: bundleRaw, objects: objects,
		request: request, base: base, candidate: candidate,
	}
}

func buildReceipt(t *testing.T, candidate string) receipt.Receipt {
	t.Helper()
	lock, err := environment.SealLock(environment.Lock{
		Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:environment",
		Network: "none", Sandbox: "supervised",
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{
		JobID: "job-worker-train-1", ProjectID: "pog",
		DefinitionDigest: "sha256:definition", Instance: control.Handle{ID: "worker", Generation: 1},
		SourceDigest: candidate, StoryPath: "stories/pog-integration-gate/app.yaml",
		StoryDigest: "sha256:story", Environment: lock, Policy: executor.Policy{Network: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{
		Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed",
		Checks:            []ci.Check{{ID: "tests", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:tests"}}},
		PromotionEligible: true, SourceDigest: candidate, StoryDigest: envelope.StoryDigest,
		EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest,
	}
	built, verification, err := receipt.Build(receipt.BuildInput{
		Job:      artifactjob.Job{ID: artifactjob.JobID(envelope.JobID)},
		Envelope: envelope, Verdict: verdict, TraceDigest: "sha256:trace",
	})
	if err != nil || verification.Status != "valid" || !verification.PromotionEligible {
		t.Fatalf("build receipt = %#v verification=%#v err=%v", built, verification, err)
	}
	return built
}

func newFixtureServer(t *testing.T, fixture admissionFixture, overrides Config) *Server {
	t.Helper()
	cfg := Config{
		Root: fixture.serviceRoot, ProjectRoot: fixture.controller,
		ProjectID: "pog", Objects: fixture.objects,
	}
	if overrides.Token != "" {
		cfg.Token = overrides.Token
	}
	cfg.RequireAuth = overrides.RequireAuth
	if overrides.MaxConcurrent != 0 {
		cfg.MaxConcurrent = overrides.MaxConcurrent
	}
	cfg.Interrupt = overrides.Interrupt
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func assertAdmissionError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var typed *admissionError
	if !errors.As(err, &typed) || typed.status != status || typed.code != code {
		t.Fatalf("error = %#v, want status=%d code=%s", err, status, code)
	}
}

func assertQueueEmpty(t *testing.T, server *Server) {
	t.Helper()
	state, err := server.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 0 {
		t.Fatalf("failed admission wrote candidates: %#v", state.Candidates)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func run(t *testing.T, dir, program string, args ...string) {
	t.Helper()
	command := exec.Command(program, args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", program, args, err, output)
	}
}

func gitSucceeds(dir string, args ...string) bool {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return command.Run() == nil
}

func mode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode()
}
