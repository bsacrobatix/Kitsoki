//go:build darwin || linux

package queue

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquireGateCustody(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("queue: open gate custody lock: %w", err)
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	if err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("queue: acquire gate custody lock: %w", ErrBusy)
		}
		return nil, fmt.Errorf("queue: acquire gate custody lock: %w", err)
	}
	return file, nil
}

func gateCustodyOwner(path string) (int, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	for {
		err = unix.FcntlFlock(file.Fd(), unix.F_GETLK, &lock)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		break
	}
	if err != nil {
		return 0, err
	}
	if lock.Type == unix.F_UNLCK || lock.Pid <= 1 {
		return 0, ErrBusy
	}
	return int(lock.Pid), nil
}

func newGateLivenessSocket() (*os.File, *os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	owner := os.NewFile(uintptr(pair[0]), "gate-liveness-owner")
	child := os.NewFile(uintptr(pair[1]), "gate-liveness-child")
	if owner == nil || child == nil {
		if owner != nil {
			_ = owner.Close()
		} else {
			_ = unix.Close(pair[0])
		}
		if child != nil {
			_ = child.Close()
		} else {
			_ = unix.Close(pair[1])
		}
		return nil, nil, fmt.Errorf("queue: wrap gate liveness socket")
	}
	return owner, child, nil
}
