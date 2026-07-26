package pgtest

import (
	"database/sql"
	"testing"
)

// TestOpenIsolation proves two parallel tests share one server but get fully
// isolated databases: identical table names, disjoint contents.
func TestOpenIsolation(t *testing.T) {
	run := func(marker string) func(*testing.T) {
		return func(t *testing.T) {
			t.Parallel()
			db := Open(t)
			seed(t, db, marker)
			var got string
			if err := db.QueryRow(`SELECT marker FROM pgtest_probe`).Scan(&got); err != nil {
				t.Fatalf("select: %v", err)
			}
			if got != marker {
				t.Fatalf("marker = %q, want %q (cross-test leakage)", got, marker)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM pgtest_probe`).Scan(&n); err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != 1 {
				t.Fatalf("count = %d, want 1 (cross-test leakage)", n)
			}
		}
	}
	t.Run("a", run("only-a"))
	t.Run("b", run("only-b"))
}

// TestDSNWithDatabase covers both accepted connection-string shapes.
func TestDSNWithDatabase(t *testing.T) {
	cases := []struct{ in, db, want string }{
		{"postgres://u:p@h:5/postgres?sslmode=disable", "x", "postgres://u:p@h:5/x?sslmode=disable"},
		{"host=h port=5 dbname=postgres user=u", "x", "host=h port=5 user=u dbname=x"},
	}
	for _, c := range cases {
		got, err := dsnWithDatabase(c.in, c.db)
		if err != nil {
			t.Fatalf("dsnWithDatabase(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("dsnWithDatabase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func seed(t *testing.T, db *sql.DB, marker string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE pgtest_probe (marker TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO pgtest_probe (marker) VALUES ($1)`, marker); err != nil {
		t.Fatalf("insert: %v", err)
	}
}
