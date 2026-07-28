// db_verify.go — `kitsoki db verify`, the deployed-Postgres-backend proof
// tool. Where `kitsoki db migrate` MOVES data from SQLite into Postgres,
// `kitsoki db verify` PROVES a deployment is genuinely running on the
// Postgres backend it claims to be — see internal/pgverify's package doc for
// the full argument for why "--db-backend postgres is in the systemd
// ExecStart" is not evidence.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/dbruntime"
	"kitsoki/internal/pgverify"
	"kitsoki/internal/store"
)

// verifierProbeApplicationName / verifierServiceApplicationName are the
// application_name values this command's OWN two Postgres connections
// (raw-query probe, and the store.Store the round-trip/concurrency checks
// write through) are stamped with. Both are deliberately distinct from
// store.ApplicationName ("kitsoki", the real deployed service's identity),
// so CheckBackendIdentity can never mistake this verification run's own
// connections for the thing it is trying to verify.
const (
	verifierProbeApplicationName   = "kitsoki-pgverify-probe"
	verifierServiceApplicationName = "kitsoki-pgverify-service"
)

func dbVerifyCmd() *cobra.Command {
	var (
		pgDSN                 string
		sqlitePath            string
		sqliteSettleWindow    time.Duration
		expectCounts          []string
		sampleSessionIDs      []string
		concurrencyWriters    int
		concurrencyAppends    int
		concurrencyEvents     int
		assumeExclusiveWindow bool
		keepTestData          bool
		outputPath            string
	)

	cmd := &cobra.Command{
		Use: "verify",
		// A failing report is a normal, expected outcome carried by
		// errVerifyFailed with the detail already printed via the JSON
		// artifact and the summary table — main() translates it to the
		// right exit code without cobra's redundant "Error: " banner.
		SilenceErrors: true,
		SilenceUsage:  true,
		Short:         "Prove a deployed kitsoki host is genuinely running on Postgres and is functionally correct",
		Long: `Proves — rather than merely asserts — that a running kitsoki deployment is
genuinely persisting to its configured Postgres backend and is functionally
correct on it. Every check fails closed: anything this command cannot
independently observe is reported "not_established" and makes the whole run
exit non-zero, never silently green.

Checks (see internal/pgverify package doc for the full rationale of each):
  1. backend_identity          — a live connection with application_name=kitsoki
                                  is observed directly in pg_stat_activity.
  2. write_read_roundtrip      — creates a real, namespaced session through
                                  store.Store, appends an event, reads it back.
  3. direct_postgres_rows      — re-derives the same rows via raw SQL that
                                  never touches internal/store.
  4. sqlite_inert              — --sqlite-path (and its -wal sidecar) are
                                  unchanged across a window that definitely
                                  wrote application data.
  5. migrated_data_intact      — --expect-count / --sample-session-id are
                                  checked against Postgres directly and
                                  spot-read through the service.
  6. stream_pos_monotonicity   — concurrent appends are tailed for no gap,
                                  no duplicate.
  7. type_fidelity             — a BYTEA payload and a microsecond-precision
                                  timestamp round-trip exactly.

All test data this command creates is namespaced under pgverify.MarkerPrefix
and deleted afterwards (pass --keep-test-data to leave it for inspection).
Never mutates or deletes any pre-existing row.

--pg-dsn is required (falls back to --pg-dsn / KITSOKI_PG_DSN set at the root
command, then the KITSOKI_PG_DSN environment variable). This command opens
its OWN two connections to that DSN — distinct application_name values from
the deployed service's — so it can tell its own traffic apart from the
service's when checking backend_identity.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dsn := strings.TrimSpace(pgDSN)
			if dsn == "" {
				dsn = strings.TrimSpace(pgDSNFlag)
			}
			if dsn == "" {
				dsn = strings.TrimSpace(os.Getenv(dbruntime.EnvDSN))
			}
			if dsn == "" {
				return fmt.Errorf("db verify: --pg-dsn is required (or set %s)", dbruntime.EnvDSN)
			}

			expected, err := parseExpectCounts(expectCounts)
			if err != nil {
				return err
			}

			probeDSN, err := store.WithApplicationName(dsn, verifierProbeApplicationName)
			if err != nil {
				return fmt.Errorf("db verify: stamp probe application_name: %w", err)
			}
			probeDB, err := sql.Open("pgx", probeDSN)
			if err != nil {
				return fmt.Errorf("db verify: open probe connection: %w", err)
			}
			defer probeDB.Close()
			if err := probeDB.PingContext(cmd.Context()); err != nil {
				return fmt.Errorf("db verify: ping probe connection: %w", err)
			}

			serviceDSN, err := store.WithApplicationName(dsn, verifierServiceApplicationName)
			if err != nil {
				return fmt.Errorf("db verify: stamp service application_name: %w", err)
			}

			opts := pgverify.Options{
				PGDB:                        probeDB,
				OpenServiceStore:            func() (store.Store, error) { return store.OpenPostgresDSN(serviceDSN) },
				SQLitePath:                  sqlitePath,
				SQLiteSettleWindow:          sqliteSettleWindow,
				ExpectedCounts:              expected,
				SampleSessionIDs:            sampleSessionIDs,
				ConcurrencyWriters:          concurrencyWriters,
				ConcurrencyAppendsPerWriter: concurrencyAppends,
				ConcurrencyEventsPerAppend:  concurrencyEvents,
				AssumeExclusiveWindow:       assumeExclusiveWindow,
				KeepTestData:                keepTestData,
			}

			report, err := pgverify.Run(cmd.Context(), opts)
			if err != nil {
				return fmt.Errorf("db verify: %w", err)
			}

			if err := writeReportJSON(cmd, report, outputPath); err != nil {
				return err
			}
			printReportSummary(cmd.OutOrStdout(), report)

			if !report.OK() {
				return errVerifyFailed{code: report.ExitCode()}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&pgDSN, "pg-dsn", "", "Postgres DSN to verify (required; falls back to the root --pg-dsn / KITSOKI_PG_DSN)")
	cmd.Flags().StringVar(&sqlitePath, "sqlite-path", "", "legacy sqlite session-store path to prove inert (default: the resolved local sessions.db path, only if it happens to exist on THIS host)")
	cmd.Flags().DurationVar(&sqliteSettleWindow, "sqlite-settle-window", pgverify.DefaultSQLiteSettleWindow, "how long to wait, bracketing the round trip, before re-statting the sqlite file")
	cmd.Flags().StringArrayVar(&expectCounts, "expect-count", nil, "minimum expected row count for a dbmigrate table label, e.g. --expect-count store.sessions=1545 (repeatable)")
	cmd.Flags().StringArrayVar(&sampleSessionIDs, "sample-session-id", nil, "pin the migrated-data spot-read to this pre-existing session id (repeatable; default: auto-selected)")
	cmd.Flags().IntVar(&concurrencyWriters, "concurrency-writers", pgverify.DefaultConcurrencyWriters, "number of concurrent writer sessions for the stream_pos monotonicity check")
	cmd.Flags().IntVar(&concurrencyAppends, "concurrency-appends", pgverify.DefaultConcurrencyAppendsPerWriter, "appends per writer for the stream_pos monotonicity check")
	cmd.Flags().IntVar(&concurrencyEvents, "concurrency-events", pgverify.DefaultConcurrencyEventsPerAppend, "events per append for the stream_pos monotonicity check")
	cmd.Flags().BoolVar(&assumeExclusiveWindow, "assume-exclusive-window", false, "additionally assert GLOBAL stream_pos contiguity — only safe when no other traffic is hitting this Postgres during the run (e.g. a maintenance window)")
	cmd.Flags().BoolVar(&keepTestData, "keep-test-data", false, "leave every namespaced row this command creates in place for manual inspection, instead of deleting it")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "write the JSON evidence artifact to this path (default: stdout, before the summary)")
	return cmd
}

// errVerifyFailed carries report.ExitCode() through cobra's error path.
// Mirrors interceptExitError (see intercept.go): the JSON artifact and the
// summary table already reported exactly what failed, so main() must NOT
// print an additional "error: ..." line for this — it is a normal "the
// report says no" outcome, not a usage or setup failure.
type errVerifyFailed struct{ code int }

func (e errVerifyFailed) Error() string {
	return fmt.Sprintf("db verify: %d check(s) did not pass — see the report above", e.code)
}

// IsVerifyFailedError reports whether err is a db-verify failure sentinel
// and, if so, returns its process exit code. Used by main() to translate the
// failure without an additional "error: ..." banner (the report already
// said everything there is to say).
func IsVerifyFailedError(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	if e, ok := err.(errVerifyFailed); ok { //nolint:errorlint
		return e.code, true
	}
	return 0, false
}

// parseExpectCounts parses repeated --expect-count label=count flags into a
// map. Rejects (rather than silently ignoring) a malformed entry, a
// negative count, or a duplicate label — all are almost certainly typos an
// operator would want caught immediately, not after the fact.
func parseExpectCounts(raw []string) (map[string]int64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]int64, len(raw))
	for _, kv := range raw {
		idx := strings.Index(kv, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("db verify: --expect-count %q must be label=count", kv)
		}
		label := kv[:idx]
		countStr := kv[idx+1:]
		count, err := strconv.ParseInt(countStr, 10, 64)
		if err != nil || count < 0 {
			return nil, fmt.Errorf("db verify: --expect-count %q: count must be a non-negative integer", kv)
		}
		if _, dup := out[label]; dup {
			return nil, fmt.Errorf("db verify: --expect-count label %q given more than once", label)
		}
		out[label] = count
	}
	return out, nil
}

// writeReportJSON writes report as indented JSON to path, or to out when
// path is empty.
func writeReportJSON(cmd *cobra.Command, report pgverify.Report, path string) error {
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("db verify: marshal report: %w", err)
	}
	b = append(b, '\n')
	if path == "" {
		_, err := cmd.OutOrStdout().Write(b)
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("db verify: write %s: %w", path, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "db verify: evidence artifact written to %s\n", path)
	return nil
}

// printReportSummary renders a human-readable pass/fail table to w.
func printReportSummary(w io.Writer, report pgverify.Report) {
	fmt.Fprintf(w, "\ndb verify: %s\n", report.GeneratedAt.Format(time.RFC3339))
	for _, c := range report.Checks {
		mark := "?"
		switch c.Status {
		case pgverify.StatusPass:
			mark = "PASS"
		case pgverify.StatusFail:
			mark = "FAIL"
		case pgverify.StatusNotEstablished:
			mark = "NOT ESTABLISHED"
		}
		fmt.Fprintf(w, "  [%-15s] %-28s %s\n", mark, c.ID, c.Detail)
	}
	if report.OK() {
		fmt.Fprintln(w, "\ndb verify: ALL CHECKS PASSED")
	} else {
		fmt.Fprintln(w, "\ndb verify: NOT ALL CHECKS PASSED — see above (exit code is non-zero)")
	}
}
