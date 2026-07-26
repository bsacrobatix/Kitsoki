# Storage Backends — SQLite, Postgres, and the Durable Event Stream

Kitsoki's session store, its satellite stores, and the trace stream can run on
one of three backends. SQLite is the default and behaviorally unchanged; the
Postgres backends are explicitly opt-in. This document is the contract for
backend selection, the schema layout, lease-based locking, the full-fidelity
events table with its stream cursor, and the consumers built on top of it
(runstatus cursor reads, trace export, mining offsets, durable receipts).

Retiring SQLite is an explicit future cutover decision, not something any of
this machinery does implicitly. Nothing here changes local behavior unless a
Postgres backend is selected.

## Backend selection

The resolver lives in `cmd/kitsoki/db_backend.go`. Selection comes from the
persistent `--db-backend` flag or the `KITSOKI_DB_BACKEND` environment
variable (the flag wins, matching `--kitsoki-repo` precedence):

| Backend | Meaning |
|---|---|
| `sqlite` (default, also empty) | Exactly `store.Open(dbPath)` — today's path, byte-for-byte. |
| `postgres` | External Postgres. Requires a DSN via `--pg-dsn` or `KITSOKI_PG_DSN`; refuses to start without one. |
| `embedded-postgres` | One process-shared embedded Postgres server (`internal/dbruntime`), started lazily on first store open and stopped when the root command returns. |

Details worth knowing:

- The default SQLite path is `$XDG_DATA_HOME/kitsoki/sessions.db`
  (`defaultDBPath` in `cmd/kitsoki/main.go`); when `KITSOKI_STATE_DIR` is set
  it is `<state>/sessions.db` (see
  [`../deploy/stateless-container.md`](../deploy/stateless-container.md)).
- The embedded backend keeps its data dir next to the session db
  (`dir(sessions.db)/pg`) and its binary download cache under
  `os.UserCacheDir()/kitsoki/embedded-pg` (or `<state>/cache/embedded-pg`
  under `KITSOKI_STATE_DIR`).
- A set `KITSOKI_PG_DSN` wins inside `dbruntime.Start` even for
  `embedded-postgres`: passthrough mode never starts an embedded server.
- Tests that need Postgres use `internal/dbruntime`'s pgtest support
  (per-test isolated databases on the embedded runtime) — never a network
  database.

## Schema-per-subsystem layout

All Postgres state shares one database. The core session store
(`internal/store/schema_pg.sql`) lives in the default schema; every satellite
store owns its own Postgres schema, created idempotently on open, with no
cross-schema foreign keys — each subsystem stays independently portable:

| Schema | Owner | Contents |
|---|---|---|
| (default) | `internal/store` | `sessions`, `events`, `snapshots`, `external_keys`, `session_locks`, `journal` |
| `artifactjob` | `internal/artifactjob` | artifact-job rows |
| `jobs` | `internal/jobs` | background-job scheduler state |
| `chats` | `internal/chats` | chat state, lease locks with background heartbeat |
| `study` | `internal/study` | governed studies |
| `turncache` | `internal/turncache` | routing turn cache |
| `webauth` | `internal/webauth` | web auth state |
| `endpoint` | `internal/capsule/endpoint` | capsule endpoint registry |
| `mining` | `internal/mining` | `consumer_offsets` (see below) |
| `studio` | `internal/mcp/studio` | `objectives`, `objective_receipts` (see below) |
| `graph` | `internal/graph/pgcatalog` | graph catalogs — see [`graph-storage.md`](graph-storage.md) |

Satellite construction dispatches on the shared handle's dialect through the
`newChatStore` / `newJobStore` / `newArtifactJobStore` / `newStudyStore` /
`newJournalWriter` helpers in `cmd/kitsoki/db_backend.go`, so every satellite
speaks the backend the session store was opened with.

## Lease-based locking

SQLite's `session_locks` is PID-liveness based, which is only meaningful on
one host. The Postgres `session_locks` table is **lease-based**: an opaque
`holder_id` minted per store instance, `heartbeat_at` refreshed while the
critical section runs, and `expires_at` after which any acquirer may
atomically take over the lease (`WithWriterLock` in
`internal/store/postgres.go`). This is the one deliberate semantic change in
the port — everything else is dialect translation.

Consequences elsewhere:

- `internal/chats` uses the same lease pattern with a background heartbeat.
- `jobs.SweepStaleJobs` is a conservative no-op under Postgres: cross-host
  PIDs cannot prove liveness, and stale rows are safer than killing live work.

## The full-fidelity events table

The SQLite events projection is lossy: it drops `state_path`, `call_id`,
`parent_turn`, `episode_id`, and `match_idx` on write (the JSONL trace file
is the canonical record there — see
[`../tracing/trace-format.md`](../tracing/trace-format.md)). The Postgres
`events` table persists all of them as real columns, so `LoadHistory`
round-trips the complete event and the table can serve as the durable record
for pg-backed sessions.

It also carries `stream_pos BIGSERIAL` — a global monotonic position across
**all** sessions, unique-indexed, exposed as the durable-stream cursor.

### EventStream cursor contract

`internal/store/stream.go` defines the optional read surface; only backends
with a global stream position implement it (today: the Postgres store).
Discover it with `store.AsEventStream(s)` — SQLite and memory stores return
`(nil, false)` and callers keep their existing history reads.

```go
type EventStream interface {
    ReadStream(ctx, after StreamCursor, limit int) ([]StreamEntry, error)
    ReadSessionStream(ctx, session app.SessionID, after StreamCursor, limit int) ([]StreamEntry, error)
    WaitForEvents(ctx, after StreamCursor) error
}
```

The contract, and how it is enforced (`internal/store/postgres_stream.go`):

- **No-miss ordering.** Every append takes
  `pg_advisory_xact_lock(pgStreamAppendLockKey)` before inserting, so
  `stream_pos` assignment order equals commit order. A cursor advanced to the
  `Pos` of the last consumed entry can never skip a row that commits later.
- **Positions are not dense.** A rolled-back append leaves a permanent,
  never-filled gap in the sequence. Cursors are opaque and monotonic, not
  contiguous — never arithmetic on them.
- **Wake-ups: LISTEN/NOTIFY with poll fallback.** Appends `pg_notify` on the
  `kitsoki_events` channel inside the same transaction. `WaitForEvents` uses
  a native pgx connection to `LISTEN` (database/sql cannot), re-checks the
  cursor on the listening connection after `LISTEN` to close the subscribe
  race, and falls back to periodic re-checks — the cursor read is the source
  of truth, notifications are only an accelerant, so a lost notification
  delays a return but never loses an event.
- **Honest cost.** The advisory lock globally serializes event appends across
  all sessions in the database. That is the price of the no-skip guarantee;
  fine at current scale, and the documented lever if append throughput ever
  matters. Likewise the notify channel is global — every tailing reader wakes
  on every session's append (a per-session channel is a known follow-up).

### runstatus cursor reads

`internal/runstatus/server/session_events.go` builds two additive surfaces on
`EventStream`, engaged only when a session's store exposes it:

- **`runstatus.session.events`** RPC: `{session_id, since?, limit?}` →
  `{events, next_cursor, live}`. `since` is the opaque int64 stream cursor
  (0 = beginning); `next_cursor` is the `Pos` of the last returned event
  (or the request's `since` on an empty page) and is passed back as `since`
  to resume exactly after it. `live: true` marks the answer as served from
  the durable stream, so the cursor stays valid across server restarts.
- **Cursor-driven SSE** inside `GET /rpc/events`: the handler blocks on
  `WaitForEvents` and reads by cursor instead of re-mapping the whole history
  on the 500 ms ticker. Wire frames are the same `runstatus.event` /
  `runstatus.session_gone` notifications, with one additive `cursor` params
  field; an optional `since` query parameter lets a client resume without
  loss after a server restart.

Non-stream sources (SQLite, trace files, in-memory test sources) are
untouched: the RPC reports stream-unsupported and the SSE handler keeps the
byte-identical ticker path. The runstatus SPA itself has not yet been moved
onto the cursor surfaces.

## Trace export

For pg-backed sessions there is no JSONL trace file on disk; `kitsoki trace
export` reconstructs it:

```
kitsoki trace export --session <id> [--out <path>] [--app <id>] [--db <path>]
```

The output is byte-compatible with what the live JSONL sink would have
written — same header shape, per-event encoding, and field order, and
deterministic (same stored session, same bytes) — so `kitsoki trace`,
`trace to-flow`, `trace status`, and the mining pipeline keep working
unchanged. Divergences are documented on `store.ExportTraceJSONL`
(`internal/store/trace_export.go`): the header `written_at` is the session's
`started_at`, timestamps are microsecond precision, and fields a backend did
not persist are omitted, never invented. It works against SQLite too, at
SQLite's lower persisted fidelity.

File JSONL remains the canonical trace for the SQLite/local default; export
is the bridge, not a replacement.

## Mining consumer offsets

On a Postgres backend the session miner's per-slug watermark ledger becomes
the durable `mining.consumer_offsets` table (`internal/mining/watermark_pg.go`,
`PGWatermarkStore` behind the existing `WatermarkStore` interface). The config
ledger seeds only slugs the table has never seen — durable rows win. The table
also carries a `stream_cursor` column reserved for future stream-driven mining
(consuming `stream_pos` instead of file mtimes). SQLite/local keeps the
in-memory map store and file/mtime behavior byte-for-byte.

## Durable objective receipts

`internal/mcp/studio/objective_pg.go` adds `PostgresObjectiveStore`:
append-only receipts in `studio.objectives` / `studio.objective_receipts`
with transaction-assigned per-objective sequences and a DB-level immutability
trigger. It is an opt-in constructor; `MemoryObjectiveStore` (receipts lost on
process exit) remains the default, and **the Postgres receipts store is not
yet wired into a default production construction path** — that is a known
follow-up, not an implied behavior.

## See also

- [`graph-storage.md`](graph-storage.md) — the graph catalog's storage seam
  and Postgres backend.
- [`../deploy/stateless-container.md`](../deploy/stateless-container.md) —
  the deployment shape these backends exist for.
- [`../tracing/trace-format.md`](../tracing/trace-format.md) — the canonical
  JSONL trace contract.
