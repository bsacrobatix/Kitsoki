package pgverify

import (
	"fmt"
	"os"
)

// fileSnapshot is the stat-derived facts CheckSQLiteInert compares
// before/after. Captured via os.Stat so the check works unmodified against
// both directions of the property (present-and-inert vs. actively-written).
type fileSnapshot struct {
	Exists  bool
	Size    int64
	ModTime int64 // unix nanoseconds
}

// statFile is the (overridable-in-tests) filesystem probe. A package-level
// var rather than an Options field because it is a pure filesystem call with
// no meaningful per-run configuration — tests substitute it directly.
var statFile = func(path string) (fileSnapshot, error) {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fileSnapshot{Exists: false}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{Exists: true, Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}, nil
}

// CheckSQLiteInert proves the legacy SQLite session-store file at path is
// NOT being actively written, by statting it (and its -wal sidecar, which
// SQLite's WAL mode writes to before checkpointing into the main file) both
// before and after a real wall-clock window, bracketed by the caller around
// something that definitely writes application data (CheckRoundTrip).
//
// waitWindow is a blocking call to opts.sleep — pass the actual Options so
// tests can fake the wait instantly instead of a real multi-second sleep.
//
// Distinguishes three outcomes:
//   - absent (both runs): StatusPass — nothing exists to write to; the file
//     was fully retired, not silently still in use.
//   - present, size/mtime unchanged across the window (main file AND -wal):
//     StatusPass — present but inert.
//   - present, size or mtime changed (either file): StatusFail — actively
//     written, meaning the legacy SQLite path is still receiving writes
//     while Postgres is supposedly the backend.
func CheckSQLiteInert(path string, before func() (fileSnapshot, fileSnapshot, error), opts Options) CheckResult {
	const id = "sqlite_inert"
	const name = "Legacy SQLite session store is not being actively written"

	if path == "" {
		return notEstablished(id, name, "no --sqlite-path given; cannot check a path that was never named", nil)
	}

	mainBefore, walBefore, err := before()
	if err != nil {
		return notEstablished(id, name, fmt.Sprintf("stat before window: %v", err), map[string]any{"path": path})
	}

	opts.sleep(opts.sqliteSettleWindow())

	mainAfter, err := statFile(path)
	if err != nil {
		return notEstablished(id, name, fmt.Sprintf("stat after window: %v", err), map[string]any{"path": path})
	}
	walAfter, err := statFile(path + "-wal")
	if err != nil {
		return notEstablished(id, name, fmt.Sprintf("stat -wal after window: %v", err), map[string]any{"path": path + "-wal"})
	}

	evidence := map[string]any{
		"path":               path,
		"window_seconds":     opts.sqliteSettleWindow().Seconds(),
		"main_exists_before": mainBefore.Exists,
		"main_exists_after":  mainAfter.Exists,
		"main_size_before":   mainBefore.Size,
		"main_size_after":    mainAfter.Size,
		"main_mtime_before":  mainBefore.ModTime,
		"main_mtime_after":   mainAfter.ModTime,
		"wal_exists_before":  walBefore.Exists,
		"wal_exists_after":   walAfter.Exists,
		"wal_size_before":    walBefore.Size,
		"wal_size_after":     walAfter.Size,
		"wal_mtime_before":   walBefore.ModTime,
		"wal_mtime_after":    walAfter.ModTime,
	}

	if !mainBefore.Exists && !mainAfter.Exists {
		return pass(id, name, "sqlite file absent both before and after the window — fully retired, no write surface exists", evidence)
	}

	changed := mainBefore != mainAfter || walBefore != walAfter
	if changed {
		return fail(id, name, "sqlite file (or its -wal sidecar) changed size/mtime across the window that definitely wrote application data through the service — SQLite is still being actively written", evidence)
	}
	return pass(id, name, "sqlite file present but its size/mtime (and its -wal sidecar) were unchanged across a window that definitely wrote application data through the service — present but inert", evidence)
}

// StatSQLiteFiles is the default "before" snapshot function CheckSQLiteInert
// expects: stats path and path+"-wal". Exported so cmd/kitsoki/db_verify.go
// does not need to know CheckSQLiteInert's internal fileSnapshot type to
// wire up the real filesystem.
func StatSQLiteFiles(path string) func() (fileSnapshot, fileSnapshot, error) {
	return func() (fileSnapshot, fileSnapshot, error) {
		main, err := statFile(path)
		if err != nil {
			return fileSnapshot{}, fileSnapshot{}, err
		}
		wal, err := statFile(path + "-wal")
		if err != nil {
			return fileSnapshot{}, fileSnapshot{}, err
		}
		return main, wal, nil
	}
}
