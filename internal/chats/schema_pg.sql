-- PostgreSQL flavor of the chats schema. Applied idempotently by
-- chats.NewPostgresStore via //go:embed, one statement at a time (the pgx
-- extended query protocol rejects multi-statement strings), inside a single
-- transaction.
--
-- Kept column-for-column in sync with schema.sql except where the engines
-- genuinely differ:
--   * INTEGER micros columns become BIGINT (SQLite INTEGER is 64-bit, and
--     the Go code scans int64 UnixMicro values)
--   * no STRICT (SQLite-only) and no PRAGMA user_version -- the version is
--     stamped in chats_schema_meta instead
--   * chat_locks is a lease table (holder + expires_at) rather than the
--     SQLite pid/host liveness table. PID liveness probes are meaningless
--     against a shared server, so takeover is time-based (see lock.go)
--   * everything lives in a dedicated "chats" schema
--     (schema-per-subsystem on a shared database)

CREATE SCHEMA IF NOT EXISTS chats;

CREATE TABLE IF NOT EXISTS chats.chats (
    id                  TEXT    NOT NULL PRIMARY KEY,
    app_id              TEXT    NOT NULL,
    room                TEXT    NOT NULL,
    scope_key           TEXT    NOT NULL DEFAULT '',
    title               TEXT    NOT NULL,
    status              TEXT    NOT NULL,
    claude_session_id   TEXT,
    parent_chat_id      TEXT,
    session_id          TEXT,
    created_at          BIGINT  NOT NULL,
    updated_at          BIGINT  NOT NULL,
    last_active_at      BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS chats_room_scope ON chats.chats(app_id, room, scope_key, last_active_at DESC);
CREATE INDEX IF NOT EXISTS chats_status     ON chats.chats(status, last_active_at DESC);
CREATE INDEX IF NOT EXISTS chats_parent     ON chats.chats(parent_chat_id);

CREATE TABLE IF NOT EXISTS chats.chat_messages (
    chat_id     TEXT    NOT NULL,
    seq         BIGINT  NOT NULL,
    role        TEXT    NOT NULL CHECK (role IN ('user','assistant','system','tool')),
    content     TEXT    NOT NULL,
    metadata    TEXT,
    created_at  BIGINT  NOT NULL,
    PRIMARY KEY (chat_id, seq)
);

-- Lease-based per-chat lock. holder is an opaque per-process token, and a
-- lease whose expires_at has passed may be taken over by any acquirer.
-- Heartbeat extends expires_at. No semicolons in comments here -- the Go
-- statement splitter cuts on them.
CREATE TABLE IF NOT EXISTS chats.chat_locks (
    chat_id      TEXT    NOT NULL PRIMARY KEY,
    holder       TEXT    NOT NULL,
    acquired_at  BIGINT  NOT NULL,
    heartbeat_at BIGINT  NOT NULL,
    expires_at   BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS chats.chat_pty_sessions (
    chat_id         TEXT    NOT NULL PRIMARY KEY,
    tmux_session    TEXT    NOT NULL,
    tmux_host       TEXT    NOT NULL,
    mode            TEXT    NOT NULL CHECK (mode IN ('pty_attached','pty_background')),
    permission_mode TEXT    NOT NULL DEFAULT '',
    workspace_path  TEXT    NOT NULL DEFAULT '',
    created_at      BIGINT  NOT NULL,
    updated_at      BIGINT  NOT NULL,
    last_idle_at    BIGINT
);

CREATE TABLE IF NOT EXISTS chats.chat_input_queue (
    drive_id          TEXT    NOT NULL PRIMARY KEY,
    chat_id           TEXT    NOT NULL,
    transport         TEXT    NOT NULL,
    thread            TEXT    NOT NULL DEFAULT '',
    actor             TEXT    NOT NULL DEFAULT '',
    correlation_id    TEXT    NOT NULL DEFAULT '',
    payload           TEXT    NOT NULL,
    status            TEXT    NOT NULL CHECK (status IN ('pending','dispatching','done','failed','dismissed')),
    received_at       BIGINT  NOT NULL,
    dispatched_at     BIGINT,
    completed_at      BIGINT,
    result_seq        BIGINT,
    error_message     TEXT    NOT NULL DEFAULT '',
    on_complete_json  TEXT    NOT NULL DEFAULT '',
    origin_session_id TEXT    NOT NULL DEFAULT '',
    origin_state      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS chat_input_queue_by_chat
    ON chats.chat_input_queue(chat_id, status, received_at);
CREATE INDEX IF NOT EXISTS chat_input_queue_pending_oncomplete
    ON chats.chat_input_queue(status, origin_session_id)
    WHERE on_complete_json != '' AND status IN ('done','failed');

-- Schema version, the Postgres stand-in for SQLite's PRAGMA user_version.
-- Bump in lockstep with `expectedSchemaVersion` in store.go
CREATE TABLE IF NOT EXISTS chats.chats_schema_meta (
    id      INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);
INSERT INTO chats.chats_schema_meta (id, version) VALUES (1, 3)
    ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version;
