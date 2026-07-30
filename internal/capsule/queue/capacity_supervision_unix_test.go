//go:build darwin || linux

package queue

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGateOwnerCrashWithChildHelper(t *testing.T) {
	if os.Getenv("KITSOKI_TEST_GATE_OWNER_CRASH_HELPER") != "1" {
		return
	}
	lease, err := (FileGateCapacity{
		Root: os.Getenv("KITSOKI_TEST_CAPACITY_ROOT"),
		Pool: os.Getenv("KITSOKI_TEST_CAPACITY_POOL"),
		Max:  1,
	}).AcquireLease(context.Background(), GateAdmissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	child := exec.Command("sh", "-c", `echo $$ > "$KITSOKI_TEST_CHILD_PID"; exec sleep 30`)
	child.Env = os.Environ()
	if err := lease.RunCommand(context.Background(), child); err != nil {
		t.Fatal(err)
	}
}

func TestGateOwnerSIGKILLReleasesCapacityAndTerminatesOrphanGroup(t *testing.T) {
	root, pool := t.TempDir(), "owner-crash"
	pidPath := root + "/child.pid"
	owner := exec.Command(os.Args[0], "-test.run=^TestGateOwnerCrashWithChildHelper$")
	owner.Env = append(os.Environ(),
		"KITSOKI_TEST_GATE_OWNER_CRASH_HELPER=1",
		"KITSOKI_TEST_CAPACITY_ROOT="+root,
		"KITSOKI_TEST_CAPACITY_POOL="+pool,
		"KITSOKI_TEST_CHILD_PID="+pidPath,
	)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if owner.Process != nil {
			_ = owner.Process.Kill()
		}
		_ = owner.Wait()
	}()
	childPID := waitForPIDFile(t, pidPath, 3*time.Second)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := (FileGateCapacity{Root: root, Pool: pool, Max: 1}).AcquireLease(ctx, GateAdmissionRequest{})
	if err != nil {
		t.Fatalf("owner crash stranded capacity: %v", err)
	}
	elapsed := time.Since(start)
	lease.Release()
	if elapsed >= time.Second {
		t.Fatalf("second capacity acquisition took %s, want under 1s", elapsed)
	}
	waitForProcessExit(t, childPID, 3*time.Second)
}

func TestUnrelatedLiveMarkerCannotBorrowARealOwnersCapacity(t *testing.T) {
	root, pool := t.TempDir(), "spoof"
	ready := filepath.Join(root, "owner.ready")
	owner := exec.Command(os.Args[0], "-test.run=^TestFileGateCapacityCrashHelper$")
	owner.Env = append(os.Environ(),
		"KITSOKI_TEST_CAPACITY_CRASH_HELPER=1",
		"KITSOKI_TEST_CAPACITY_ROOT="+root,
		"KITSOKI_TEST_CAPACITY_POOL="+pool,
		"KITSOKI_TEST_CAPACITY_READY="+ready,
	)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if owner.Process != nil {
			_ = owner.Process.Kill()
		}
		_ = owner.Wait()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !testFileExists(ready) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !testFileExists(ready) {
		t.Fatal("real capacity owner never became ready")
	}
	paths, err := filepath.Glob(filepath.Join(root, "gate-capacity", "*", "slot-000.custody"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("custody path discovery=%v err=%v", paths, err)
	}

	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeRead.Close()
	defer pipeWrite.Close()
	assertSpoofedMarkerWaits(t, root, pool, paths[0], pipeRead, owner.Process.Pid)

	socketOwner, socketChild, err := newGateLivenessSocket()
	if err != nil {
		t.Fatal(err)
	}
	defer socketOwner.Close()
	defer socketChild.Close()
	// The socket peer and declaration agree on this process, but the kernel
	// custody lock identifies the separate real owner. The three-way binding
	// must reject the otherwise-live marker and wait on actual capacity.
	assertSpoofedMarkerWaits(t, root, pool, paths[0], socketChild, os.Getpid())
}

func assertSpoofedMarkerWaits(t *testing.T, root, pool, custodyPath string, marker *os.File, declaredPID int) {
	t.Helper()
	t.Setenv("KITSOKI_GATE_CAPACITY_ROOT", root)
	t.Setenv("KITSOKI_GATE_CAPACITY_POOL", pool)
	t.Setenv("KITSOKI_GATE_CAPACITY_FD", strconv.FormatUint(uint64(marker.Fd()), 10))
	t.Setenv("KITSOKI_GATE_CAPACITY_OWNER_PID", strconv.Itoa(declaredPID))
	t.Setenv("KITSOKI_GATE_CAPACITY_CUSTODY_PATH", custodyPath)
	if borrowed, ok := inheritedGateBorrow(root, pool, filepath.Dir(custodyPath)); ok {
		peerPID, peerErr := gateSocketPeerPID(marker.Fd())
		custodyPID, custodyErr := gateCustodyOwner(custodyPath)
		t.Fatalf("spoof marker authenticated: marker=%+v peer=%d/%v custody=%d/%v", borrowed, peerPID, peerErr, custodyPID, custodyErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	lease, err := (FileGateCapacity{Root: root, Pool: pool, Max: 1, PollInterval: 5 * time.Millisecond}).AcquireLease(ctx, GateAdmissionRequest{})
	if lease != nil {
		lease.Release()
		t.Fatal("unrelated live marker bypassed held capacity")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("spoofed marker acquisition error=%v, want deadline wait", err)
	}
	if elapsed := time.Since(start); elapsed < 125*time.Millisecond {
		t.Fatalf("spoofed marker returned after %s instead of waiting on capacity", elapsed)
	}
}

func TestGateContextCancellationTerminatesProcessGroupAndDoesNotLeakSlot(t *testing.T) {
	root, pool := t.TempDir(), "cancel"
	pidPath := root + "/child.pid"
	capacity := FileGateCapacity{Root: root, Pool: pool, Max: 1}
	lease, err := capacity.AcquireLease(context.Background(), GateAdmissionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	child := exec.Command("sh", "-c", `echo $$ > "$KITSOKI_TEST_CHILD_PID"; trap '' TERM; while :; do sleep 1; done`)
	child.Env = append(os.Environ(), "KITSOKI_TEST_CHILD_PID="+pidPath)
	done := make(chan error, 1)
	go func() { done <- lease.RunCommand(ctx, child) }()
	childPID := waitForPIDFile(t, pidPath, 3*time.Second)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled gate error=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled gate did not stop promptly")
	}
	waitForProcessExit(t, childPID, 3*time.Second)
	lease.Release()

	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), time.Second)
	defer acquireCancel()
	next, err := capacity.AcquireLease(acquireCtx, GateAdmissionRequest{})
	if err != nil {
		t.Fatalf("cancelled gate leaked capacity slot: %v", err)
	}
	next.Release()
}

func waitForPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child PID at %s", path)
	return 0
}

func waitForProcessExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d survived gate supervision timeout", pid)
}
