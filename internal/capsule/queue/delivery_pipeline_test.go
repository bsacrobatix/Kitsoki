package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileGateCapacitySharesPhysicalPoolAcrossProjects(t *testing.T) {
	capacity := FileGateCapacity{Root: t.TempDir(), Pool: "workstation", Max: 2}
	releaseAll := make(chan struct{})
	acquired := make(chan struct{}, 10)
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, err := capacity.Acquire(context.Background(), GateAdmissionRequest{
				ProjectID: "repo-" + string(rune('a'+i)), TargetRef: "main", Tier: "change",
			})
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			now := active.Add(1)
			for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
			}
			acquired <- struct{}{}
			<-releaseAll
			active.Add(-1)
		}(i)
	}
	<-acquired
	<-acquired
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak=%d, want physical pool capacity 2", got)
	}
	close(releaseAll)
	wg.Wait()
	if got := peak.Load(); got > 2 {
		t.Fatalf("cross-repository gates exceeded pool: %d", got)
	}
}

func TestDeliveryDefaultsDeriveTierAndHostCapacity(t *testing.T) {
	for target, want := range map[string]string{
		"staging/local": "change",
		"main":          "full",
		"deploy/prod":   "release",
		"release/v2":    "release",
	} {
		if got := RequiredGateTierForTarget(target); got != want {
			t.Fatalf("target %q tier=%q, want %q", target, got, want)
		}
	}
	capacity := DefaultFileGateCapacity()
	if !filepath.IsAbs(capacity.Root) || capacity.Pool != "default" || capacity.Max != 1 {
		t.Fatalf("default capacity=%+v, want absolute per-user root/default/1", capacity)
	}
}

func TestRepairGreenRequiresIndependentReview(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reviewer RepairReviewer
	}{
		{name: "missing"},
		{name: "same_identity", reviewer: reviewFunc(func(context.Context, RepairReview) (RepairReviewResult, error) {
			return RepairReviewResult{Passed: true, ReviewerID: "repairer"}, nil
		})},
		{name: "rejected", reviewer: reviewFunc(func(context.Context, RepairReview) (RepairReviewResult, error) {
			return RepairReviewResult{Passed: false, ReviewerID: "reviewer"}, nil
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := Store{ProjectRoot: t.TempDir()}
			sha := strings.Repeat("a", 40)
			if _, err := store.Submit(Submit{Branch: "agent/fix", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			worker := Worker{Store: store, Deps: ProcessDeps{
				Integration: &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
					return Speculation{SHA: "tree", WorkspacePath: t.TempDir()}, nil
				}},
				Gate: gateFunc(func(context.Context, Speculation) (GateResult, error) {
					runs++
					return GateResult{Passed: runs == 2}, nil
				}),
				Repairer: repairFunc(func(context.Context, Speculation, error) ([]string, error) {
					return []string{"fixed"}, nil
				}),
				RepairReviewer:     tc.reviewer,
				RepairerID:         "repairer",
				ReviewPolicyDigest: "sha256:review",
				MaxAttempts:        1,
			}}
			if _, err := worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, _ := store.List()
			if state.Candidates[0].phase() != NeedsInput {
				t.Fatalf("candidate=%#v, want blocked", state.Candidates[0])
			}
		})
	}
}

func TestRepairAndReviewStagesHaveHardTimeouts(t *testing.T) {
	for _, stage := range []string{"repair", "review"} {
		t.Run(stage, func(t *testing.T) {
			store := Store{ProjectRoot: t.TempDir()}
			sha := strings.Repeat("e", 40)
			if _, err := store.Submit(Submit{Branch: "agent/timeout-" + stage, SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			deps := ProcessDeps{
				Integration: &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
					return Speculation{SHA: "tree", BaseSHA: "base"}, nil
				}},
				Gate: gateFunc(func(context.Context, Speculation) (GateResult, error) {
					runs++
					return GateResult{Passed: runs > 1}, nil
				}),
				Repairer: repairFunc(func(ctx context.Context, _ Speculation, _ error) ([]string, error) {
					if stage == "repair" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return []string{"repaired"}, nil
				}),
				RepairReviewer: reviewFunc(func(ctx context.Context, _ RepairReview) (RepairReviewResult, error) {
					<-ctx.Done()
					return RepairReviewResult{}, ctx.Err()
				}),
				RepairerID:         "repairer",
				ReviewPolicyDigest: "sha256:review",
				GateTimeout:        5 * time.Millisecond,
				MaxAttempts:        1,
			}
			if _, err := (Worker{Store: store, Deps: deps}).RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, err := store.List()
			if err != nil {
				t.Fatal(err)
			}
			candidate := state.Candidates[0]
			if candidate.phase() != NeedsInput || !strings.Contains(candidate.Failure, stage+" timed out") {
				t.Fatalf("candidate=%#v, want bounded %s failure", candidate, stage)
			}
		})
	}
}

func TestPreparedCandidateInvalidatesChangedGatePolicyAndTierMismatch(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("b", 40)
	candidate, err := store.Submit(Submit{Branch: "agent/main", SHA: sha, Receipt: testReceipt(t, sha), TargetRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.RequiredGateTier != "full" {
		t.Fatalf("omitted main tier=%q, want full", candidate.RequiredGateTier)
	}
	weak := Worker{Store: store, Deps: ProcessDeps{
		Integration: &fakeIntegration{}, Gate: gateFunc(func(context.Context, Speculation) (GateResult, error) {
			t.Fatal("weak tier must not claim main candidate")
			return GateResult{}, nil
		}), GateTier: "change",
	}}
	if progressed, err := weak.RunOnce(context.Background()); err != nil || progressed {
		t.Fatalf("weak worker progressed=%v err=%v", progressed, err)
	}
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		return Speculation{SHA: "tree", BaseSHA: "base"}, nil
	}}
	v1 := Worker{Store: store, Deps: ProcessDeps{
		Integration: integration, Gate: gateFunc(func(context.Context, Speculation) (GateResult, error) {
			return GateResult{Passed: true}, nil
		}), GateTier: "full", GateVersion: "main/v1",
	}}
	if _, err := v1.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	weak.Deps.Finalizer = finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
		t.Fatal("change worker must not finalize a full-tier main candidate")
		return FinalizeResult{}, nil
	})
	if progressed, err := weak.RunOnce(context.Background()); err != nil || progressed {
		t.Fatalf("weak finalizer progressed=%v err=%v", progressed, err)
	}
	v2 := v1
	v2.Deps.GateVersion = "main/v2"
	if _, err := v2.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ := store.List()
	if state.Candidates[0].phase() != Reprepare || !hasEvidence(state.Candidates[0], "prepared-policy-invalidated") {
		t.Fatalf("changed policy did not invalidate prepared candidate: %#v", state.Candidates[0])
	}
}

func TestFinalizationLeaseReplayDoesNotRegate(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("c", 40)
	if _, err := store.Submit(Submit{Branch: "agent/replay", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	_, err := store.withLock(func(path string) (State, error) {
		state, _, err := store.read(path)
		if err != nil {
			return State{}, err
		}
		c := &state.Candidates[0]
		c.Phase, c.Status = Finalizing, Finalizing
		c.BaseSHA, c.TreeSHA, c.ValidatedSHA = "base", "tree", "tree"
		c.GateVersion, c.GatePolicyDigest = "gate", fingerprint("", "", "", "full", "")
		c.DependencyFingerprint = preparedFingerprint(*c)
		c.WorkerID, c.LeaseExpiresAt = "dead", now.Add(-time.Second)
		return state, write(path, state)
	})
	if err != nil {
		t.Fatal(err)
	}
	finalized := 0
	worker := Worker{Store: store, Deps: ProcessDeps{
		Integration: &fakeIntegration{}, Gate: gateFunc(func(context.Context, Speculation) (GateResult, error) {
			t.Fatal("finalization replay must not regate")
			return GateResult{}, nil
		}),
		Finalizer: finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
			finalized++
			return FinalizeResult{OldMainSHA: "base", NewMainSHA: "tree"}, nil
		}),
		Now: func() time.Time { return now },
	}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ := store.List()
	if finalized != 1 || state.Candidates[0].phase() != Landed {
		t.Fatalf("finalized=%d candidate=%#v", finalized, state.Candidates[0])
	}
}

func TestCorruptCheckoutProjectionIsQuarantined(t *testing.T) {
	root := wipRepo(t)
	finalizer := ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"}
	path, err := finalizer.projectionPath("main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, reusable, err := finalizer.reusableProjection(context.Background(), "main"); err != nil || reusable {
		t.Fatalf("reusable=%v err=%v", reusable, err)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("corrupt projection not quarantined: %v", matches)
	}
}

func TestShellGateScrubsProviderSecretsAndTimeoutReleasesCapacity(t *testing.T) {
	root := wipRepo(t)
	t.Setenv("DIGITALOCEAN_API_KEY", "must-not-leak")
	t.Setenv("KITSOKI_WORKER_OUTPUTS_SECRET", "must-not-leak")
	result, err := (ShellGate{Command: `test -z "$DIGITALOCEAN_API_KEY" && test -z "$KITSOKI_WORKER_OUTPUTS_SECRET"`}).Run(
		context.Background(), Speculation{WorkspacePath: root},
	)
	if err != nil || !result.Passed {
		t.Fatalf("scrubbed gate=%#v err=%v", result, err)
	}
	capacity := FileGateCapacity{Root: t.TempDir(), Pool: "timeout", Max: 1}
	worker := Worker{Deps: ProcessDeps{
		GateAdmission: capacity,
		GateTimeout:   time.Nanosecond,
		Gate: gateFunc(func(ctx context.Context, _ Speculation) (GateResult, error) {
			<-ctx.Done()
			return GateResult{}, ctx.Err()
		}),
	}}
	if _, err := worker.runGate(context.Background(), Candidate{ProjectID: "p", TargetRef: "main"}, Speculation{}); err == nil {
		t.Fatal("timed-out gate unexpectedly passed")
	}
	release, err := capacity.Acquire(context.Background(), GateAdmissionRequest{})
	if err != nil {
		t.Fatalf("timed-out child stranded capacity: %v", err)
	}
	release()
}

func TestTwoLandingsReusePendingCheckoutProjectionWithoutBranchChurn(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "one.txt", "one\n", "one")
	one := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "two.txt", "two\n", "two")
	two := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/one", one)
	git(t, root, "branch", "agent/two", two)
	git(t, root, "reset", "--hard", base)
	if err := os.Remove(filepath.Join(root, "base.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "base.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt", "local.txt"), []byte("never lose"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/one", SHA: one, Receipt: persistedReceipt(t, root, one)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submit(Submit{Branch: "agent/two", SHA: two, Receipt: persistedReceipt(t, root, two)}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
		GateVersion: "change/v1",
		TargetRef:   "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "rev-parse", "main"); got != state.Candidates[1].ResultMainSHA {
		t.Fatalf("main=%s want landed result=%s state=%#v", got, state.Candidates[1].ResultMainSHA, state.Candidates)
	}
	git(t, root, "merge-base", "--is-ancestor", two, "main")
	branches := strings.Fields(git(t, root, "for-each-ref", "--format=%(refname:short)", "refs/heads/queue/preserved-wip"))
	if len(branches) != 1 {
		t.Fatalf("preserved branch churn: %v", branches)
	}
	if got := strings.TrimSpace(git(t, root, "show", branches[0]+":base.txt/local.txt")); got != "never lose" {
		t.Fatalf("preserved local bytes=%q", got)
	}
}
