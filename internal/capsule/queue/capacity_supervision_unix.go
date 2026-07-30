//go:build darwin || linux

package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type gateBorrowMarker struct {
	file        *os.File
	ownerPID    int
	custodyPath string
}

var inheritedGateBorrowFiles sync.Map
var inheritedGateBorrowFilesMu sync.Mutex

func inheritedGateBorrow(root, pool, dir string) (gateBorrowMarker, bool) {
	if os.Getenv("KITSOKI_GATE_CAPACITY_ROOT") != root ||
		os.Getenv("KITSOKI_GATE_CAPACITY_POOL") != pool {
		return gateBorrowMarker{}, false
	}
	fd, err := strconv.Atoi(os.Getenv("KITSOKI_GATE_CAPACITY_FD"))
	if err != nil || fd < 3 {
		return gateBorrowMarker{}, false
	}
	ownerPID, err := strconv.Atoi(os.Getenv("KITSOKI_GATE_CAPACITY_OWNER_PID"))
	if err != nil || ownerPID <= 1 {
		return gateBorrowMarker{}, false
	}
	custodyPath := filepath.Clean(os.Getenv("KITSOKI_GATE_CAPACITY_CUSTODY_PATH"))
	rel, err := filepath.Rel(dir, custodyPath)
	if err != nil || filepath.Dir(rel) != "." || !validGateCustodyName(rel) {
		return gateBorrowMarker{}, false
	}
	key := strconv.Itoa(fd) + "\x00" + root + "\x00" + pool + "\x00" +
		strconv.Itoa(ownerPID) + "\x00" + custodyPath
	inheritedGateBorrowFilesMu.Lock()
	defer inheritedGateBorrowFilesMu.Unlock()
	value, ok := inheritedGateBorrowFiles.Load(key)
	if !ok {
		file := os.NewFile(uintptr(fd), "inherited-gate-liveness")
		if file == nil {
			return gateBorrowMarker{}, false
		}
		inheritedGateBorrowFiles.Store(key, file)
		value = file
	}
	file, ok := value.(*os.File)
	marker := gateBorrowMarker{file: file, ownerPID: ownerPID, custodyPath: custodyPath}
	if !ok || !gateBorrowAlive(marker) {
		return gateBorrowMarker{}, false
	}
	return marker, true
}

func validGateCustodyName(name string) bool {
	if len(name) != len("slot-000.custody") ||
		!strings.HasPrefix(name, "slot-") ||
		!strings.HasSuffix(name, ".custody") {
		return false
	}
	for _, ch := range name[len("slot-"):len("slot-000")] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func gateBorrowAlive(marker gateBorrowMarker) bool {
	if marker.file == nil || marker.ownerPID <= 1 || !filepath.IsAbs(marker.custodyPath) {
		return false
	}
	info, err := marker.file.Stat()
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	peerPID, err := gateSocketPeerPID(marker.file.Fd())
	if err != nil || peerPID != marker.ownerPID {
		return false
	}
	custodyPID, err := gateCustodyOwner(marker.custodyPath)
	if err != nil || custodyPID != marker.ownerPID {
		return false
	}
	for {
		poll := []unix.PollFd{{Fd: int32(marker.file.Fd()), Events: unix.POLLIN | unix.POLLHUP}}
		n, err := unix.Poll(poll, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil && (n == 0 || poll[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) == 0)
	}
}

const gateSupervisorScript = `
has_second_marker=$1
shift
(
	trap '' TERM
	IFS= read -r _ <&3
	kill -TERM -$$ 2>/dev/null || true
	sleep 0.20
	kill -KILL -$$ 2>/dev/null || true
) &
local_watch=$!
parent_watch=
if [ "$has_second_marker" = 1 ]; then
	(
		trap '' TERM
		IFS= read -r _ <&4
		kill -TERM -$$ 2>/dev/null || true
		sleep 0.20
		kill -KILL -$$ 2>/dev/null || true
	) &
	parent_watch=$!
fi
"$@" &
child=$!
wait "$child"
status=$?
kill -KILL "$local_watch" 2>/dev/null || true
if [ -n "$parent_watch" ]; then
	kill -KILL "$parent_watch" 2>/dev/null || true
fi
wait "$local_watch" 2>/dev/null || true
if [ -n "$parent_watch" ]; then
	wait "$parent_watch" 2>/dev/null || true
fi
exit "$status"
`

func runSupervisedGateCommand(ctx context.Context, child *exec.Cmd, lease *FileGateLease) error {
	if len(child.ExtraFiles) != 0 {
		return fmt.Errorf("queue: gate command cannot supply inherited files")
	}
	localOwner, localChild, err := newGateLivenessSocket()
	if err != nil {
		return fmt.Errorf("queue: create gate liveness socket: %w", err)
	}
	defer localOwner.Close()
	defer localChild.Close()

	hasSecondMarker := "0"
	extra := []*os.File{localChild}
	if lease.inherited {
		if !gateBorrowAlive(lease.borrow) {
			return fmt.Errorf("queue: inherited gate capacity owner is no longer alive")
		}
		// FD 3 remains the original kernel-authenticated owner marker so
		// arbitrarily deep nesting continues to bind to the custody lock owner.
		// FD 4 watches this immediate wrapper and kills its command group if the
		// wrapper itself disappears while the original owner remains alive.
		hasSecondMarker = "1"
		extra = []*os.File{lease.borrow.file, localChild}
	}
	env, err := lease.commandEnv(child.Env, 3)
	if err != nil {
		return err
	}
	args := []string{"-c", gateSupervisorScript, "kitsoki-gate-supervisor", hasSecondMarker, child.Path}
	if len(child.Args) > 1 {
		args = append(args, child.Args[1:]...)
	}
	supervisor := exec.Command("/bin/sh", args...)
	supervisor.Dir = child.Dir
	supervisor.Env = env
	supervisor.Stdin, supervisor.Stdout, supervisor.Stderr = child.Stdin, child.Stdout, child.Stderr
	supervisor.ExtraFiles = extra
	supervisor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := supervisor.Start(); err != nil {
		return fmt.Errorf("queue: start supervised gate: %w", err)
	}
	_ = localChild.Close()

	wait := make(chan error, 1)
	go func() { wait <- supervisor.Wait() }()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-supervisor.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-wait:
			timer.Stop()
		case <-timer.C:
			_ = syscall.Kill(-supervisor.Process.Pid, syscall.SIGKILL)
			<-wait
		}
		return ctx.Err()
	}
}
