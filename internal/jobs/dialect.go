package jobs

import (
	_ "embed"
	"strconv"
	"strings"
)

//go:embed schema_pg.sql
var jobsSchemaPGDDL string

// Dialect selects the SQL flavor a JobStore emits. SQLite is the zero value
// and the default; Postgres is opted into explicitly via WithDialect at
// construction (the shared *sql.DB is then expected to be a pgx-stdlib
// handle).
type Dialect int

const (
	DialectSQLite Dialect = iota
	DialectPostgres
)

// WithDialect selects the SQL dialect for a JobStore. The default (SQLite)
// keeps every existing caller unchanged; pass DialectPostgres when the shared
// database handle is Postgres so schema migration and query text adapt.
func WithDialect(d Dialect) JobStoreOption {
	return func(js *JobStore) {
		js.dialect = d
	}
}

// jobsPGTables qualifies this subsystem's table references into the "jobs"
// schema (schema-per-subsystem on a shared database). Rewrites are anchored on
// the preceding SQL keyword so unrelated identifiers (gh_jobs, error strings)
// are never touched.
var jobsPGTables = strings.NewReplacer(
	"INTO jobs", "INTO jobs.jobs",
	"FROM jobs", "FROM jobs.jobs",
	"UPDATE jobs", "UPDATE jobs.jobs",
	"INTO notifications", "INTO jobs.notifications",
	"FROM notifications", "FROM jobs.notifications",
	"UPDATE notifications", "UPDATE jobs.notifications",
)

// q adapts a canonical (SQLite-shaped) query to the store's dialect: for
// Postgres it schema-qualifies the table names and converts ?-placeholders to
// $N ordinals. SQLite queries pass through untouched.
func (js *JobStore) q(query string) string {
	if js.dialect != DialectPostgres {
		return query
	}
	return rebindPositional(jobsPGTables.Replace(query))
}

// rebindPositional converts ?-placeholders to Postgres $N ordinals. Queries in
// this package never contain a literal '?'.
func rebindPositional(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}
