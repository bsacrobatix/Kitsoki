package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/applicationjob"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/chats"
	"kitsoki/internal/clock"
	"kitsoki/internal/dbruntime"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
	"kitsoki/internal/journal"
	"kitsoki/internal/materializationstatus"
	"kitsoki/internal/mining"
	"kitsoki/internal/reviewedfeedback"
	"kitsoki/internal/store"
	"kitsoki/internal/study"
)

// Session-store backend selection. SQLite is the default and keeps today's
// behavior byte-for-byte; the Postgres backends are opt-in via the persistent
// --db-backend flag or KITSOKI_DB_BACKEND (flag wins, matching the
// --kitsoki-repo precedence). Every open site that used to call
// store.Open(dbPath) for the shared sessions.db goes through
// openSessionStoreBackend instead. Satellite stores (jobs, chats, artifact
// jobs, studies) still hang off Store.DB(), but each must speak the shared
// handle's dialect, so construction goes through the newChatStore /
// newJobStore / newArtifactJobStore / newStudyStore helpers below, which
// dispatch on store.IsPostgres.
const (
	envDBBackend = "KITSOKI_DB_BACKEND"

	dbBackendSQLite     = "sqlite"
	dbBackendPostgres   = "postgres"
	dbBackendEmbeddedPG = "embedded-postgres"
)

// dbBackendFlag / pgDSNFlag back the persistent --db-backend / --pg-dsn
// overrides registered in newRootCmd.
var (
	dbBackendFlag string
	pgDSNFlag     string
)

// embeddedPG owns the single process-shared embedded Postgres runtime for the
// embedded-postgres backend. buildSessionRuntime opens one store per session,
// so the server is started lazily on first open and lives for the process
// lifetime; closeEmbeddedPG stops it after the root command returns.
var embeddedPG struct {
	mu sync.Mutex
	rt dbruntime.Runtime
}

// resolveDBBackend normalizes the backend selection from --db-backend /
// KITSOKI_DB_BACKEND. Empty means sqlite (the historical default).
func resolveDBBackend() (string, error) {
	backend := strings.TrimSpace(dbBackendFlag)
	if backend == "" {
		backend = strings.TrimSpace(os.Getenv(envDBBackend))
	}
	switch backend {
	case "", dbBackendSQLite:
		return dbBackendSQLite, nil
	case dbBackendPostgres, dbBackendEmbeddedPG:
		return backend, nil
	default:
		return "", fmt.Errorf("unknown db backend %q (use sqlite|postgres|embedded-postgres)", backend)
	}
}

// openSessionStoreBackend opens the shared session store for the resolved backend.
//
//   - sqlite: exactly store.Open(dbPath), today's default path.
//   - postgres: DSN from --pg-dsn / KITSOKI_PG_DSN (required).
//   - embedded-postgres: lazily starts one process-shared embedded server with
//     its data dir next to dbPath (e.g. ~/.local/share/kitsoki/pg) and opens a
//     store against it. A set KITSOKI_PG_DSN still wins inside dbruntime.Start
//     (documented passthrough: it never starts an embedded server then).
func openSessionStoreBackend(dbPath string) (store.Store, error) {
	backend, err := resolveDBBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case dbBackendSQLite:
		return store.Open(dbPath)
	case dbBackendPostgres:
		dsn := strings.TrimSpace(pgDSNFlag)
		if dsn == "" {
			dsn = strings.TrimSpace(os.Getenv(dbruntime.EnvDSN))
		}
		if dsn == "" {
			return nil, fmt.Errorf("db backend %q requires a DSN: pass --pg-dsn or set %s", dbBackendPostgres, dbruntime.EnvDSN)
		}
		return openPGSessionStore(dsn)
	case dbBackendEmbeddedPG:
		rt, err := startEmbeddedPG(dbPath)
		if err != nil {
			return nil, fmt.Errorf("start embedded postgres: %w", err)
		}
		return openPGSessionStore(rt.DSN())
	default:
		// Unreachable: resolveDBBackend already rejected unknown values.
		return nil, fmt.Errorf("unknown db backend %q", backend)
	}
}

// openPGSessionStore opens the Postgres session store and injects its shared
// handle into host's "pg:<catalog-id>" graph catalog routing, so graph refs
// resolve against the configured backend without requiring KITSOKI_PG_DSN to
// be set separately (it stays the fallback when no store has been opened).
func openPGSessionStore(dsn string) (store.Store, error) {
	s, err := store.OpenPostgresDSN(dsn)
	if err != nil {
		return nil, err
	}
	host.SetGraphPGDB(s.DB())
	return s, nil
}

// newChatStore constructs the chats satellite store on s's shared handle in
// the dialect matching s's backend. Options apply to both dialects.
func newChatStore(s store.Store, opts ...chats.Option) (*chats.Store, error) {
	if store.IsPostgres(s) {
		return chats.NewPostgresStore(s.DB(), opts...)
	}
	return chats.NewStore(s.DB(), opts...)
}

// newJobStore constructs the jobs satellite store on s's shared handle,
// appending the Postgres dialect option when s's backend requires it.
func newJobStore(s store.Store, opts ...jobs.JobStoreOption) (*jobs.JobStore, error) {
	if store.IsPostgres(s) {
		opts = append(opts, jobs.WithDialect(jobs.DialectPostgres))
	}
	return jobs.NewJobStore(s.DB(), opts...)
}

// newJournalWriter constructs the journal writer on s's shared handle in the
// dialect matching s's backend.
func newJournalWriter(s store.Store) (journal.Writer, error) {
	if store.IsPostgres(s) {
		return journal.NewPostgresWriter(s.DB())
	}
	return journal.NewSQLiteWriter(s.DB())
}

// newJournalReader constructs the journal reader on s's shared handle in the
// dialect matching s's backend.
func newJournalReader(s store.Store) (journal.Reader, error) {
	if store.IsPostgres(s) {
		return journal.NewPostgresReader(s.DB())
	}
	return journal.NewSQLiteReader(s.DB())
}

// newArtifactJobStore constructs the artifact-job satellite store on s's
// shared handle in the dialect matching s's backend.
func newArtifactJobStore(s store.Store) (artifactjob.Store, error) {
	if store.IsPostgres(s) {
		return artifactjob.NewPostgresStore(s.DB())
	}
	return artifactjob.NewSQLiteStore(s.DB())
}

func newReviewedFeedbackDispatchStore(
	s store.Store,
	clk clock.Clock,
) (reviewedfeedback.DispatchStore, error) {
	if store.IsPostgres(s) {
		return reviewedfeedback.NewPostgresDispatchStore(s.DB(), clk)
	}
	return reviewedfeedback.NewSQLiteDispatchStore(s.DB(), clk)
}

func newReviewedFeedbackReconcileStore(
	s store.Store,
	clk clock.Clock,
) (reviewedfeedback.ReconcileStore, error) {
	if store.IsPostgres(s) {
		return reviewedfeedback.NewPostgresReconcileStore(s.DB(), clk)
	}
	return reviewedfeedback.NewSQLiteReconcileStore(s.DB(), clk)
}

func newApplicationJobStore(s store.Store) (applicationjob.Store, error) {
	if store.IsPostgres(s) {
		return applicationjob.NewPostgresStore(s.DB())
	}
	return applicationjob.NewSQLiteStore(s.DB())
}

// newMiningWatermarkStore constructs the miner's per-slug watermark ledger.
// On a Postgres backend it is the durable mining.consumer_offsets table on s's
// shared handle (seeded from the config ledger only for slugs the table has
// never seen — durable rows win); otherwise the in-memory map store, keeping
// today's file/mtime behavior byte-for-byte.
func newMiningWatermarkStore(s store.Store, seed map[string]int64) (mining.WatermarkStore, error) {
	if store.IsPostgres(s) {
		return mining.NewPGWatermarkStore(s.DB(), mining.SessionMinerConsumer, seed)
	}
	return mining.NewMapWatermarkStore(seed), nil
}

// newStudyStore constructs the study satellite store on s's shared handle in
// the dialect matching s's backend.
func newStudyStore(s store.Store) (study.Store, error) {
	if store.IsPostgres(s) {
		return study.NewPostgresStore(s.DB())
	}
	return study.NewSQLiteStore(s.DB())
}

// newMaterializationStatusStore constructs the application lifecycle
// projection on the same backend and shared handle as the daemon session
// store.
func newMaterializationStatusStore(s store.Store, now func() time.Time) (materializationstatus.Store, error) {
	if store.IsPostgres(s) {
		return materializationstatus.NewPostgresStore(s.DB(), now)
	}
	return materializationstatus.NewSQLiteStore(s.DB(), now)
}

// openGraphCatalogDB opens the shared database handle `kitsoki graph
// import/export` persist graph catalogs through (internal/graph/pgcatalog's
// schema-per-subsystem rows hang off the same handle as every satellite
// store). Graph catalogs have no SQLite backend — the YAML file catalog stays
// the default surface — so the sqlite backend (the default) is a clear error
// here, never a silent fallback.
func openGraphCatalogDB() (store.Store, error) {
	backend, err := resolveDBBackend()
	if err != nil {
		return nil, err
	}
	if backend == dbBackendSQLite {
		return nil, fmt.Errorf("graph catalogs are stored in Postgres only, but the db backend is %q: pass --db-backend postgres|embedded-postgres (or set %s), with a DSN via --pg-dsn / %s for the postgres backend",
			backend, envDBBackend, dbruntime.EnvDSN)
	}
	return openSessionStoreBackend(defaultDBPath())
}

// startEmbeddedPG returns the process-shared embedded runtime, starting it on
// first use. The data dir defaults next to the session db path so embedded
// Postgres state lives under the same kitsoki data dir as sessions.db.
func startEmbeddedPG(dbPath string) (dbruntime.Runtime, error) {
	embeddedPG.mu.Lock()
	defer embeddedPG.mu.Unlock()
	if embeddedPG.rt != nil {
		return embeddedPG.rt, nil
	}
	var dataDir string
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		dataDir = filepath.Join(dir, "pg")
	}
	rt, err := dbruntime.Start(dbruntime.Config{DataDir: dataDir})
	if err != nil {
		return nil, err
	}
	embeddedPG.rt = rt
	return rt, nil
}

// closeEmbeddedPG stops the process-shared embedded server if one was started.
// Called from main after the root command returns (os.Exit skips defers, so it
// runs before the exit-code translation).
func closeEmbeddedPG() {
	embeddedPG.mu.Lock()
	defer embeddedPG.mu.Unlock()
	if embeddedPG.rt != nil {
		_ = embeddedPG.rt.Close()
		embeddedPG.rt = nil
	}
}
