//go:build !windows

package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type gateBorrowMarker struct {
	file *os.File
}

var inheritedGateBorrowFiles sync.Map
var inheritedGateBorrowFilesMu sync.Mutex

func inheritedGateBorrow(root, pool string) (gateBorrowMarker, bool) {
	if os.Getenv("KITSOKI_GATE_CAPACITY_ROOT") != root ||
		os.Getenv("KITSOKI_GATE_CAPACITY_POOL") != pool {
		return gateBorrowMarker{}, false
	}
	fd, err := strconv.Atoi(os.Getenv("KITSOKI_GATE_CAPACITY_FD"))
	if err != nil || fd < 3 {
		return gateBorrowMarker{}, false
	}
	key := strconv.Itoa(fd) + "\x00" + root + "\x00" + pool
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
	marker := gateBorrowMarker{file: file}
	if !ok || !gateBorrowAlive(marker) {
		return gateBorrowMarker{}, false
	}
	return marker, true
}

func gateBorrowAlive(marker gateBorrowMarker) bool {
	if marker.file == nil {
		return false
	}
	info, err := marker.file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
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
has_parent=$1
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
if [ "$has_parent" = 1 ]; then
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
	localRead, localWrite, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("queue: create gate liveness pipe: %w", err)
	}
	defer localWrite.Close()
	defer localRead.Close()

	hasParent := "0"
	extra := []*os.File{localRead}
	if lease.inherited {
		if !gateBorrowAlive(lease.borrow) {
			return fmt.Errorf("queue: inherited gate capacity owner is no longer alive")
		}
		hasParent = "1"
		extra = append(extra, lease.borrow.file)
	}
	env, err := lease.commandEnv(child.Env, 3)
	if err != nil {
		return err
	}
	args := []string{"-c", gateSupervisorScript, "kitsoki-gate-supervisor", hasParent, child.Path}
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
	_ = localRead.Close()

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
