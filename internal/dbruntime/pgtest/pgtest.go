// Package pgtest gives tests real Postgres databases with zero external
// setup. One embedded server (internal/dbruntime) is started lazily per test
// process and shared; every Open call gets its own freshly created database,
// so parallel tests are fully isolated. Set KITSOKI_PG_DSN to point the
// helper at an external server instead (the embedded server is then never
// started); the per-test create/drop still applies, so the account in that
// DSN needs CREATEDB.
package pgtest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"kitsoki/internal/dbruntime"
)

// shared is the process-wide embedded (or external) server, reference-counted
// so the last test to finish stops the server instead of orphaning it.
var (
	mu        sync.Mutex
	sharedRT  dbruntime.Runtime
	sharedErr error
	admin     *sql.DB
	refs      int
	dataDir   string
	nextID    int
)

// Open returns a *sql.DB (pgx stdlib driver) connected to a fresh database
// that only this test sees. The database is dropped and the handle closed in
// t.Cleanup. Safe for t.Parallel. Skips the test when no Postgres server can
// be started in this environment (e.g. the one-time binary download is
// blocked) and KITSOKI_PG_DSN is unset.
func Open(t *testing.T) *sql.DB {
	t.Helper()

	mu.Lock()
	if sharedRT == nil && sharedErr == nil {
		acquireLocked()
	}
	if sharedErr != nil {
		err := sharedErr
		mu.Unlock()
		t.Skipf("pgtest: postgres unavailable: %v (set %s to use an external server)", err, dbruntime.EnvDSN)
	}
	refs++
	nextID++
	name := fmt.Sprintf("pgtest_%d_%d_%s", os.Getpid(), nextID, randHex(4))
	// Serialized under mu: concurrent CREATE DATABASE from one template can
	// fail on some server versions, and it is cheap relative to the tests.
	_, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %s TEMPLATE template0`, quoteIdent(name)))
	adminDSN := sharedRT.DSN()
	if err != nil {
		releaseLocked()
		mu.Unlock()
		t.Fatalf("pgtest: create database %s: %v", name, err)
	}
	mu.Unlock()

	t.Cleanup(func() { dropDatabase(t, name) })

	dsn, err := dsnWithDatabase(adminDSN, name)
	if err != nil {
		t.Fatalf("pgtest: rewrite dsn: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("pgtest: open %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("pgtest: ping %s: %v", name, err)
	}
	return db
}

// acquireLocked starts the shared runtime; mu must be held. The embedded data
// directory is a throwaway temp dir — persistence is a production concern,
// tests only need speed and isolation.
func acquireLocked() {
	dir, err := os.MkdirTemp("", "kitsoki-pgtest-")
	if err != nil {
		sharedErr = err
		return
	}
	rt, err := dbruntime.Start(dbruntime.Config{DataDir: dir})
	if err != nil {
		os.RemoveAll(dir)
		sharedErr = err
		return
	}
	db, err := rt.DB()
	if err != nil {
		rt.Close()
		os.RemoveAll(dir)
		sharedErr = err
		return
	}
	sharedRT, admin, dataDir = rt, db, dir
}

// releaseLocked drops one reference; the last one stops the server. mu must
// be held. A later Open restarts from scratch, which only costs time when
// tests do not overlap at all.
func releaseLocked() {
	refs--
	if refs > 0 {
		return
	}
	sharedRT.Close()
	os.RemoveAll(dataDir)
	os.RemoveAll(dataDir + ".runtime")
	sharedRT, admin, dataDir, sharedErr = nil, nil, "", nil
}

func dropDatabase(t *testing.T, name string) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	defer releaseLocked()
	// FORCE (PG13+) kicks lingering connections; fall back for older external
	// servers where the test's own connections are already closed anyway.
	_, err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(name)))
	if err != nil {
		_, err = admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quoteIdent(name)))
	}
	if err != nil {
		t.Errorf("pgtest: drop database %s: %v", name, err)
	}
}

// dsnWithDatabase returns dsn pointed at database name, accepting both URL
// (postgres://...) and keyword=value connection strings.
func dsnWithDatabase(dsn, name string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		u.Path = "/" + name
		return u.String(), nil
	}
	// Keyword form: drop any existing dbname and append ours.
	fields := strings.Fields(dsn)
	kept := fields[:0]
	for _, f := range fields {
		if !strings.HasPrefix(f, "dbname=") {
			kept = append(kept, f)
		}
	}
	return strings.Join(append(kept, "dbname="+name), " "), nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}
