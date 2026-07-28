package pgverify

import (
	"database/sql"
	"time"

	"kitsoki/internal/store"
)

// MarkerPrefix namespaces every piece of test data this package creates
// (session app_id, study idempotency_key, ...), so it is trivially
// identifiable and never collides with real application data.
const MarkerPrefix = "kitsoki-pgverify-"

// Status is the outcome of one check. There is no "pass with caveats":
// anything not independently confirmed is [StatusNotEstablished], never
// [StatusPass].
type Status string

const (
	// StatusPass means the property was independently observed to hold.
	StatusPass Status = "pass"
	// StatusFail means the property was independently observed to NOT hold
	// — a real, actionable negative (e.g. the sqlite file's mtime moved).
	StatusFail Status = "fail"
	// StatusNotEstablished means this package could not honestly determine
	// the property either way (missing input, unreachable dependency,
	// probe error). Treated as failure for exit-code purposes: a
	// verification tool that reports green when it could not check
	// something is worse than no tool.
	StatusNotEstablished Status = "not_established"
)

// CheckResult is the outcome of one independently-run check.
type CheckResult struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Status   Status         `json:"status"`
	Detail   string         `json:"detail"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

// ok reports whether r counts as a pass for report-level rollup.
func (r CheckResult) ok() bool { return r.Status == StatusPass }

// notEstablished builds a CheckResult in the fail-closed StatusNotEstablished
// state with the given reason, optionally carrying evidence about what was
// (or was not) available.
func notEstablished(id, name, reason string, evidence map[string]any) CheckResult {
	return CheckResult{ID: id, Name: name, Status: StatusNotEstablished, Detail: reason, Evidence: evidence}
}

func fail(id, name, reason string, evidence map[string]any) CheckResult {
	return CheckResult{ID: id, Name: name, Status: StatusFail, Detail: reason, Evidence: evidence}
}

func pass(id, name, reason string, evidence map[string]any) CheckResult {
	return CheckResult{ID: id, Name: name, Status: StatusPass, Detail: reason, Evidence: evidence}
}

// Options parameterizes a verification run. Every external dependency
// (clock, sleep, filesystem stat) is injectable so tests can drive the exact
// same code paths hermetically and adversarially — see pgverify_test.go.
type Options struct {
	// PGDB is the already-opened Postgres handle the tool queries directly
	// for identity, direct-row, migrated-data, concurrency, and type-
	// fidelity checks. Required for every check except CheckSQLiteInert.
	//
	// Deliberately NOT the connection the round trip / concurrency checks
	// write through — see OpenServiceStore. If this handle's own
	// application_name happened to equal store.ApplicationName,
	// CheckBackendIdentity would count it as "the real service", producing
	// a false pass; production wiring (cmd/kitsoki/db_verify.go) opens PGDB
	// with a distinct, clearly-probe application_name for exactly this
	// reason.
	PGDB *sql.DB

	// OpenServiceStore opens the store.Store the round-trip and
	// concurrency checks write and read through — the actual
	// internal/store/postgres.go code path a live kitsoki process runs,
	// not a mock. Required for CheckRoundTrip and CheckStreamMonotonicity.
	//
	// Production wiring opens this against the SAME Postgres server as
	// PGDB but with application_name stamped distinctly (e.g.
	// "kitsoki-pgverify"), so the verifier's own connections can never be
	// mistaken for the deployed service's in pg_stat_activity. Tests may
	// wrap the same *sql.DB pgtest hands out via store.OpenPostgres,
	// since a fake/injected CheckBackendIdentity result — not a real
	// pg_stat_activity query — is what exercises that check hermetically.
	OpenServiceStore func() (store.Store, error)

	// SQLitePath is the legacy sqlite session-store path to prove inert.
	// Empty means CheckSQLiteInert reports StatusNotEstablished — this
	// package never assumes a default path, since guessing wrong here would
	// silently produce a false pass.
	SQLitePath string

	// SQLiteSettleWindow is how long CheckSQLiteInert waits, bracketing the
	// round trip, before re-statting the sqlite file. Longer windows give a
	// slow WAL checkpoint more chance to reveal itself; shorter windows keep
	// the tool fast. Zero means DefaultSQLiteSettleWindow.
	SQLiteSettleWindow time.Duration

	// ExpectedCounts maps a dbmigrate table label (dbmigrate.tableSpec.Label
	// — see internal/dbmigrate) to the minimum row count CheckMigratedDataIntact
	// requires. Nil/empty means that check reports StatusNotEstablished:
	// this package never invents an expectation to compare against.
	ExpectedCounts map[string]int64

	// SampleSessionIDs, when non-empty, pins CheckMigratedDataIntact's
	// spot-read to these specific pre-existing session IDs (e.g. carried
	// over from a migration dry-run report) instead of an auto-selected
	// random sample.
	SampleSessionIDs []string

	// ConcurrencyWriters / ConcurrencyAppendsPerWriter / ConcurrencyEventsPerAppend
	// size CheckStreamMonotonicity's load. Zero means DefaultConcurrency*.
	ConcurrencyWriters          int
	ConcurrencyAppendsPerWriter int
	ConcurrencyEventsPerAppend  int

	// AssumeExclusiveWindow, when true, additionally asserts GLOBAL
	// stream_pos contiguity (no other writer touched the events table)
	// across the exact pos range CheckStreamMonotonicity's writers used.
	// Only safe to set when the operator knows no other traffic is hitting
	// this Postgres during the run (e.g. a maintenance window) — on a live
	// production host with concurrent real traffic this assumption does not
	// hold and must be left false, in which case the check still proves the
	// no-gap/no-dup property for the tool's OWN rows (the property that
	// matters for a durable consumer), just not global exclusivity.
	AssumeExclusiveWindow bool

	// KeepTestData disables cleanup of everything this package creates
	// (sessions, events, the study probe row). Off by default; useful for
	// an operator who wants to inspect the created rows by hand.
	KeepTestData bool

	// Now returns the current time; defaults to time.Now. Overridable so
	// tests can control timestamps deterministically.
	Now func() time.Time

	// Sleep pauses for d; defaults to time.Sleep. Overridable so tests can
	// fake the SQLite settle window without a real wall-clock wait.
	Sleep func(d time.Duration)
}

// DefaultSQLiteSettleWindow is applied when Options.SQLiteSettleWindow is zero.
const DefaultSQLiteSettleWindow = 3 * time.Second

// Default concurrency sizing for CheckStreamMonotonicity.
const (
	DefaultConcurrencyWriters          = 6
	DefaultConcurrencyAppendsPerWriter = 15
	DefaultConcurrencyEventsPerAppend  = 3
)

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) sleep(d time.Duration) {
	if o.Sleep != nil {
		o.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (o Options) sqliteSettleWindow() time.Duration {
	if o.SQLiteSettleWindow > 0 {
		return o.SQLiteSettleWindow
	}
	return DefaultSQLiteSettleWindow
}

func (o Options) concurrencyWriters() int {
	if o.ConcurrencyWriters > 0 {
		return o.ConcurrencyWriters
	}
	return DefaultConcurrencyWriters
}

func (o Options) concurrencyAppendsPerWriter() int {
	if o.ConcurrencyAppendsPerWriter > 0 {
		return o.ConcurrencyAppendsPerWriter
	}
	return DefaultConcurrencyAppendsPerWriter
}

func (o Options) concurrencyEventsPerAppend() int {
	if o.ConcurrencyEventsPerAppend > 0 {
		return o.ConcurrencyEventsPerAppend
	}
	return DefaultConcurrencyEventsPerAppend
}

// Report is the complete, machine-readable evidence artifact for one
// verification run.
type Report struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Checks      []CheckResult `json:"checks"`
}

// OK reports whether every check passed. Any StatusFail or
// StatusNotEstablished makes the whole report not-OK.
func (r Report) OK() bool {
	if len(r.Checks) == 0 {
		return false
	}
	for _, c := range r.Checks {
		if !c.ok() {
			return false
		}
	}
	return true
}

// ExitCode returns 0 when every check passed, 1 otherwise — the CLI's
// process exit code, so a caller (CI, a shell script, an operator's
// terminal) can rely on it without parsing JSON.
func (r Report) ExitCode() int {
	if r.OK() {
		return 0
	}
	return 1
}
