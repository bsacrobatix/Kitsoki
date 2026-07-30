//go:build darwin

package queue

import "golang.org/x/sys/unix"

func gateSocketPeerPID(fd uintptr) (int, error) {
	return unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}
