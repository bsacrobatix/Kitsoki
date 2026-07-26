// host.graph.* Postgres catalog routing: a catalog_path of the explicit form
// "pg:<catalog-id>" (internal/graph/pgcatalog.RefPrefix) resolves to the
// Postgres-backed CatalogStore instead of the file store. The database handle
// comes from the session's configured backend when the embedding process
// injects one (SetGraphPGDB — cmd/kitsoki's --db-backend wiring, tests), else
// from a process-cached connection to KITSOKI_PG_DSN. File refs are entirely
// untouched: they never consult this file, and the YAML path stays the
// default.
package host

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"

	"kitsoki/internal/dbruntime"
	objectgraph "kitsoki/internal/graph"
	"kitsoki/internal/graph/pgcatalog"
)

// SetGraphPGDB injects the *sql.DB every "pg:<catalog-id>" graph catalog ref
// resolves against — the session's configured backend handle. nil resets to
// the default KITSOKI_PG_DSN resolution. Cached per-catalog stores are
// invalidated either way.
func SetGraphPGDB(db *sql.DB) {
	graphPG.mu.Lock()
	defer graphPG.mu.Unlock()
	graphPG.injected = db
	graphPG.stores = nil
}

// graphPG owns the pg catalog routing state: the injected handle (nil when
// unset), the lazily opened env-DSN handle, and one migrated *pgcatalog.Store
// per catalog id on the current handle.
var graphPG struct {
	mu       sync.Mutex
	injected *sql.DB
	envDSN   string // DSN the cached envDB was opened against
	envDB    *sql.DB
	stores   map[string]objectgraph.CatalogStore // catalog id -> store on the current handle
}

// graphIsPGRef reports whether a catalog_path arg names a Postgres-backed
// catalog.
func graphIsPGRef(ref string) bool { return pgcatalog.IsRef(ref) }

// graphPGCatalogStore resolves a "pg:<catalog-id>" ref to its store,
// running pgcatalog's idempotent schema migration once per catalog id per
// handle. Errors (no DSN, unreachable server, bad ref) come back as an error
// — the resolver seam wraps them in an errorCatalogStore so the CatalogStore
// signature stays error-free and the failure surfaces on first use.
func graphPGCatalogStore(ref string) (objectgraph.CatalogStore, error) {
	id, ok := pgcatalog.CatalogID(ref)
	if !ok {
		return nil, fmt.Errorf("host.graph: %q is not a valid pg catalog ref (want %s<catalog-id>)", ref, pgcatalog.RefPrefix)
	}

	graphPG.mu.Lock()
	defer graphPG.mu.Unlock()
	if store, ok := graphPG.stores[id]; ok {
		return store, nil
	}

	db := graphPG.injected
	if db == nil {
		dsn := os.Getenv(dbruntime.EnvDSN)
		if dsn == "" {
			return nil, fmt.Errorf("host.graph: catalog ref %q needs a Postgres connection: set %s or configure a pg db backend", ref, dbruntime.EnvDSN)
		}
		if graphPG.envDB == nil || graphPG.envDSN != dsn {
			opened, err := sql.Open("pgx", dsn)
			if err != nil {
				return nil, fmt.Errorf("host.graph: open %s for %q: %w", dbruntime.EnvDSN, ref, err)
			}
			if graphPG.envDB != nil {
				_ = graphPG.envDB.Close()
			}
			graphPG.envDB, graphPG.envDSN = opened, dsn
			graphPG.stores = nil
		}
		db = graphPG.envDB
	}

	store, err := pgcatalog.Open(db, id)
	if err != nil {
		return nil, err
	}
	if graphPG.stores == nil {
		graphPG.stores = map[string]objectgraph.CatalogStore{}
	}
	graphPG.stores[id] = store
	return store, nil
}

// errorCatalogStore defers a store-resolution error to first use, so the
// error-free graphCatalogStoreResolver seam keeps its signature while a bad
// pg configuration still surfaces as a clear per-call error.
type errorCatalogStore struct {
	ref string
	err error
}

var _ objectgraph.CatalogStore = errorCatalogStore{}

func (s errorCatalogStore) Ref() string { return s.ref }

func (s errorCatalogStore) Load(context.Context) (*objectgraph.Catalog, objectgraph.CatalogRev, error) {
	return nil, "", s.err
}

func (s errorCatalogStore) Commit(context.Context, objectgraph.CatalogRev, []objectgraph.Operation, objectgraph.CommitOptions) (objectgraph.CommitResult, error) {
	return objectgraph.CommitResult{}, s.err
}
