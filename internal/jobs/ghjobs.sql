CREATE TABLE IF NOT EXISTS gh_jobs (
    job_id        TEXT PRIMARY KEY,        -- ULID (or hash of origin_ref); minted on first insert
    origin_ref    TEXT NOT NULL UNIQUE,    -- inbox.GitHubOriginRef: github:<repo>/<kind>/<number> — the natural key
    repo          TEXT NOT NULL,
    object_kind   TEXT NOT NULL,           -- 'issue' | 'pr'
    object_number TEXT NOT NULL,
    story         TEXT,                     -- chosen story path (NULL while guidance-parked)
    state         TEXT NOT NULL,            -- queued|claimed|running|awaiting_guidance|done|failed
    worker_id     TEXT,                     -- holder of the claim (NULL when unclaimed)
    run_id        TEXT,
    run_url       TEXT,
    comment_id    TEXT,                     -- the rolling-status comment id (captured on first Post)
    err_msg       TEXT,
    created_at    INTEGER NOT NULL,         -- unix ms
    updated_at    INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS gh_jobs_origin ON gh_jobs(origin_ref);
