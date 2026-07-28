// db_migrate.go — `kitsoki db migrate`, the SQLite -> Postgres data mover.
//
// There was previously no way to move an existing SQLite deployment's
// durable state into the opt-in Postgres backend (see db_backend.go and
// docs/architecture/storage-backends.md): `--db-backend postgres` starts a
// Postgres store from empty. This command reads a live sessions.db and
// copies it in; the copy logic itself lives in internal/dbmigrate so it is
// testable against internal/dbruntime/pgtest without a CLI in the loop.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/endpoint"
	"kitsoki/internal/dbmigrate"
	"kitsoki/internal/store"
	"kitsoki/internal/turncache"
	"kitsoki/internal/webauth"

	// Registers the "sqlite" database/sql driver; internal/store also does
	// this blank import, but db_migrate.go opens its own source handles
	// independent of any Store, so it names the dependency directly.
	_ "modernc.org/sqlite"
)

func dbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Database backend maintenance",
	}
	cmd.AddCommand(dbMigrateCmd())
	cmd.AddCommand(dbVerifyCmd())
	return cmd
}

func dbMigrateCmd() *cobra.Command {
	var sqlitePath string
	var turncachePath string
	var endpointPath string
	var dryRun bool
	var allowNonEmptyDest bool
	var batchSize int

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Copy a live SQLite deployment's durable state into the configured Postgres backend",
		Long: fmt.Sprintf(`Copies the main session store (sessions, events, snapshots, external_keys,
journal) and the five satellite stores that share its SQLite file —
artifactjob, jobs, chats, study, webauth — from a live sessions.db into the
Postgres backend selected by --db-backend / KITSOKI_DB_BACKEND (postgres or
embedded-postgres; --pg-dsn / KITSOKI_PG_DSN for the postgres backend). Two
more satellites, turncache and the capsule/endpoint broker, keep their state
in their own SQLite file and are only migrated if --turncache-sqlite /
--endpoint-sqlite is passed.

Not migrated, on purpose:
  - graph catalogs (internal/graph/pgcatalog): no SQLite backend exists;
    use "kitsoki graph import" from the YAML catalog instead.
  - mining watermarks and studio objectives: SQLite/local keeps these in
    memory or in a file-mtime ledger, never a durable SQLite table.
  - session_locks, chat_locks, chat_pty_sessions: process-liveness/lock rows
    that describe a process on THIS host; copying them to a new deployment
    would be misleading, not useful.

Idempotent and resumable: every insert targets the destination's real
primary key with ON CONFLICT ... DO NOTHING, so an interrupted run can
simply be re-run — already-copied rows are never duplicated. Streams one
table at a time via a single source cursor batched into
--batch-size-row destination transactions (default %d); never loads a whole
table into memory.

Refuses a non-empty destination unless --allow-nonempty-dest is passed, and
after copying, compares source and destination row counts per table,
failing loudly on any mismatch.

--dry-run reports planned per-table row counts and writes no data (the
destination's own idempotent CREATE SCHEMA/TABLE DDL may still run, since
that is how planned counts are read back).`, dbmigrate.DefaultBatchSize),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sqlitePath == "" {
				sqlitePath = defaultDBPath()
			}
			opts := dbmigrate.Options{DryRun: dryRun, AllowNonEmptyDest: allowNonEmptyDest, BatchSize: batchSize}
			return runDBMigrate(cmd, sqlitePath, turncachePath, endpointPath, opts)
		},
	}
	cmd.Flags().StringVar(&sqlitePath, "sqlite-path", "", "path to the source sessions.db (default: the resolved local sessions.db path)")
	cmd.Flags().StringVar(&turncachePath, "turncache-sqlite", "", "optional path to a separate turncache SQLite file to migrate too")
	cmd.Flags().StringVar(&endpointPath, "endpoint-sqlite", "", "optional path to a separate capsule/endpoint broker SQLite file to migrate too")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report planned row counts; write no data")
	cmd.Flags().BoolVar(&allowNonEmptyDest, "allow-nonempty-dest", false, "permit migrating into a destination that already has rows in a targeted table")
	cmd.Flags().IntVar(&batchSize, "batch-size", 0, fmt.Sprintf("rows per destination transaction (default %d)", dbmigrate.DefaultBatchSize))
	return cmd
}

// runDBMigrate resolves the configured backend, opens the source SQLite
// file(s) and the destination Postgres handle, runs the preflight
// non-empty-destination check, performs the copy (or, under DryRun, only
// reports planned counts), and prints a per-table report. It is a thin
// wrapper: all of the actual copy logic is in internal/dbmigrate so it can
// be exercised in tests without a *cobra.Command in the loop.
func runDBMigrate(cmd *cobra.Command, sqlitePath, turncachePath, endpointPath string, opts dbmigrate.Options) error {
	backend, err := resolveDBBackend()
	if err != nil {
		return err
	}
	if backend == dbBackendSQLite {
		return fmt.Errorf("db migrate: destination backend is sqlite; pass --db-backend postgres|embedded-postgres (SQLite is only ever the migration source)")
	}
	if _, err := os.Stat(sqlitePath); err != nil {
		return fmt.Errorf("db migrate: source sqlite file %q: %w", sqlitePath, err)
	}

	srcDB, err := openReadOnlySQLite(sqlitePath)
	if err != nil {
		return fmt.Errorf("db migrate: open source %q: %w", sqlitePath, err)
	}
	defer srcDB.Close()

	destStore, err := openSessionStoreBackend(defaultDBPath())
	if err != nil {
		return fmt.Errorf("db migrate: open destination: %w", err)
	}
	defer func() { _ = destStore.Close() }()
	if !store.IsPostgres(destStore) {
		return fmt.Errorf("db migrate: resolved backend %q did not open a Postgres store", backend)
	}
	destDB := destStore.DB()

	// Create every satellite's Postgres schema up front — mirrors how a live
	// server constructs each satellite off the shared handle (db_backend.go),
	// and gives Preflight real (if empty) tables to count.
	if _, err := newArtifactJobStore(destStore); err != nil {
		return fmt.Errorf("db migrate: prepare artifactjob schema: %w", err)
	}
	if _, err := newJobStore(destStore); err != nil {
		return fmt.Errorf("db migrate: prepare jobs schema: %w", err)
	}
	if _, err := newChatStore(destStore); err != nil {
		return fmt.Errorf("db migrate: prepare chats schema: %w", err)
	}
	if _, err := newStudyStore(destStore); err != nil {
		return fmt.Errorf("db migrate: prepare study schema: %w", err)
	}
	if _, err := webauth.NewPostgresStore(destDB); err != nil {
		return fmt.Errorf("db migrate: prepare webauth schema: %w", err)
	}

	out := cmd.OutOrStdout()
	ctx := cmd.Context()

	if !opts.DryRun {
		if err := preflightRefuse(ctx, out, "main store + shared-file satellites", dbmigrate.PreflightShared, destDB, opts.AllowNonEmptyDest); err != nil {
			return err
		}
	}
	report := dbmigrate.Migrate(ctx, srcDB, destDB, opts)
	printMigrateReport(out, "shared sessions.db", report)

	if turncachePath != "" {
		tcSrc, err := openReadOnlySQLite(turncachePath)
		if err != nil {
			return fmt.Errorf("db migrate: open turncache source %q: %w", turncachePath, err)
		}
		defer tcSrc.Close()
		if _, err := turncache.NewPostgres(destDB, turncache.DefaultConfig()); err != nil {
			return fmt.Errorf("db migrate: prepare turncache schema: %w", err)
		}
		if !opts.DryRun {
			if err := preflightRefuse(ctx, out, "turncache", dbmigrate.PreflightTurnCache, destDB, opts.AllowNonEmptyDest); err != nil {
				return err
			}
		}
		tcReport := dbmigrate.MigrateTurnCache(ctx, tcSrc, destDB, opts)
		printMigrateReport(out, "turncache", tcReport)
		report.Tables = append(report.Tables, tcReport.Tables...)
	}

	if endpointPath != "" {
		epSrc, err := openReadOnlySQLite(endpointPath)
		if err != nil {
			return fmt.Errorf("db migrate: open endpoint source %q: %w", endpointPath, err)
		}
		defer epSrc.Close()
		if _, err := endpoint.NewPostgresStore(destDB); err != nil {
			return fmt.Errorf("db migrate: prepare endpoint schema: %w", err)
		}
		if !opts.DryRun {
			if err := preflightRefuse(ctx, out, "capsule/endpoint", dbmigrate.PreflightEndpoint, destDB, opts.AllowNonEmptyDest); err != nil {
				return err
			}
		}
		epReport := dbmigrate.MigrateEndpoint(ctx, epSrc, destDB, opts)
		printMigrateReport(out, "capsule/endpoint", epReport)
		report.Tables = append(report.Tables, epReport.Tables...)
	}

	if opts.DryRun {
		fmt.Fprintln(out, "db migrate: --dry-run — no data was written")
		return nil
	}
	if !report.OK() {
		return fmt.Errorf("db migrate: one or more tables failed to migrate cleanly: %w", report.Err())
	}
	fmt.Fprintln(out, "db migrate: all tables copied and verified")
	return nil
}

// openReadOnlySQLite opens path and sets PRAGMA query_only so a bug in this
// command can never accidentally write into the deployment being migrated
// out of.
func openReadOnlySQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA query_only = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set query_only: %w", err)
	}
	return db, nil
}

// preflightRefuse runs preflight over destDB, printing what it found, and
// returns an error naming every non-empty table when the destination is not
// empty and the operator has not passed --allow-nonempty-dest.
func preflightRefuse(ctx context.Context, out io.Writer, label string, preflight func(context.Context, *sql.DB) ([]dbmigrate.DestCount, error), destDB *sql.DB, allow bool) error {
	counts, err := preflight(ctx, destDB)
	if err != nil {
		return fmt.Errorf("db migrate: preflight %s: %w", label, err)
	}
	var nonEmpty []dbmigrate.DestCount
	for _, c := range counts {
		if c.Rows > 0 {
			nonEmpty = append(nonEmpty, c)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}
	if allow {
		fmt.Fprintf(out, "db migrate: %s destination is non-empty (--allow-nonempty-dest set):\n", label)
		for _, c := range nonEmpty {
			fmt.Fprintf(out, "  %-40s %d row(s)\n", c.DestTable, c.Rows)
		}
		return nil
	}
	msg := fmt.Sprintf("db migrate: refusing to run — %s destination already has data:\n", label)
	for _, c := range nonEmpty {
		msg += fmt.Sprintf("  %-40s %d row(s)\n", c.DestTable, c.Rows)
	}
	msg += "pass --allow-nonempty-dest to migrate anyway (idempotent re-runs of a prior migrate are always safe without it, since already-empty tables stay empty in this check)"
	return fmt.Errorf("%s", msg)
}

func printMigrateReport(out io.Writer, label string, report dbmigrate.Report) {
	fmt.Fprintf(out, "db migrate: %s\n", label)
	for _, t := range report.Tables {
		switch {
		case !t.SourcePresent:
			fmt.Fprintf(out, "  %-32s source table not present (feature never used on this deployment) — skipped\n", t.Label)
		case t.Planned:
			fmt.Fprintf(out, "  %-32s source=%d dest(before)=%d — planned, no write (dry-run)\n", t.Label, t.SourceRows, t.DestRowsBefore)
		case t.Err != nil:
			fmt.Fprintf(out, "  %-32s FAILED: %v\n", t.Label, t.Err)
		default:
			fmt.Fprintf(out, "  %-32s source=%d inserted=%d already-present=%d dest(after)=%d OK\n",
				t.Label, t.SourceRows, t.Inserted, t.Skipped, t.DestRowsAfter)
		}
	}
}
