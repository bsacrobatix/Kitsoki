package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalBundleAdmissionKeepsControllerObjectFreeUntilNormalQueueLifecycle(t *testing.T) {
	controller, bundlePath, result, submit := externalBundleFixture(t)
	if _, err := gitOutput(context.Background(), controller, "cat-file", "-e", result.CandidateSHA+"^{commit}"); err == nil {
		t.Fatal("candidate unexpectedly existed in controller project before admission")
	}

	store := Store{ProjectRoot: controller}
	first, anchor, err := store.AdmitExternalBundle(context.Background(), ExternalBundleSubmission{
		Result: result, BundlePath: bundlePath, Submit: submit,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, replayed, err := store.AdmitExternalBundle(context.Background(), ExternalBundleSubmission{
		Result: result, BundlePath: bundlePath, Submit: submit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || anchor.ID != replayed.ID || !anchor.CreatedAt.Equal(replayed.CreatedAt) {
		t.Fatalf("replay changed identities: first=%#v second=%#v anchor=%#v replay=%#v", first, second, anchor, replayed)
	}
	if first.SourceAnchorID != anchor.ID {
		t.Fatalf("candidate source anchor = %q, want %q", first.SourceAnchorID, anchor.ID)
	}
	if _, err := gitOutput(context.Background(), controller, "cat-file", "-e", result.CandidateSHA+"^{commit}"); err == nil {
		t.Fatal("admission imported candidate into controller project object database")
	}
	privateRepo := filepath.Join(controller, filepath.FromSlash(anchor.ObjectRepository))
	if got := git(t, privateRepo, "rev-parse", anchor.Ref); got != result.CandidateSHA {
		t.Fatalf("queue-private anchor ref = %s, want %s", got, result.CandidateSHA)
	}

	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: controller, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: controller, TargetRef: "main", SkipWIPPreservation: true},
		GateVersion: "external-bundle-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].phase() != Landed {
		t.Fatalf("normal queue lifecycle did not land external candidate: %#v", state.Candidates)
	}
	if got := git(t, controller, "rev-parse", "main"); got != result.CandidateSHA {
		t.Fatalf("protected main = %s, want %s", got, result.CandidateSHA)
	}
}

func TestExternalBundleAdmissionAndPreparationUseSeparateQueueAuthority(t *testing.T) {
	controller, bundlePath, result, submit := externalBundleFixture(t)
	queueRoot := filepath.Join(t.TempDir(), "queue-authority")
	store := Store{ProjectRoot: controller, QueueRoot: queueRoot}
	projectQueue := filepath.Join(controller, ".capsules", "queue")
	if _, err := os.Stat(projectQueue); !os.IsNotExist(err) {
		t.Fatalf("project-local queue unexpectedly exists before admission: %v", err)
	}

	candidate, anchor, err := store.AdmitExternalBundle(context.Background(), ExternalBundleSubmission{
		Result: result, BundlePath: bundlePath, Submit: submit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if anchor.ObjectRepository != "external-objects.git" {
		t.Fatalf("external authority anchor repository = %q", anchor.ObjectRepository)
	}
	if _, err := os.Stat(projectQueue); !os.IsNotExist(err) {
		t.Fatalf("external admission wrote project-local queue state: %v", err)
	}
	if _, err := gitOutput(context.Background(), controller, "cat-file", "-e", result.CandidateSHA+"^{commit}"); err == nil {
		t.Fatal("external authority admission wrote protected project objects")
	}
	privateRepo := filepath.Join(queueRoot, "external-objects.git")
	if got := git(t, privateRepo, "rev-parse", anchor.Ref); got != result.CandidateSHA {
		t.Fatalf("external authority ref = %s, want %s", got, result.CandidateSHA)
	}

	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: controller, QueueRoot: queueRoot, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: controller, TargetRef: "main", SkipWIPPreservation: true},
		GateVersion: "external-authority-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].ID != candidate.ID || state.Candidates[0].phase() != Landed {
		t.Fatalf("external authority queue did not land candidate: %#v", state.Candidates)
	}
	if got := git(t, controller, "rev-parse", "main"); got != result.CandidateSHA {
		t.Fatalf("protected main = %s, want %s", got, result.CandidateSHA)
	}
}

func TestExternalBundleAdmissionRejectsTamperWrongHeadAndWrongBase(t *testing.T) {
	t.Run("tamper", func(t *testing.T) {
		controller, bundlePath, result, submit := externalBundleFixture(t)
		file, err := os.OpenFile(bundlePath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("tamper")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, _, err = (Store{ProjectRoot: controller}).AdmitExternalBundle(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: bundlePath, Submit: submit})
		if err == nil || (!strings.Contains(err.Error(), "declared bounded size") && !strings.Contains(err.Error(), "digest mismatch")) {
			t.Fatalf("tampered bundle error = %v", err)
		}
		assertNoExternalCandidate(t, controller)
	})

	t.Run("wrong-head", func(t *testing.T) {
		controller, bundlePath, result, submit := externalBundleFixture(t)
		result.CandidateSHA = strings.Repeat("a", 40)
		submit.SHA = result.CandidateSHA
		submit.Receipt = testReceipt(t, result.CandidateSHA)
		result.ReceiptID = submit.Receipt.ReceiptID
		result.JobID = submit.Receipt.JobID
		_, _, err := (Store{ProjectRoot: controller}).AdmitExternalBundle(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: bundlePath, Submit: submit})
		if err == nil || !strings.Contains(err.Error(), "exact candidate head") {
			t.Fatalf("wrong head error = %v", err)
		}
		assertNoExternalCandidate(t, controller)
	})

	t.Run("wrong-base", func(t *testing.T) {
		controller, bundlePath, result, submit := externalBundleFixture(t)
		result.BaseSHA = strings.Repeat("b", 40)
		_, _, err := (Store{ProjectRoot: controller}).AdmitExternalBundle(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: bundlePath, Submit: submit})
		if err == nil || !strings.Contains(err.Error(), "missing declared base") {
			t.Fatalf("wrong base error = %v", err)
		}
		assertNoExternalCandidate(t, controller)
	})
}

func TestExternalBundleAdmissionRejectsTraversalSymlinkAndOversize(t *testing.T) {
	t.Run("traversal-key", func(t *testing.T) {
		controller, bundlePath, result, submit := externalBundleFixture(t)
		result.BundleKey = "../secret.bundle"
		_, _, err := (Store{ProjectRoot: controller}).AdmitExternalBundle(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: bundlePath, Submit: submit})
		if err == nil || !strings.Contains(err.Error(), "bundle_key is invalid") {
			t.Fatalf("traversal error = %v", err)
		}
		assertNoExternalCandidate(t, controller)
	})

	t.Run("symlink", func(t *testing.T) {
		controller, bundlePath, result, submit := externalBundleFixture(t)
		link := filepath.Join(t.TempDir(), "source.bundle")
		if err := os.Symlink(bundlePath, link); err != nil {
			t.Fatal(err)
		}
		_, _, err := (Store{ProjectRoot: controller}).AdmitExternalBundle(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: link, Submit: submit})
		if err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
			t.Fatalf("symlink error = %v", err)
		}
		assertNoExternalCandidate(t, controller)
	})

	t.Run("oversized", func(t *testing.T) {
		controller, bundlePath, result, submit := externalBundleFixture(t)
		_, _, err := (ExternalBundleAdmitter{Store: Store{ProjectRoot: controller}, MaxBundleBytes: result.BundleBytes - 1}).Admit(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: bundlePath, Submit: submit})
		if err == nil || !strings.Contains(err.Error(), "exceeds allowed maximum") {
			t.Fatalf("oversize error = %v", err)
		}
		assertNoExternalCandidate(t, controller)
	})
}

func TestExternalBundleAdmissionRecoversInterruptedImportWithoutAdmittingPartialCandidate(t *testing.T) {
	for _, stage := range []string{"after-import", "after-ref"} {
		t.Run(stage, func(t *testing.T) {
			controller, bundlePath, result, submit := externalBundleFixture(t)
			admitter := ExternalBundleAdmitter{
				Store: Store{ProjectRoot: controller},
				Interrupt: func(got string) error {
					if got == stage {
						return context.Canceled
					}
					return nil
				},
			}
			if _, _, err := admitter.Admit(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: bundlePath, Submit: submit}); err == nil {
				t.Fatal("interrupted import unexpectedly succeeded")
			}
			assertNoExternalCandidate(t, controller)
			anchorID, err := externalAnchorID(result)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(controller, ".capsules", "queue", "external-anchors", anchorID+".json")); !os.IsNotExist(err) {
				t.Fatalf("partial import published an anchor: %v", err)
			}

			candidate, anchor, err := (Store{ProjectRoot: controller}).AdmitExternalBundle(context.Background(), ExternalBundleSubmission{Result: result, BundlePath: bundlePath, Submit: submit})
			if err != nil {
				t.Fatal(err)
			}
			if candidate.SourceAnchorID != anchor.ID || anchor.ID != anchorID {
				t.Fatalf("recovery identities candidate=%#v anchor=%#v", candidate, anchor)
			}
		})
	}
}

func externalBundleFixture(t *testing.T) (controller, bundlePath string, result ExternalWorkerResult, submit Submit) {
	t.Helper()
	controller = protectedQueueRepo(t)
	source := filepath.Join(t.TempDir(), "worker")
	run(t, "", "git", "clone", "--quiet", controller, source)
	git(t, source, "config", "user.name", "External Worker")
	git(t, source, "config", "user.email", "worker@example.invalid")
	base := git(t, source, "rev-parse", "HEAD")
	commit(t, source, "worker-fix.txt", "fixed\n", "worker fix")
	candidate := git(t, source, "rev-parse", "HEAD")
	bundlePath = filepath.Join(t.TempDir(), "worker.bundle")
	git(t, source, "bundle", "create", bundlePath, "HEAD")
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	receipt := persistedReceipt(t, controller, candidate)
	manifest := "sha256:" + strings.Repeat("c", 64)
	result = ExternalWorkerResult{
		Schema: ExternalWorkerResultSchema, ExecutionID: "execution-1", JobID: receipt.JobID,
		TrainID: "train-1", ManifestDigest: manifest, Branch: "worker/train-1/job-1",
		CandidateSHA: candidate, BaseSHA: base, TargetRef: "main", ReceiptID: receipt.ReceiptID,
		BundleDigest: "sha256:" + hex.EncodeToString(sum[:]), BundleBytes: int64(len(raw)),
		BundleKey: "trains/train-1/job-1/refs.bundle",
	}
	submit = Submit{
		Branch: result.Branch, SHA: candidate, TargetRef: "main",
		TargetBaseSHAAtAdmission: git(t, controller, "rev-parse", "main"),
		Receipt:                  receipt, ReceiptRef: filepath.Join(controller, ".capsules", "ci", "receipts", receipt.ReceiptID+".json"),
		Backend: "external-worker", ManifestDigest: manifest,
	}
	return controller, bundlePath, result, submit
}

func assertNoExternalCandidate(t *testing.T, controller string) {
	t.Helper()
	state, err := (Store{ProjectRoot: controller}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 0 {
		t.Fatalf("failed import admitted queue candidate: %#v", state.Candidates)
	}
}
