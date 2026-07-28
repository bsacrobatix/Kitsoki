package store

// postgres_dsn_test.go — unit coverage for withApplicationName, the
// application_name stamping OpenPostgresDSN applies so pg_stat_activity can
// independently identify a live kitsoki connection (see ApplicationName's
// doc comment in postgres.go and internal/pgverify for the consumer).
//
// In "package store" (not store_test) because withApplicationName is
// unexported; safe as a pure string-transform unit test with no I/O.

import (
	"net/url"
	"strings"
	"testing"
)

func TestWithApplicationName_URLForm_AddsWhenAbsent(t *testing.T) {
	got, err := WithApplicationName("postgres://user:pass@host:5432/db?sslmode=disable", "kitsoki")
	if err != nil {
		t.Fatalf("withApplicationName: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result %q: %v", got, err)
	}
	if u.Query().Get("application_name") != "kitsoki" {
		t.Fatalf("expected application_name=kitsoki in %q", got)
	}
	if u.Query().Get("sslmode") != "disable" {
		t.Fatalf("expected sslmode to survive in %q", got)
	}
}

func TestWithApplicationName_URLForm_RespectsExplicitCallerChoice(t *testing.T) {
	got, err := WithApplicationName("postgres://host/db?application_name=custom-app", "kitsoki")
	if err != nil {
		t.Fatalf("withApplicationName: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result %q: %v", got, err)
	}
	if u.Query().Get("application_name") != "custom-app" {
		t.Fatalf("expected caller's application_name to survive untouched, got %q", got)
	}
}

func TestWithApplicationName_KeywordForm_AddsWhenAbsent(t *testing.T) {
	got, err := WithApplicationName("host=localhost dbname=kitsoki sslmode=disable", "kitsoki")
	if err != nil {
		t.Fatalf("withApplicationName: %v", err)
	}
	for _, want := range []string{"host=localhost", "dbname=kitsoki", "sslmode=disable", "application_name=kitsoki"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
}

func TestWithApplicationName_KeywordForm_RespectsExplicitCallerChoice(t *testing.T) {
	got, err := WithApplicationName("host=localhost application_name=custom-app", "kitsoki")
	if err != nil {
		t.Fatalf("withApplicationName: %v", err)
	}
	if !strings.Contains(got, "application_name=custom-app") {
		t.Fatalf("expected caller's application_name to survive untouched, got %q", got)
	}
	if strings.Count(got, "application_name=") != 1 {
		t.Fatalf("must not add a second application_name, got %q", got)
	}
}

func TestWithApplicationName_EmptyDSN(t *testing.T) {
	got, err := WithApplicationName("", "kitsoki")
	if err != nil {
		t.Fatalf("withApplicationName: %v", err)
	}
	if got != "application_name=kitsoki" {
		t.Fatalf("expected bare application_name=kitsoki for empty DSN, got %q", got)
	}
}
