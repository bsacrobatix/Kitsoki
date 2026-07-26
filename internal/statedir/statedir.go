// Package statedir resolves the single runtime state root for deployments
// that must confine every mutable kitsoki location to one writable mount
// (e.g. a read-only-rootfs container with one volume).
//
// When KITSOKI_STATE_DIR is set (or the persistent --state-dir flag, which
// exports it), every default runtime-writable location re-roots under it:
//
//	<state>/sessions            per-session JSONL traces + sidecars
//	                            (transcripts, frames, annotations are
//	                            co-located with the trace path)
//	<state>/sessions.db         default SQLite sessions database
//	<state>/pg                  embedded-postgres data dir (derived from
//	                            dir(sessions.db)/pg, so it follows the db)
//	<state>/cache/embedded-pg   embedded-postgres binary cache
//	<state>/graph-mcp           graph-mcp feedback + receipts ledgers
//
// When unset, nothing changes: callers keep their historical defaults
// byte-for-byte (~/.kitsoki/sessions, $XDG_DATA_HOME/kitsoki/sessions.db,
// <repo>/.artifacts/graph-mcp, os.UserCacheDir()/kitsoki/embedded-pg).
//
// This is deliberately not a config framework: it is one env-var lookup that
// the handful of existing path seams consult.
package statedir

import (
	"os"
	"strings"
)

// EnvStateDir is the environment variable naming the runtime state root.
const EnvStateDir = "KITSOKI_STATE_DIR"

// Root returns the configured runtime state root and true when
// KITSOKI_STATE_DIR is set to a non-blank value; otherwise ("", false) and
// callers use their historical default paths unchanged.
func Root() (string, bool) {
	dir := strings.TrimSpace(os.Getenv(EnvStateDir))
	if dir == "" {
		return "", false
	}
	return dir, true
}
