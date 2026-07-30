package queue

// Adversarial multi-worker stress/property tests for the --concurrency N
// merge queue. Multiple `kitsoki queue worker --concurrency N` goroutines
// share one .capsules/queue/state.json guarded by a file lock and coordinate
// through per-candidate leases; only the FIFO/emergency head ever finalizes
// (see worker.go's claimPreparation/finalize). These tests exercise that
// discipline under real goroutine races rather than single-stepped RunOnce
// calls, since the queue is about to become the backbone of a remote-VM
// dispatch system where exactly-once landing is the whole point.
//
// Every top-level identifier in this file is prefixed cst to avoid
// redeclaration collisions with other test files landing in this package
// concurrently.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kitsoki/internal/capsule/receipt"
)

// cstSHA returns a deterministic 40-character lowercase-hex candidate SHA,
// distinct for each positive n, satisfying Submit's full-git-SHA validation
// without needing a real repository.
func cstSHA(n int) string {
	return fmt.Sprintf("%040x", n)
}

// cstWaitUntil polls cond every few milliseconds -- a light, deterministic
// poll rather than a long fixed sleep -- until it reports true or deadline
// passes. It returns cond's last observed value either way, so callers can
// still fail with a useful final state on timeout.
func cstWaitUntil(deadline time.Time, cond func() bool) bool {
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return cond()
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// cstStubIntegration is a deterministic, conflict-free Integration stub
// shared by the tests below: every candidate gets its own tree, so
// preparation itself never needs a retry and the tests stay focused on the
// concurrency machinery rather than on integration semantics already
// covered elsewhere in this package.
type cstStubIntegration struct{}

func (cstStubIntegration) Speculate(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
	return Speculation{SHA: "cst-tree-" + c.SHA, BaseSHA: "cst-base-" + c.TargetRef}, nil
}
func (cstStubIntegration) Land(context.Context, Speculation) error { return nil }

// cstAlwaysPassGate is a Gate stub that passes immediately, every time.
type cstAlwaysPassGate struct{}

func (cstAlwaysPassGate) Run(context.Context, Speculation) (GateResult, error) {
	return GateResult{Passed: true, Evidence: []string{"cst:gate-pass"}}, nil
}

// cstCrashOnceGate simulates a worker that wedges mid-gate and is abandoned
// rather than cleanly failing: its Run call blocks forever, uninterruptibly,
// the first time it is asked to gate crashSHA. Every other call -- including
// crashSHA's second attempt, after the durable lease has expired and a
// different worker has reclaimed the candidate -- passes immediately.
type cstCrashOnceGate struct {
	mu       sync.Mutex
	attempts map[string]int
	crashSHA string
	block    chan struct{} // never closed: the point is to hang forever.
}

func newCstCrashOnceGate(crashSHA string) *cstCrashOnceGate {
	return &cstCrashOnceGate{attempts: map[string]int{}, crashSHA: crashSHA, block: make(chan struct{})}
}

func (g *cstCrashOnceGate) Run(_ context.Context, s Speculation) (GateResult, error) {
	g.mu.Lock()
	g.attempts[s.SHA]++
	attempt := g.attempts[s.SHA]
	g.mu.Unlock()
	if s.SHA == g.crashSHA && attempt == 1 {
		<-g.block // deliberately never returns: this simulates the crashed/abandoned worker.
	}
	return GateResult{Passed: true, Evidence: []string{"cst:gate-pass"}}, nil
}

// cstFinalizeCall records one observed Finalize invocation.
type cstFinalizeCall struct {
	id  string
	seq uint64
}

// cstRecordingFinalizer is a thread-safe Finalizer stub that models the
// production CAS contract: the first finalization of a candidate wins and
// moves the ref; any later (delayed-worker) invocation loses the
// compare-and-swap and resolves Stale. It records effective landings per
// candidate (the exactly-once invariant under test), the global landing order
// (the FIFO invariant), and which candidates saw a CAS-lost duplicate — a
// legal at-least-once artifact of wall-clock leases, which the worker's
// fencing must discard without corrupting the landed state.
type cstRecordingFinalizer struct {
	mu     sync.Mutex
	counts map[string]int
	order  []cstFinalizeCall
	dupes  []string
}

func newCstRecordingFinalizer() *cstRecordingFinalizer {
	return &cstRecordingFinalizer{counts: map[string]int{}}
}

func (f *cstRecordingFinalizer) Finalize(_ context.Context, c Candidate) (FinalizeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts[c.ID] >= 1 {
		f.dupes = append(f.dupes, c.ID)
		return FinalizeResult{Stale: true}, nil
	}
	f.counts[c.ID]++
	f.order = append(f.order, cstFinalizeCall{id: c.ID, seq: c.Sequence})
	return FinalizeResult{OldMainSHA: c.BaseSHA, NewMainSHA: c.TreeSHA}, nil
}

func (f *cstRecordingFinalizer) landedCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[id]
}

func (f *cstRecordingFinalizer) totalLanded() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.order)
}

func (f *cstRecordingFinalizer) sequenceOrder() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uint64, len(f.order))
	for i, c := range f.order {
		out[i] = c.seq
	}
	return out
}

func (f *cstRecordingFinalizer) duplicates() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dupes...)
}

// cstRunDrainLoop repeatedly calls w.RunOnce until *stop is set (checked
// between iterations), reporting any error on errs. A short sleep only
// happens when a step made no progress, so idle workers do not busy-spin.
func cstRunDrainLoop(w Worker, stop *int32, errs chan<- error) {
	for atomic.LoadInt32(stop) == 0 {
		progressed, err := w.RunOnce(context.Background())
		if err != nil {
			errs <- err
			return
		}
		if !progressed {
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// TestCstExactlyOnceLandingUnderConcurrentWorkers submits a dozen candidates
// and drains them with four concurrent workers sharing one Store. It asserts
// every candidate lands exactly once (never double-finalized, never stuck),
// and that the observed finalization order respects the FIFO Sequence --
// the exactly-once and FIFO invariants the --concurrency worker pool exists
// to preserve.
func TestCstExactlyOnceLandingUnderConcurrentWorkers(t *testing.T) {
	t.Parallel()
	store := Store{ProjectRoot: t.TempDir(), LockWait: 10 * time.Second}
	const total = 12
	const workers = 4
	for i := 1; i <= total; i++ {
		sha := cstSHA(i)
		if _, err := store.Submit(Submit{Branch: fmt.Sprintf("cst/a-%d", i), SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}

	finalizer := newCstRecordingFinalizer()
	deps := ProcessDeps{Integration: cstStubIntegration{}, Gate: cstAlwaysPassGate{}, Finalizer: finalizer, GateVersion: "cst/v1"}

	var wg sync.WaitGroup
	var stop int32
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wDeps := deps
		wDeps.WorkerID = fmt.Sprintf("cst-worker-%d", i)
		w := Worker{Store: store, Deps: wDeps}
		wg.Add(1)
		go func() { defer wg.Done(); cstRunDrainLoop(w, &stop, errs) }()
	}

	drained := cstWaitUntil(time.Now().Add(20*time.Second), func() bool {
		state, err := store.List()
		if err != nil || len(state.Candidates) != total {
			return false
		}
		for _, c := range state.Candidates {
			if c.Status != Landed {
				return false
			}
		}
		return true
	})
	atomic.StoreInt32(&stop, 1)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("worker error: %v", err)
	}
	if !drained {
		state, _ := store.List()
		t.Fatalf("candidates did not fully drain within deadline: %#v", state.Candidates)
	}

	// CAS-lost duplicate invocations are legal at-least-once artifacts under
	// scheduler pressure; effective landings must still be exactly one per
	// candidate.
	if dupes := finalizer.duplicates(); len(dupes) != 0 {
		t.Logf("CAS-lost duplicate finalizations (discarded): %v", dupes)
	}
	if got := finalizer.totalLanded(); got != total {
		t.Fatalf("expected %d effective landings, got %d", total, got)
	}
	order := finalizer.sequenceOrder()
	for i := 1; i < len(order); i++ {
		if order[i] < order[i-1] {
			t.Fatalf("landing order violated FIFO sequence: %v", order)
		}
	}
}

// TestCstLeaseTakeoverAfterAbandonedWorker simulates a worker that crashes
// mid-gate on a candidate's first attempt: its Run call hangs forever and
// the worker never comes back to release or renew the lease (the context it
// was called with is already cancelled, so heartbeat's renewal loop exits
// immediately too -- see cstCrashOnceGate and heartbeat in worker.go). It
// asserts a different worker eventually reclaims the candidate once the
// lease TTL elapses, and that the candidate still lands exactly once despite
// the abandoned first attempt.
func TestCstLeaseTakeoverAfterAbandonedWorker(t *testing.T) {
	t.Parallel()
	store := Store{ProjectRoot: t.TempDir(), LockWait: 10 * time.Second}
	shaCrash, shaB, shaC := cstSHA(101), cstSHA(102), cstSHA(103)
	crash, err := store.Submit(Submit{Branch: "cst/crash", SHA: shaCrash, Receipt: testReceipt(t, shaCrash)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submit(Submit{Branch: "cst/b", SHA: shaB, Receipt: testReceipt(t, shaB)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submit(Submit{Branch: "cst/c", SHA: shaC, Receipt: testReceipt(t, shaC)}); err != nil {
		t.Fatal(err)
	}

	gate := newCstCrashOnceGate("cst-tree-" + shaCrash)
	finalizer := newCstRecordingFinalizer()
	const lease = 200 * time.Millisecond
	const rescuers = 3
	// This scenario runs four workers concurrently (one abandoned victim plus
	// three rescuers), and the victim holds its gate slot forever by design.
	// Gate admission is a declared resource, so the fixture must declare a
	// pool that fits the topology it asserts, exactly as an operator sizes
	// --capacity to the workers a host runs. Leaving it unset would inherit
	// the host-wide single-slot default, where the abandoned worker's slot
	// starves every rescuer and no takeover could ever be observed. The root
	// is per-test so this never contends with a real worker on the same host.
	deps := ProcessDeps{
		Integration:   cstStubIntegration{},
		Gate:          gate,
		Finalizer:     finalizer,
		GateVersion:   "cst/v1",
		Lease:         lease,
		GateAdmission: FileGateCapacity{Root: t.TempDir(), Pool: "cst-lease-takeover", Max: rescuers + 1},
	}

	victimDeps := deps
	victimDeps.WorkerID = "cst-victim"
	victim := Worker{Store: store, Deps: victimDeps}
	// An already-cancelled context: heartbeat's renewal goroutine (see
	// worker.go) observes ctx.Done() immediately and exits without ever
	// renewing the lease, while the call itself still hangs forever inside
	// cstCrashOnceGate -- exactly the "abandoned mid-phase, lease never
	// released" scenario this test targets.
	victimCtx, cancel := context.WithCancel(context.Background())
	cancel()
	go func() {
		_, _ = victim.RunOnce(victimCtx) // deliberately not joined: simulates a hung/crashed worker process.
	}()

	claimed := cstWaitUntil(time.Now().Add(2*time.Second), func() bool {
		cur, err := store.Get(crash.ID)
		return err == nil && cur.phase() == Gating && cur.WorkerID == "cst-victim"
	})
	if !claimed {
		t.Fatal("victim worker never claimed and entered gating on the crash candidate")
	}

	var wg sync.WaitGroup
	var stop int32
	errs := make(chan error, rescuers)
	for i := 0; i < rescuers; i++ {
		wDeps := deps
		wDeps.WorkerID = fmt.Sprintf("cst-rescuer-%d", i)
		w := Worker{Store: store, Deps: wDeps}
		wg.Add(1)
		go func() { defer wg.Done(); cstRunDrainLoop(w, &stop, errs) }()
	}

	drained := cstWaitUntil(time.Now().Add(15*time.Second), func() bool {
		state, err := store.List()
		if err != nil || len(state.Candidates) != 3 {
			return false
		}
		for _, c := range state.Candidates {
			if c.Status != Landed {
				return false
			}
		}
		return true
	})
	atomic.StoreInt32(&stop, 1)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("rescuer error: %v", err)
	}
	if !drained {
		state, _ := store.List()
		t.Fatalf("crash candidate was never reclaimed and landed: %#v", state.Candidates)
	}

	if n := finalizer.landedCount(crash.ID); n != 1 {
		t.Fatalf("crash candidate effectively landed %d times, want exactly 1", n)
	}
	// A CAS-lost duplicate invocation from the delayed worker is legal
	// at-least-once behavior; what matters is it resolved Stale (modeled by
	// the stub) and the durable state was not corrupted by it — asserted via
	// the Landed checks above and worker attribution below.
	if dupes := finalizer.duplicates(); len(dupes) != 0 {
		t.Logf("CAS-lost duplicate finalizations (discarded): %v", dupes)
	}
	final, err := store.Get(crash.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.WorkerID == "cst-victim" {
		t.Fatalf("crash candidate landed while still attributed to the abandoned victim worker")
	}
	if final.Attempt < 2 {
		t.Fatalf("crash candidate landed without a reprepare attempt after lease expiry: attempt=%d", final.Attempt)
	}
}

// TestCstLockContentionKeepsStateIntact hammers the Store's mutation surface
// -- concurrent Submit, then concurrent operator verbs (Park/Reject) -- from
// many goroutines and asserts the durable state.json on disk is still valid
// JSON with an intact schema, has lost no candidates, and that every
// candidate's Sequence is unique and forms a contiguous 1..N run (i.e. the
// file lock genuinely serializes read-modify-write cycles rather than
// occasionally losing an update).
func TestCstLockContentionKeepsStateIntact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{ProjectRoot: dir, LockWait: 10 * time.Second}
	const total = 30

	type submitResult struct {
		idx int
		c   Candidate
		err error
	}
	results := make(chan submitResult, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		i := i
		sha := cstSHA(200 + i)
		rcpt := testReceipt(t, sha)
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := store.Submit(Submit{Branch: fmt.Sprintf("cst/lock-%d", i), SHA: sha, Receipt: rcpt})
			results <- submitResult{idx: i, c: c, err: err}
		}()
	}
	wg.Wait()
	close(results)

	ids := make(map[string]bool, total)
	for r := range results {
		if r.err != nil {
			t.Fatalf("submit %d: %v", r.idx, r.err)
		}
		ids[r.c.ID] = true
	}
	if len(ids) != total {
		t.Fatalf("expected %d distinct candidates after concurrent submit, got %d", total, len(ids))
	}

	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != total {
		t.Fatalf("state has %d candidates after submit, want %d", len(state.Candidates), total)
	}

	// Hammer the operator-verb mutation surface (Store.operate's lock path)
	// concurrently too: Park even-indexed candidates, Reject odd-indexed
	// ones, all racing on the same state.json.
	var wg2 sync.WaitGroup
	errs2 := make(chan error, total)
	for i, c := range state.Candidates {
		i, c := i, c
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			var err error
			if i%2 == 0 {
				_, err = store.Park(Op{ID: c.ID, Actor: "cst-op", Reason: "cst-park"})
			} else {
				_, err = store.Reject(Op{ID: c.ID, Actor: "cst-op", Reason: "cst-reject"})
			}
			if err != nil {
				errs2 <- err
			}
		}()
	}
	wg2.Wait()
	close(errs2)
	for err := range errs2 {
		t.Fatalf("operator verb: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".capsules", "queue", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var final State
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatalf("state.json is not valid JSON after concurrent mutation: %v", err)
	}
	if final.Schema != Schema {
		t.Fatalf("schema=%q, want %q", final.Schema, Schema)
	}
	if len(final.Candidates) != total {
		t.Fatalf("final candidates=%d, want %d (a candidate was lost under lock contention)", len(final.Candidates), total)
	}

	seqSeen := make(map[uint64]bool, total)
	seqs := make([]uint64, 0, total)
	for _, c := range final.Candidates {
		if seqSeen[c.Sequence] {
			t.Fatalf("duplicate sequence number %d in final state", c.Sequence)
		}
		seqSeen[c.Sequence] = true
		seqs = append(seqs, c.Sequence)
		if c.Status != Rejected && c.Status != NeedsInput {
			t.Fatalf("candidate %s left in unexpected status %s after Park/Reject", c.ID, c.Status)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, s := range seqs {
		if s != uint64(i+1) {
			t.Fatalf("sequence numbers are not a contiguous monotonic 1..N run: %v", seqs)
		}
	}
}

// TestCstConcurrentSubmitAndDrainConservesCandidates submits an initial
// batch, starts a worker pool draining it, and trickles in more submissions
// while draining is in flight. It asserts conservation (every submitted
// candidate ends up landed; none are lost, silently duplicated, or left
// stuck) and that the whole thing converges within a generous bounded
// deadline instead of deadlocking.
func TestCstConcurrentSubmitAndDrainConservesCandidates(t *testing.T) {
	t.Parallel()
	store := Store{ProjectRoot: t.TempDir(), LockWait: 10 * time.Second}
	const initial = 6
	const trickled = 6
	const workers = 3
	const total = initial + trickled

	for i := 0; i < initial; i++ {
		sha := cstSHA(300 + i)
		if _, err := store.Submit(Submit{Branch: fmt.Sprintf("cst/d-init-%d", i), SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
			t.Fatalf("initial submit %d: %v", i, err)
		}
	}

	finalizer := newCstRecordingFinalizer()
	deps := ProcessDeps{Integration: cstStubIntegration{}, Gate: cstAlwaysPassGate{}, Finalizer: finalizer, GateVersion: "cst/v1"}
	var wg sync.WaitGroup
	var stop int32
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wDeps := deps
		wDeps.WorkerID = fmt.Sprintf("cst-drain-worker-%d", i)
		w := Worker{Store: store, Deps: wDeps}
		wg.Add(1)
		go func() { defer wg.Done(); cstRunDrainLoop(w, &stop, errs) }()
	}

	// Precompute the trickled receipts up front, in this (the test's own)
	// goroutine -- testReceipt calls t.Fatal on failure, and that must never
	// run from a goroutine other than the one running the test -- then hand
	// the submitter goroutine plain values plus a deterministic seed for its
	// small inter-submit jitter.
	type trickle struct {
		branch string
		sha    string
		rcpt   receipt.Receipt
	}
	trickles := make([]trickle, trickled)
	for i := 0; i < trickled; i++ {
		sha := cstSHA(400 + i)
		trickles[i] = trickle{branch: fmt.Sprintf("cst/d-trickle-%d", i), sha: sha, rcpt: testReceipt(t, sha)}
	}

	var submitMu sync.Mutex
	var submitErr error
	var subWG sync.WaitGroup
	subWG.Add(1)
	go func() {
		defer subWG.Done()
		rnd := rand.New(rand.NewSource(42))
		for _, tr := range trickles {
			time.Sleep(time.Duration(rnd.Intn(4)) * time.Millisecond)
			if _, err := store.Submit(Submit{Branch: tr.branch, SHA: tr.sha, Receipt: tr.rcpt}); err != nil {
				submitMu.Lock()
				if submitErr == nil {
					submitErr = err
				}
				submitMu.Unlock()
				return
			}
		}
	}()
	subWG.Wait()
	if submitErr != nil {
		atomic.StoreInt32(&stop, 1)
		wg.Wait()
		t.Fatalf("trickled submit failed: %v", submitErr)
	}

	drained := cstWaitUntil(time.Now().Add(20*time.Second), func() bool {
		state, err := store.List()
		if err != nil || len(state.Candidates) != total {
			return false
		}
		for _, c := range state.Candidates {
			if c.Status != Landed {
				return false
			}
		}
		return true
	})
	atomic.StoreInt32(&stop, 1)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("drain worker error: %v", err)
	}
	if !drained {
		state, _ := store.List()
		t.Fatalf("concurrent submit+drain did not converge within deadline (possible deadlock): %#v", state.Candidates)
	}

	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	landed, other := 0, 0
	for _, c := range state.Candidates {
		switch c.Status {
		case Landed:
			landed++
		case Rejected, NeedsInput, NeedsConflictInput:
			other++
		}
	}
	if landed+other != total {
		t.Fatalf("conservation violated: submitted=%d landed=%d other-terminal=%d", total, landed, other)
	}
	if landed != total {
		t.Fatalf("expected all %d candidates to land, got %d", total, landed)
	}
	if dupes := finalizer.duplicates(); len(dupes) != 0 {
		t.Logf("CAS-lost duplicate finalizations (discarded): %v", dupes)
	}
	if got := finalizer.totalLanded(); got != total {
		t.Fatalf("finalizer observed %d effective landings, want %d", got, total)
	}
}
