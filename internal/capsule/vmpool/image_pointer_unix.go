//go:build unix

package vmpool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openTrustedImagePointer(path string, ownerUID uint32) (*os.File, int64, error) {
	if err := validateImagePointerParents(path, ownerUID); err != nil {
		return nil, 0, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("vmpool: open worker image pointer %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, 0, fmt.Errorf("vmpool: open worker image pointer %s: invalid file descriptor", path)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("vmpool: stat worker image pointer %s: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = f.Close()
		return nil, 0, fmt.Errorf("vmpool: worker image pointer %s is not a regular file", path)
	}
	if stat.Uid != ownerUID {
		_ = f.Close()
		return nil, 0, fmt.Errorf("vmpool: worker image pointer %s owner uid is %d, want %d", path, stat.Uid, ownerUID)
	}
	if stat.Mode&0o022 != 0 {
		_ = f.Close()
		return nil, 0, fmt.Errorf("vmpool: worker image pointer %s must not be group- or world-writable (mode %04o)", path, stat.Mode&0o7777)
	}
	return f, stat.Size, nil
}

// validateImagePointerParents closes the gap O_NOFOLLOW alone cannot: an
// attacker must not be able to replace the leaf through a writable or
// symlinked parent. For the production ownerUID=0 path every component must
// be root-owned. Tests may admit their non-root owner, while still requiring
// each ancestor to be owned by either root or that owner and never writable
// by group/other.
func validateImagePointerParents(path string, ownerUID uint32) error {
	parent := filepath.Dir(path)
	volume := filepath.VolumeName(parent)
	remainder := strings.TrimPrefix(parent, volume)
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(remainder, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		var stat unix.Stat_t
		if err := unix.Lstat(current, &stat); err != nil {
			return fmt.Errorf("vmpool: stat worker image pointer parent %s: %w", current, err)
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
			return fmt.Errorf("vmpool: worker image pointer parent %s must not be a symlink", current)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("vmpool: worker image pointer parent %s is not a directory", current)
		}
		if stat.Uid != 0 && stat.Uid != ownerUID {
			return fmt.Errorf("vmpool: worker image pointer parent %s owner uid is %d, want root or %d", current, stat.Uid, ownerUID)
		}
		if stat.Mode&0o022 != 0 {
			return fmt.Errorf("vmpool: worker image pointer parent %s must not be group- or world-writable (mode %04o)", current, stat.Mode&0o7777)
		}
	}
	return nil
}
