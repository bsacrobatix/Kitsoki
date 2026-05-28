// Package store — trace_path.go: default on-disk path for per-session JSONL traces.
//
// The path scheme follows proposal §1 "Resolutions" item 3:
//
//	~/.kitsoki/sessions/<app>/<sha8>-<slug>.jsonl
//
// where:
//   - sha8 is the first 8 hex chars of sha256(transport:thread)
//   - slug is transport:thread with '/' and other unsafe path characters
//     replaced by '-', giving a human-readable suffix for `ls` output.
package store

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// slugUnsafe matches characters that are unsafe or inconvenient in a file-system
// path component.  We keep alphanumerics, '.', '_', and '-' (the last already
// being our replacement character).
var slugUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._\-]`)

// DefaultTracePath returns the canonical on-disk path for a JSONL session trace
// keyed by (app, transport, thread).
//
// Path:  ~/.kitsoki/sessions/<app>/<sha8>-<slug>.jsonl
//
//   - <app>  is the app identifier (from AppDef.App.ID); sanitised with the same
//     slug rules as the thread component.
//   - <sha8> is the first 8 hex chars of sha256(transport + ":" + thread), giving
//     collision-safety and a fixed-width leading column.
//   - <slug> is transport + ":" + thread with unsafe characters replaced by '-'.
//
// The parent directory is NOT created by this function; callers are responsible
// for calling os.MkdirAll before opening the trace.
func DefaultTracePath(app, transport, thread string) string {
	key := transport + ":" + thread
	sum := sha256.Sum256([]byte(key))
	sha8 := fmt.Sprintf("%x", sum[:4]) // 4 bytes → 8 hex chars

	slug := slugUnsafe.ReplaceAllString(key, "-")
	appSlug := slugUnsafe.ReplaceAllString(app, "-")

	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback: use a path relative to /tmp so the caller still gets a
		// deterministic, writable location even without a home directory.
		home = os.TempDir()
	}
	return filepath.Join(home, ".kitsoki", "sessions", appSlug, sha8+"-"+slug+".jsonl")
}
