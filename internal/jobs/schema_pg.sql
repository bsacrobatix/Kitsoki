-- Jobs and notifications tables, Postgres flavor.
-- Mirrors schema.sql with schema-per-subsystem qualification ("jobs" schema)
-- and BIGINT for the unix-millisecond timestamps (Postgres INTEGER is 32-bit).
-- owner_pid is present from day one, so the SQLite compatibility ALTER in
-- NewJobStore is skipped on this dialect.

CREATE SCHEMA IF NOT EXISTS jobs;

CREATE TABLE IF NOT EXISTS jobs.jobs (
  id                   TEXT PRIMARY KEY,        -- ULID
  session_id           TEXT NOT NULL,
  kind                 TEXT NOT NULL,           -- handler name, e.g., "host.run_tests"
  status               TEXT NOT NULL,           -- running|awaiting_input|done|failed|cancelled
  origin_state         TEXT NOT NULL,           -- room where spawned
  origin_proposal_id   TEXT,                    -- nullable; jobs spawned outside a proposal
  payload              TEXT NOT NULL,           -- JSON: the `with` args passed to execute
  progress             TEXT,                    -- JSON: latest snapshot (overwritten)
  result               TEXT,                    -- JSON: on terminal status
  error                TEXT,                    -- string: on failed
  clarification_schema TEXT,                    -- JSON: set while awaiting_input
  clarification_answer TEXT,                    -- JSON: once submitted
  retry_count          BIGINT NOT NULL DEFAULT 0,
  created_at           BIGINT NOT NULL,         -- unix ms (queued)
  updated_at           BIGINT NOT NULL,
  started_at           BIGINT,                  -- actual handler start
  finished_at          BIGINT,                  -- terminal timestamp
  owner_pid            BIGINT                   -- owning scheduler process; see NewJobStore
);

CREATE INDEX IF NOT EXISTS jobs_session_status  ON jobs.jobs(session_id, status);
CREATE INDEX IF NOT EXISTS jobs_session_created ON jobs.jobs(session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS jobs.notifications (
  id                   TEXT PRIMARY KEY,        -- ULID
  session_id           TEXT NOT NULL,
  created_at           BIGINT NOT NULL,
  read_at              BIGINT,                  -- NULL if unread
  dismissed_at         BIGINT,                  -- NULL if active
  snoozed_until        BIGINT,                  -- NULL if not snoozed
  severity             TEXT NOT NULL,           -- info|success|warn|error|action_required
  title                TEXT NOT NULL,
  body                 TEXT,                    -- markdown
  teleport_state       TEXT NOT NULL,
  teleport_slots       TEXT,                    -- JSON
  teleport_proposal_id TEXT,                    -- nullable
  teleport_job_id      TEXT,                    -- nullable
  origin_kind          TEXT NOT NULL,           -- job|external
  origin_ref           TEXT NOT NULL,           -- e.g., "job:abc", "github:pr/123"
  origin_url           TEXT                     -- external deep link if any
);

CREATE INDEX IF NOT EXISTS notif_session_unread  ON jobs.notifications(session_id, read_at, severity);
CREATE INDEX IF NOT EXISTS notif_session_created ON jobs.notifications(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS notif_dedup           ON jobs.notifications(session_id, origin_kind, origin_ref);
