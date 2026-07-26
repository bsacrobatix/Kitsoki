// trace_export.go — implements `kitsoki trace export`.
//
// Sessions that run on the Postgres backend (--db-backend postgres |
// embedded-postgres) persist their events in the full-fidelity pg events
// table instead of a JSONL trace file. `kitsoki trace export` reconstructs
// the canonical JSONL trace (docs/tracing/trace-format.md) from the
// configured store so the JSONL consumers — `kitsoki trace`, `trace to-flow`,
// `trace status`, the mining pipeline — keep working for those sessions. It
// works against the default SQLite backend too; SQLite just persists lower
// event fidelity (see store.ExportTraceJSONL for the documented divergences).
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

func traceExportCmd() *cobra.Command {
	var (
		sessionID string
		outPath   string
		appFilter string
		dbPath    string
	)

	cmd := &cobra.Command{
		Use:   "export --session <id> [--out <path>] [--app <id>]",
		Short: "Reconstruct the canonical JSONL trace for a stored session",
		Long: `Reconstruct the canonical JSONL trace file for a session from the configured
session store (--db-backend / KITSOKI_DB_BACKEND; SQLite by default).

This is the bridge from the database-backed session log back to every JSONL
consumer: 'kitsoki trace', 'trace to-flow', 'trace status', and the mining
pipeline. The output is byte-compatible with what the live JSONL sink would
have written — same header shape, same per-event encoding and field order,
deterministic (same stored session, same bytes) — with the divergences
documented on store.ExportTraceJSONL (header written_at is the session's
started_at; timestamps are microsecond precision; fields a backend did not
persist are omitted, never invented).

SESSION RESOLUTION: --session takes a full session id, or a unique id prefix
when --app names the app to search within.

EXAMPLES:
  kitsoki trace export --session 7ca57b33-... --out /tmp/session.jsonl
  kitsoki trace export --session 7ca57b33 --app kitsoki-dev   # prefix + app, stdout
  kitsoki trace export --session <id> | kitsoki trace -        # pretty-print`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(sessionID) == "" {
				return fmt.Errorf("--session is required")
			}

			s, err := openSessionStore(dbPath)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			sid, err := resolveExportSession(cmd, s, sessionID, appFilter)
			if err != nil {
				return err
			}

			if outPath == "" || outPath == "-" {
				_, err := store.ExportTraceJSONL(cmd.Context(), s, sid, cmd.OutOrStdout())
				return err
			}

			f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("create %q: %w", outPath, err)
			}
			n, err := store.ExportTraceJSONL(cmd.Context(), s, sid, f)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote trace %s (%d events)\n", outPath, n)
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "session id (or unique prefix with --app) to export (required)")
	cmd.Flags().StringVar(&outPath, "out", "", "output path for the JSONL trace (default: stdout)")
	cmd.Flags().StringVar(&appFilter, "app", "", "app id to resolve a --session prefix within")
	cmd.Flags().StringVar(&dbPath, "db", "", "session database (default: $XDG_DATA_HOME/kitsoki/sessions.db)")

	return cmd
}

// resolveExportSession resolves the --session argument: an exact id wins; a
// prefix is resolved against --app's sessions (ListSessions has no cross-app
// enumeration, so prefix resolution requires --app). Ambiguity is an error
// listing the candidates.
func resolveExportSession(cmd *cobra.Command, s store.Store, arg, appFilter string) (app.SessionID, error) {
	if _, err := s.GetSession(cmd.Context(), app.SessionID(arg)); err == nil {
		return app.SessionID(arg), nil
	} else if !errors.Is(err, store.ErrSessionNotFound) {
		return "", err
	}

	if appFilter == "" {
		return "", fmt.Errorf("session %q not found (pass the full id, or --app <id> to resolve a prefix)", arg)
	}
	sums, err := s.ListSessions(cmd.Context(), appFilter, 0)
	if err != nil {
		return "", err
	}
	var matches []app.SessionID
	for _, sum := range sums {
		if strings.HasPrefix(string(sum.ID), arg) {
			matches = append(matches, sum.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session with id prefix %q under app %q", arg, appFilter)
	case 1:
		// Name the resolved id (on stderr, so piped stdout stays clean).
		fmt.Fprintf(cmd.ErrOrStderr(), "# session %s\n", matches[0])
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = string(m)
		}
		return "", fmt.Errorf("session prefix %q is ambiguous under app %q: %s", arg, appFilter, strings.Join(ids, ", "))
	}
}
