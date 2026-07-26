package dbruntime

import (
	"os"
	"testing"
)

// TestExternalPassthrough proves a configured DSN short-circuits embedded
// startup: Start must return instantly with the DSN echoed back.
func TestExternalPassthrough(t *testing.T) {
	const dsn = "postgres://user:pw@db.example.invalid:5432/kitsoki"
	rt, err := Start(Config{DSN: dsn})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Close()
	if got := rt.DSN(); got != dsn {
		t.Fatalf("DSN() = %q, want %q", got, dsn)
	}
}

// TestEnvPassthrough proves KITSOKI_PG_DSN selects passthrough mode too.
func TestEnvPassthrough(t *testing.T) {
	const dsn = "postgres://user:pw@db.example.invalid:5432/kitsoki"
	t.Setenv(EnvDSN, dsn)
	rt, err := Start(Config{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Close()
	if got := rt.DSN(); got != dsn {
		t.Fatalf("DSN() = %q, want %q", got, dsn)
	}
}

// TestEmbeddedLifecycle starts a real embedded server, pings it, and runs DDL
// plus DML through the pgx stdlib handle.
func TestEmbeddedLifecycle(t *testing.T) {
	if os.Getenv(EnvDSN) != "" {
		t.Skipf("%s is set; embedded lifecycle not exercised", EnvDSN)
	}
	rt, err := Start(Config{DataDir: t.TempDir() + "/data"})
	if err != nil {
		t.Skipf("embedded postgres unavailable in this environment: %v", err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	db, err := rt.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE dbruntime_smoke (id INT PRIMARY KEY, note TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO dbruntime_smoke (id, note) VALUES (1, 'hello')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var note string
	if err := db.QueryRow(`SELECT note FROM dbruntime_smoke WHERE id = 1`).Scan(&note); err != nil {
		t.Fatalf("select: %v", err)
	}
	if note != "hello" {
		t.Fatalf("note = %q, want %q", note, "hello")
	}
	if _, err := db.Exec(`DROP TABLE dbruntime_smoke`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	// DB is cached: a second call must return the same handle.
	again, err := rt.DB()
	if err != nil {
		t.Fatalf("DB (second call): %v", err)
	}
	if again != db {
		t.Fatal("DB() returned a different handle on second call")
	}
}
