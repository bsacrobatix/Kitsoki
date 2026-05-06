-- Chats, messages, and locks tables.
-- Applied idempotently by chats.NewStore via //go:embed.

CREATE TABLE IF NOT EXISTS chats (
    id                  TEXT    NOT NULL PRIMARY KEY,   -- ULID
    app_id              TEXT    NOT NULL,               -- the app the room belongs to
    room                TEXT    NOT NULL,               -- state path: "oracle", "bugfix.phase_3"
    scope_key           TEXT    NOT NULL DEFAULT '',    -- free-form disambiguator (e.g. "PROJ-123")
    title               TEXT    NOT NULL,
    status              TEXT    NOT NULL,               -- active|paused|completed|archived
    claude_session_id   TEXT,                           -- for `claude -p --session-id`
    parent_chat_id      TEXT,                           -- non-null on forks
    session_id          TEXT,                           -- last hally session that drove this chat (audit only)
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    last_active_at      INTEGER NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS chats_room_scope ON chats(app_id, room, scope_key, last_active_at DESC);
CREATE INDEX IF NOT EXISTS chats_status     ON chats(status, last_active_at DESC);
CREATE INDEX IF NOT EXISTS chats_parent     ON chats(parent_chat_id);

CREATE TABLE IF NOT EXISTS chat_messages (
    chat_id     TEXT    NOT NULL,
    seq         INTEGER NOT NULL,
    role        TEXT    NOT NULL CHECK (role IN ('user','assistant','system','tool')),
    content     TEXT    NOT NULL,
    metadata    TEXT,                    -- JSON: tool calls, mcp validation, etc.
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (chat_id, seq)
) STRICT;

CREATE TABLE IF NOT EXISTS chat_locks (
    chat_id      TEXT    NOT NULL PRIMARY KEY,
    owner_pid    INTEGER NOT NULL,
    owner_host   TEXT    NOT NULL,
    acquired_at  INTEGER NOT NULL,
    heartbeat_at INTEGER NOT NULL
) STRICT;

-- Schema version. Bump in lockstep with `expectedSchemaVersion` in store.go
-- whenever the DDL above changes incompatibly. The CREATE TABLE IF NOT
-- EXISTS guards prevent silent re-runs from picking up a new column, so a
-- bump here forces the version-check in NewStore to fail loudly until a
-- migration is provided.
PRAGMA user_version = 1;
