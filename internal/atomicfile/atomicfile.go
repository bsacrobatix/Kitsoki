// Package atomicfile persists replaceable state files without changing the
// authority boundary established by their containing directory.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes data through a same-directory temporary file and rename.
//
// The containing directory is the authority for the resulting file's owner
// and group. This matters for shared control-plane stores: a privileged CLI
// may write the same state that a less-privileged daemon reads later. Atomic
// rename must not silently transfer that state to the invoking user.
//
// Existing file permissions are preserved. New files use mode. Missing parent
// directories use dirMode and inherit the nearest existing ancestor's owner
// and group.
func WriteFile(path string, data []byte, mode, dirMode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	if dirMode == 0 {
		dirMode = 0o755
	}
	dir := filepath.Dir(path)
	if err := mkdirAllInheriting(dir, dirMode); err != nil {
		return err
	}
	authority, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("atomicfile: inspect authority directory %s: %w", dir, err)
	}
	if existing, statErr := os.Stat(path); statErr == nil {
		mode = existing.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("atomicfile: inspect destination %s: %w", path, statErr)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("atomicfile: create temporary file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err = tmp.Write(data); err == nil {
		err = inheritFileIdentity(tmp, authority)
	}
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("atomicfile: prepare %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("atomicfile: replace %s: %w", path, err)
	}
	return nil
}

func mkdirAllInheriting(path string, mode os.FileMode) error {
	var missing []string
	cursor := filepath.Clean(path)
	for {
		_, err := os.Stat(cursor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("atomicfile: inspect parent %s: %w", cursor, err)
		}
		missing = append(missing, cursor)
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return fmt.Errorf("atomicfile: no existing ancestor for %s", path)
		}
		cursor = parent
	}

	for i := len(missing) - 1; i >= 0; i-- {
		parentInfo, err := os.Stat(filepath.Dir(missing[i]))
		if err != nil {
			return fmt.Errorf("atomicfile: inspect parent for %s: %w", missing[i], err)
		}
		if err := os.Mkdir(missing[i], mode); err != nil {
			if !os.IsExist(err) {
				return fmt.Errorf("atomicfile: create directory %s: %w", missing[i], err)
			}
			continue
		}
		if err := inheritPathIdentity(missing[i], parentInfo); err != nil {
			return err
		}
		if err := os.Chmod(missing[i], mode); err != nil {
			return fmt.Errorf("atomicfile: set directory mode %s: %w", missing[i], err)
		}
	}
	return nil
}
