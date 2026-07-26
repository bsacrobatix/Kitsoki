package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/dbruntime"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/materializationstatus"
	"kitsoki/internal/store"
)

// resetDBBackend saves and restores the package-level backend selection state
// (flag vars + the process-shared embedded runtime) around a test. Tests using
// it must not run in parallel (they mutate package state and env).
func resetDBBackend(t *testing.T) {
	t.Helper()
	prevBackend, prevDSN := dbBackendFlag, pgDSNFlag
	t.Cleanup(func() {
		dbBackendFlag, pgDSNFlag = prevBackend, prevDSN
		closeEmbeddedPG()
	})
}

func TestResolveDBBackend(t *testing.T) {
	resetDBBackend(t)
	t.Setenv(envDBBackend, "")

	// Default: sqlite.
	dbBackendFlag = ""
	got, err := resolveDBBackend()
	if err != nil || got != dbBackendSQLite {
		t.Fatalf("default backend = %q, %v; want %q, nil", got, err, dbBackendSQLite)
	}

	// Env selects.
	t.Setenv(envDBBackend, dbBackendPostgres)
	got, err = resolveDBBackend()
	if err != nil || got != dbBackendPostgres {
		t.Fatalf("env backend = %q, %v; want %q, nil", got, err, dbBackendPostgres)
	}

	// Flag wins over env.
	dbBackendFlag = dbBackendEmbeddedPG
	got, err = resolveDBBackend()
	if err != nil || got != dbBackendEmbeddedPG {
		t.Fatalf("flag backend = %q, %v; want %q, nil", got, err, dbBackendEmbeddedPG)
	}

	// Unknown values are rejected.
	dbBackendFlag = "mysql"
	if _, err := resolveDBBackend(); err == nil {
		t.Fatal("unknown backend accepted; want error")
	}
}

func TestOpenSessionStoreSQLiteDefault(t *testing.T) {
	resetDBBackend(t)
	t.Setenv(envDBBackend, "")
	dbBackendFlag = ""

	s, err := openSessionStoreBackend(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("openSessionStore (sqlite default): %v", err)
	}
	defer func() { _ = s.Close() }()
	if s.DB() == nil {
		t.Fatal("sqlite store returned nil *sql.DB")
	}
	def := &app.AppDef{}
	def.App.ID = "test-app"
	if _, err := s.CreateSession(context.Background(), def); err != nil {
		t.Fatalf("CreateSession on sqlite store: %v", err)
	}
}

func TestOpenSessionStorePostgresRequiresDSN(t *testing.T) {
	resetDBBackend(t)
	t.Setenv(dbruntime.EnvDSN, "")
	dbBackendFlag = dbBackendPostgres
	pgDSNFlag = ""

	if _, err := openSessionStoreBackend(filepath.Join(t.TempDir(), "sessions.db")); err == nil {
		t.Fatal("postgres backend without DSN accepted; want error")
	} else if !strings.Contains(err.Error(), dbruntime.EnvDSN) {
		t.Fatalf("error %q does not mention %s", err, dbruntime.EnvDSN)
	}
}

func TestOpenSessionStoreEmbeddedPG(t *testing.T) {
	resetDBBackend(t)
	// Force a real embedded server: an inherited KITSOKI_PG_DSN would make
	// dbruntime.Start pass through to an external database.
	t.Setenv(dbruntime.EnvDSN, "")
	dbBackendFlag = dbBackendEmbeddedPG

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	s, err := openSessionStoreBackend(dbPath)
	if err != nil {
		t.Skipf("embedded postgres unavailable in this environment: %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.DB() == nil {
		t.Fatal("embedded-postgres store returned nil *sql.DB")
	}
	def := &app.AppDef{}
	def.App.ID = "test-app"
	if _, err := s.CreateSession(context.Background(), def); err != nil {
		t.Fatalf("CreateSession on embedded-postgres store: %v", err)
	}

	// A second open reuses the process-shared server (no second Start).
	s2, err := openSessionStoreBackend(dbPath)
	if err != nil {
		t.Fatalf("second openSessionStore (shared embedded runtime): %v", err)
	}
	_ = s2.Close()
}

// TestSatelliteStoresFollowBackendDialect proves every satellite store that
// hangs off the shared session-store handle constructs successfully in the
// dialect of the selected backend. The Postgres half is exactly the failure
// mode this guards against: the SQLite constructors run SQLite-only DDL
// (STRICT tables, PRAGMA user_version) on the pg handle and fail hard.
func TestSatelliteStoresFollowBackendDialect(t *testing.T) {
	buildAll := func(t *testing.T, s store.Store) {
		t.Helper()
		if cs, err := newChatStore(s); err != nil {
			t.Errorf("newChatStore: %v", err)
		} else if cs == nil {
			t.Error("newChatStore: nil store")
		}
		if js, err := newJobStore(s); err != nil {
			t.Errorf("newJobStore: %v", err)
		} else if js == nil {
			t.Error("newJobStore: nil store")
		}
		if as, err := newArtifactJobStore(s); err != nil {
			t.Errorf("newArtifactJobStore: %v", err)
		} else if as == nil {
			t.Error("newArtifactJobStore: nil store")
		}
		if ss, err := newStudyStore(s); err != nil {
			t.Errorf("newStudyStore: %v", err)
		} else if ss == nil {
			t.Error("newStudyStore: nil store")
		}
		if ms, err := newMaterializationStatusStore(s, nil); err != nil {
			t.Errorf("newMaterializationStatusStore: %v", err)
		} else if ms == nil {
			t.Error("newMaterializationStatusStore: nil store")
		} else {
			_, err := ms.Save(context.Background(), materializationstatus.Record{
				ApplicationID: "factory-app", JobID: "factory-job",
				Status: "done", UpdatedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Errorf("newMaterializationStatusStore.Save: %v", err)
			}
		}
		if ds, err := newReviewedFeedbackDispatchStore(s, nil); err != nil {
			t.Errorf("newReviewedFeedbackDispatchStore: %v", err)
		} else if ds == nil {
			t.Error("newReviewedFeedbackDispatchStore: nil store")
		}
		if rs, err := newReviewedFeedbackReconcileStore(s, nil); err != nil {
			t.Errorf("newReviewedFeedbackReconcileStore: %v", err)
		} else if rs == nil {
			t.Error("newReviewedFeedbackReconcileStore: nil store")
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		s, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		defer func() { _ = s.Close() }()
		buildAll(t, s)
	})

	t.Run("postgres", func(t *testing.T) {
		db := pgtest.Open(t) // skips when no server can start here
		s, err := store.OpenPostgres(db)
		if err != nil {
			t.Fatalf("store.OpenPostgres: %v", err)
		}
		// pgtest owns the handle's cleanup; Store.Close is idempotent with it.
		buildAll(t, s)
	})
}
