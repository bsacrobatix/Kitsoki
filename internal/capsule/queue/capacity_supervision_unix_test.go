//go:build !windows

package queue

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
