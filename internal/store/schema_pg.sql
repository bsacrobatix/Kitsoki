-- schema_pg.sql — Kitsoki event-sourced session store DDL, Postgres dialect.
-- Embedded via go:embed in postgres.go and executed idempotently on OpenPostgres().
-- The SQLite schema (schema.sql) remains the default; this file is the
-- explicitly-selected Postgres variant. Statements are separated by ";" and
-- executed one at a time (pgx extended protocol rejects multi-command strings),
-- so comments here must not contain semicolons.

-- Session metadata (one row per session). Unlike the SQLite projection this
-- table carries a real PRIMARY KEY so GROUP BY s.id can select the other
-- session columns via functional dependency.
CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT    NOT NULL PRIMARY KEY,
    app_id       TEXT    NOT NULL,
    app_version  TEXT    NOT NULL,
    started_at   BIGINT  NOT NULL,   -- unix microseconds
    last_turn    BIGINT  NOT NULL,   -- last turn number written
    status       TEXT    NOT NULL    -- "active" | "completed" | "abandoned"
);

-- Append-only event log, FULL fidelity. The SQLite projection drops
-- state_path, call_id, parent_turn, episode_id and match_idx on write - the
-- Postgres table persists them as real columns so LoadHistory round-trips the
-- complete Event. stream_pos is a global monotonic position over ALL sessions,
-- reserved as the durable-stream cursor for future tailing consumers.
CREATE TABLE IF NOT EXISTS events (
    session_id   TEXT    NOT NULL,
    turn         BIGINT  NOT NULL,
    seq          INTEGER NOT NULL,
    ts           BIGINT  NOT NULL,   -- unix microseconds, monotonic-within-turn
    kind         TEXT    NOT NULL,
    payload_json TEXT    NOT NULL,
    state_path   TEXT    NOT NULL DEFAULT '',
    call_id      TEXT    NOT NULL DEFAULT '',
    parent_turn  BIGINT  NOT NULL DEFAULT 0,
    episode_id   TEXT    NOT NULL DEFAULT '',
    match_idx    INTEGER NOT NULL DEFAULT 0,
    stream_pos   BIGSERIAL,
    PRIMARY KEY (session_id, turn, seq)
);

-- Index on (session_id, kind) for efficient event-kind queries.
CREATE INDEX IF NOT EXISTS events_session_kind_idx ON events (session_id, kind);

-- The stream cursor must be scannable in order.
CREATE UNIQUE INDEX IF NOT EXISTS events_stream_pos_idx ON events (stream_pos);

-- Periodic materialized snapshots, one per N turns (default N=20).
CREATE TABLE IF NOT EXISTS snapshots (
    session_id   TEXT    NOT NULL,
    turn         BIGINT  NOT NULL,
    state_path   TEXT    NOT NULL,
    world_json   TEXT    NOT NULL,
    rng_seed     BIGINT  NOT NULL,
    PRIMARY KEY (session_id, turn)
);

-- External-key index: maps (transport, thread) to a session_id. One session
-- may carry multiple keys, the (transport, thread) pair is unique.
CREATE TABLE IF NOT EXISTS external_keys (
    transport   TEXT    NOT NULL,
    thread      TEXT    NOT NULL,
    session_id  TEXT    NOT NULL,
    created_at  BIGINT  NOT NULL,
    PRIMARY KEY (transport, thread)
);
CREATE INDEX IF NOT EXISTS external_keys_session_idx ON external_keys (session_id);

-- Session-level writer lock, LEASE-based (unlike the SQLite table, which is
-- PID-liveness based and only meaningful on one host). holder_id is an opaque
-- string minted per store instance. A lease is live until expires_at, the
-- holder heartbeats while its critical section runs, and any acquirer may
-- atomically take over a lease whose expires_at has passed. See WithWriterLock
-- in postgres.go.
CREATE TABLE IF NOT EXISTS session_locks (
    session_id   TEXT    NOT NULL PRIMARY KEY,
    holder_id    TEXT    NOT NULL,
    acquired_at  BIGINT  NOT NULL,   -- unix microseconds
    heartbeat_at BIGINT  NOT NULL,   -- unix microseconds
    expires_at   BIGINT  NOT NULL    -- unix microseconds
);

-- Durable session journal, written atomically alongside events by
-- AppendEventsAndJournal. Same shape as the SQLite table: patch entries carry
-- a (doc, doc_version) pair, typed-only entries leave both NULL, checkpoints
-- use the "<doc>.checkpoint" kind value.
CREATE TABLE IF NOT EXISTS journal (
    session_id   TEXT    NOT NULL,
    turn         BIGINT  NOT NULL,
    seq          INTEGER NOT NULL,
    ts           BIGINT  NOT NULL,   -- unix microseconds
    kind         TEXT    NOT NULL,
    doc          TEXT,               -- nullable for typed-only entries
    doc_version  BIGINT,             -- nullable for typed-only entries
    body_json    TEXT    NOT NULL,
    PRIMARY KEY (session_id, turn, seq)
);

CREATE INDEX IF NOT EXISTS journal_doc_idx ON journal (session_id, doc, doc_version);
