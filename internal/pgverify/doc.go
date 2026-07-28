// Package pgverify proves — not merely asserts — that a running kitsoki
// deployment is genuinely persisting to its configured Postgres backend and
// is functionally correct on it. It exists because "--db-backend postgres is
// in the systemd ExecStart" and "the process started" are not evidence: a
// misconfigured deployment can start cleanly while silently still reading or
// writing SQLite, or degrade under concurrency in ways a smoke test never
// exercises.
//
// Every check here is built to fail closed: if a property cannot be
// independently observed (missing input, unreachable dependency, ambiguous
// signal), the check reports [StatusNotEstablished] rather than assuming
// success. Nothing is accepted on the strength of a flag, an environment
// variable, or a value this package itself declared.
//
// Checks, and how each is independently established:
//
//  1. Backend identity ([CheckBackendIdentity]): queries Postgres's own
//     pg_stat_activity for live connections whose application_name is
//     store.ApplicationName — a signal Postgres records about a real
//     connection, not something the kitsoki process merely claims about
//     itself (see store.ApplicationName's doc comment for why this stamp
//     was added to OpenPostgresDSN).
//  2. Write-read round trip ([CheckRoundTrip]): creates a real, clearly
//     namespaced session through store.Store (the exact code path a live
//     kitsoki process runs — internal/store/postgres.go), appends an event,
//     and reads it back through the same Store interface.
//  3. Data landed in Postgres ([CheckRoundTrip], DirectRows sub-result):
//     re-reads the exact rows written in (2) via a raw SQL query that never
//     goes through internal/store, so nothing about the round trip could be
//     served from an in-process cache or a mock.
//  4. SQLite is not being written ([CheckSQLiteInert]): stats the legacy
//     sqlite file (and its -wal sidecar) before and after the round trip in
//     (2), across a real wall-clock window, and asserts mtime/size are
//     unchanged. Distinguishes "absent" (retired), "present but inert", and
//     "actively written" (fail).
//  5. Migrated data is intact ([CheckMigratedDataIntact]): reuses
//     internal/dbmigrate's own PreflightShared row counts against
//     operator-supplied expected counts, and spot-reads pre-existing
//     sessions through store.Store, cross-checked against a raw event count
//     for the same session.
//  6. stream_pos monotonicity under concurrency ([CheckStreamMonotonicity]):
//     drives concurrent AppendEvents across several namespaced sessions and
//     tails each via the real EventStream (ReadStream/WaitForEvents),
//     verifying no gap and no duplicate in what a real durable consumer
//     would see.
//  7. Type fidelity ([CheckTypeFidelity]): round-trips a BYTEA payload
//     through the real study.governed_studies table (the schema's one
//     BLOB-typed column) and a microsecond-precision timestamp through the
//     events table, both compared byte-for-byte / value-for-value against
//     what was written.
//
// All test data this package creates is namespaced under [MarkerPrefix] and
// removed after each check runs (or left in place, for operator inspection,
// when Options.KeepTestData is set) — this is safe to run against a live
// host and never touches pre-existing rows.
package pgverify
