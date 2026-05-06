package chats

import (
	"database/sql"
	"testing"
)

// TestDB is a test helper that exposes the underlying *sql.DB of a Store.
// Only available in test builds.
type TestDB struct {
	db *sql.DB
}

// DBForTest returns a TestDB wrapping the Store's underlying *sql.DB.
// Used by lock_test.go to insert stale lock rows directly.
func (s *Store) DBForTest() *TestDB {
	return &TestDB{db: s.db}
}

// MustExec executes a SQL statement and fatals the test on error.
func (t *TestDB) MustExec(tb testing.TB, query string, args ...any) {
	tb.Helper()
	if _, err := t.db.Exec(query, args...); err != nil {
		tb.Fatalf("MustExec(%q): %v", query, err)
	}
}

// MustQueryInt runs a 1-column query that returns a single int value
// (typical: `SELECT count(*) FROM ...`) and fatals on error.
func (t *TestDB) MustQueryInt(tb testing.TB, query string, args ...any) int {
	tb.Helper()
	var n int
	if err := t.db.QueryRow(query, args...).Scan(&n); err != nil {
		tb.Fatalf("MustQueryInt(%q): %v", query, err)
	}
	return n
}
