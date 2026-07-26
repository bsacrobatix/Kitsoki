package turncache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	// Registers the "pgx" database/sql driver used by callers that hand
	// us a Postgres handle (kitsoki/internal/dbruntime opens with it too).
	_ "github.com/jackc/pgx/v5/stdlib"
)

// pgSchemaDDL is the Postgres translation of schema.sql. Same two tables,
// same columns, same unix-millis timestamp encoding; the only dialect
// changes are type names (BIGINT / DOUBLE PRECISION instead of SQLite's
// INTEGER / REAL), the absence of STRICT, which Postgres enforces by
// being strongly typed anyway, and the dedicated "turncache" schema
// (schema-per-subsystem on a shared database). Every statement is CREATE … IF NOT EXISTS
// so re-applying the DDL on an existing database is a no-op, mirroring
// the SQLite migration posture.
const pgSchemaDDL = `
CREATE SCHEMA IF NOT EXISTS turncache;

CREATE TABLE IF NOT EXISTS turncache.turn_cache (
    app              TEXT             NOT NULL,
    app_hash         TEXT             NOT NULL,
    state_path       TEXT             NOT NULL,
    signature        TEXT             NOT NULL,
    intent           TEXT             NOT NULL,
    slots_json       TEXT             NOT NULL,
    confidence       DOUBLE PRECISION NOT NULL,
    source_model     TEXT,
    source_turn_id   TEXT,

    hit_count        BIGINT           NOT NULL DEFAULT 0,
    last_hit_at      BIGINT,                            -- unix-millis; NULL means never hit.
    last_verified_at BIGINT,                            -- unix-millis; NULL means never verified.
    revalidate_fails BIGINT           NOT NULL DEFAULT 0,

    created_at       BIGINT           NOT NULL,         -- unix-millis.
    PRIMARY KEY (app, app_hash, state_path, signature)
);
CREATE INDEX IF NOT EXISTS turn_cache_lru ON turncache.turn_cache (app, last_hit_at);

CREATE TABLE IF NOT EXISTS turncache.synonym_hits (
    app_hash    TEXT   NOT NULL,
    intent      TEXT   NOT NULL,
    pattern     TEXT   NOT NULL,
    kind        TEXT   NOT NULL CHECK (kind IN ('bare','example','template','enum_value')),
    hit_count   BIGINT NOT NULL DEFAULT 0,
    last_hit_at BIGINT,                                 -- unix-millis.
    PRIMARY KEY (app_hash, intent, pattern, kind)
);
`

// NewPostgres builds a [Cache] over an already-open Postgres handle
// (pgx stdlib driver). The schema is applied idempotently on every call,
// matching NewSQLite's posture. The caller keeps ownership of db — Close
// marks the cache closed but does not close the handle, so one *sql.DB
// can back several subsystems (the injected-handle convention used by
// webauth.NewStore).
//
// Semantics are identical to the SQLite backend and are pinned by the
// shared conformance suite: same NULL encoding for zero times, same
// Put-side LastHitAt stamping, same strike/evict policy.
func NewPostgres(db *sql.DB, cfg Config) (Cache, error) {
	if db == nil {
		return nil, fmt.Errorf("turncache.NewPostgres: nil db")
	}
	if cfg.RevalidateStrikes < 1 {
		cfg.RevalidateStrikes = 1
	}
	if _, err := db.Exec(pgSchemaDDL); err != nil {
		return nil, fmt.Errorf("turncache.NewPostgres: schema migration: %w", err)
	}
	return &pgCache{cfg: cfg, db: db}, nil
}

// pgCache is the Postgres-backed [Cache]. The mutex guards the closed
// flag (Close idempotency) and serialises the composite
// RecordRevalidateFail UPDATE+DELETE, mirroring sqliteCache — Postgres
// row locks would also serialise concurrent strike bumps, but keeping
// the in-process lock keeps the two backends behaviourally identical.
type pgCache struct {
	cfg Config

	mu     sync.Mutex
	db     *sql.DB
	closed bool
}

func (c *pgCache) Get(ctx context.Context, k Key) (CachedVerdict, bool, error) {
	if err := ctx.Err(); err != nil {
		return CachedVerdict{}, false, err
	}
	row := c.db.QueryRowContext(ctx, `
		SELECT intent, slots_json, confidence,
		       COALESCE(source_model, ''), COALESCE(source_turn_id, ''),
		       hit_count, last_hit_at, last_verified_at,
		       revalidate_fails, created_at
		FROM turncache.turn_cache
		WHERE app = $1 AND app_hash = $2 AND state_path = $3 AND signature = $4`,
		k.App, k.AppHash, k.StatePath, k.Signature,
	)
	var (
		v                     CachedVerdict
		lastHit, lastVerified sql.NullInt64
		createdAt             int64
	)
	err := row.Scan(
		&v.Intent, &v.SlotsJSON, &v.Confidence,
		&v.SourceModel, &v.SourceTurnID,
		&v.HitCount, &lastHit, &lastVerified,
		&v.RevalidateFails, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedVerdict{}, false, nil
	}
	if err != nil {
		return CachedVerdict{}, false, fmt.Errorf("turncache.Get: %w", err)
	}
	v.LastHitAt = fromNullableMillis(lastHit)
	v.LastVerifiedAt = fromNullableMillis(lastVerified)
	v.CreatedAt = time.UnixMilli(createdAt).UTC()
	return v, true, nil
}

func (c *pgCache) Put(ctx context.Context, k Key, v CachedVerdict) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	// Same Put-side stamp as the other backends: a caller-zero LastHitAt
	// becomes CreatedAt so a freshly-Put row survives SweepCold.
	if v.LastHitAt.IsZero() {
		v.LastHitAt = v.CreatedAt
	}
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO turncache.turn_cache (
			app, app_hash, state_path, signature,
			intent, slots_json, confidence,
			source_model, source_turn_id,
			hit_count, last_hit_at, last_verified_at,
			revalidate_fails, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (app, app_hash, state_path, signature) DO UPDATE SET
			intent           = EXCLUDED.intent,
			slots_json       = EXCLUDED.slots_json,
			confidence       = EXCLUDED.confidence,
			source_model     = EXCLUDED.source_model,
			source_turn_id   = EXCLUDED.source_turn_id,
			hit_count        = EXCLUDED.hit_count,
			last_hit_at      = EXCLUDED.last_hit_at,
			last_verified_at = EXCLUDED.last_verified_at,
			revalidate_fails = EXCLUDED.revalidate_fails,
			created_at       = EXCLUDED.created_at
		`,
		k.App, k.AppHash, k.StatePath, k.Signature,
		v.Intent, v.SlotsJSON, v.Confidence,
		nullableString(v.SourceModel), nullableString(v.SourceTurnID),
		v.HitCount, nullableMillis(v.LastHitAt), nullableMillis(v.LastVerifiedAt),
		v.RevalidateFails, v.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("turncache.Put: %w", err)
	}
	return nil
}

func (c *pgCache) RecordHit(ctx context.Context, k Key, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	atMillis := at.UnixMilli()
	_, err := c.db.ExecContext(ctx, `
		UPDATE turncache.turn_cache
		SET hit_count = hit_count + 1,
		    last_hit_at = $1,
		    last_verified_at = $2,
		    revalidate_fails = 0
		WHERE app = $3 AND app_hash = $4 AND state_path = $5 AND signature = $6`,
		atMillis, atMillis,
		k.App, k.AppHash, k.StatePath, k.Signature,
	)
	if err != nil {
		return fmt.Errorf("turncache.RecordHit: %w", err)
	}
	return nil
}

func (c *pgCache) RecordRevalidateFail(ctx context.Context, k Key, at time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Same lock discipline as the SQLite backend (see sqliteCache): the
	// read-increment-evict cycle holds the package-level lock so two
	// concurrent strike bumps can never both stop short of the eviction
	// threshold.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false, errors.New("turncache.RecordRevalidateFail: cache is closed")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("turncache.RecordRevalidateFail: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE turncache.turn_cache
		SET revalidate_fails = revalidate_fails + 1
		WHERE app = $1 AND app_hash = $2 AND state_path = $3 AND signature = $4`,
		k.App, k.AppHash, k.StatePath, k.Signature,
	)
	if err != nil {
		return false, fmt.Errorf("turncache.RecordRevalidateFail: update: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("turncache.RecordRevalidateFail: rows affected: %w", err)
	}
	if affected == 0 {
		// Missing row: documented no-op.
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("turncache.RecordRevalidateFail: commit: %w", err)
		}
		return false, nil
	}

	var fails int
	if err := tx.QueryRowContext(ctx, `
		SELECT revalidate_fails FROM turncache.turn_cache
		WHERE app = $1 AND app_hash = $2 AND state_path = $3 AND signature = $4`,
		k.App, k.AppHash, k.StatePath, k.Signature,
	).Scan(&fails); err != nil {
		return false, fmt.Errorf("turncache.RecordRevalidateFail: read back: %w", err)
	}
	evicted := false
	if fails >= c.cfg.RevalidateStrikes {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM turncache.turn_cache
			WHERE app = $1 AND app_hash = $2 AND state_path = $3 AND signature = $4`,
			k.App, k.AppHash, k.StatePath, k.Signature,
		); err != nil {
			return false, fmt.Errorf("turncache.RecordRevalidateFail: delete: %w", err)
		}
		evicted = true
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("turncache.RecordRevalidateFail: commit: %w", err)
	}
	_ = at // at is part of the interface signature but the in-memory cache also ignores it for strike-only updates.
	return evicted, nil
}

func (c *pgCache) InvalidateOtherHashes(ctx context.Context, app string, keepHash string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	res, err := c.db.ExecContext(ctx, `
		DELETE FROM turncache.turn_cache
		WHERE app = $1 AND app_hash != $2`,
		app, keepHash,
	)
	if err != nil {
		return 0, fmt.Errorf("turncache.InvalidateOtherHashes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("turncache.InvalidateOtherHashes: rows affected: %w", err)
	}
	// Same M1 cleanup as the SQLite backend: synonym_hits is app-less,
	// so any non-keep app_hash is stale by definition. Reported count
	// stays the turn_cache row count.
	if _, err := c.db.ExecContext(ctx, `
		DELETE FROM turncache.synonym_hits
		WHERE app_hash != $1`,
		keepHash,
	); err != nil {
		return int(n), fmt.Errorf("turncache.InvalidateOtherHashes: synonym_hits: %w", err)
	}
	return int(n), nil
}

func (c *pgCache) SweepCold(ctx context.Context, app string, olderThan time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Same sweep policy as sqliteCache, including the explicit IS NULL
	// arm: Postgres evaluates "NULL < <int>" as UNKNOWN (row excluded)
	// exactly like SQLite, so never-hit legacy rows need the explicit
	// branch to count as "before any cutoff".
	cutoff := olderThan.UnixMilli()
	var (
		decayActive bool
		decayCutoff int64
	)
	if c.cfg.ConfidenceDecay && c.cfg.MaxAge > 0 {
		decayActive = true
		decayCutoff = olderThan.Add(c.cfg.MaxAge / 2).UnixMilli()
	}

	var (
		query string
		args  []any
	)
	if decayActive {
		query = `
			DELETE FROM turncache.turn_cache
			WHERE app = $1
			  AND (
			        last_hit_at IS NULL
			     OR (confidence >= 0.7 AND last_hit_at < $2)
			     OR (confidence <  0.7 AND last_hit_at < $3)
			  )`
		args = []any{app, cutoff, decayCutoff}
	} else {
		query = `
			DELETE FROM turncache.turn_cache
			WHERE app = $1
			  AND (last_hit_at IS NULL OR last_hit_at < $2)`
		args = []any{app, cutoff}
	}
	res, err := c.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("turncache.SweepCold: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("turncache.SweepCold: rows affected: %w", err)
	}
	return int(n), nil
}

func (c *pgCache) TrimLRU(ctx context.Context, app string, capRows int, trimFraction float64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if capRows <= 0 || trimFraction <= 0 {
		return 0, nil
	}
	var total int
	if err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM turncache.turn_cache WHERE app = $1`, app,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("turncache.TrimLRU: count: %w", err)
	}
	if total <= capRows {
		return 0, nil
	}
	drop := int(float64(total) * trimFraction)
	if drop < 1 {
		drop = 1
	}
	if drop > total {
		drop = total
	}
	// ctid stands in for SQLite's rowid as the physical row handle for
	// the DELETE … IN (SELECT … LIMIT) pattern. NULLS FIRST is explicit:
	// Postgres defaults to NULLS LAST under ASC (the opposite of
	// SQLite), and never-hit rows must sort as the coldest.
	res, err := c.db.ExecContext(ctx, `
		DELETE FROM turncache.turn_cache
		WHERE ctid IN (
			SELECT ctid FROM turncache.turn_cache
			WHERE app = $1
			ORDER BY last_hit_at ASC NULLS FIRST
			LIMIT $2
		)`,
		app, drop,
	)
	if err != nil {
		return 0, fmt.Errorf("turncache.TrimLRU: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("turncache.TrimLRU: rows affected: %w", err)
	}
	return int(n), nil
}

func (c *pgCache) RecordSynonymHit(ctx context.Context, sk SynonymKey, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO turncache.synonym_hits (app_hash, intent, pattern, kind, hit_count, last_hit_at)
		VALUES ($1, $2, $3, $4, 1, $5)
		ON CONFLICT (app_hash, intent, pattern, kind) DO UPDATE SET
			hit_count = synonym_hits.hit_count + 1,
			last_hit_at = EXCLUDED.last_hit_at`,
		sk.AppHash, sk.Intent, sk.Pattern, sk.Kind, at.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("turncache.RecordSynonymHit: %w", err)
	}
	return nil
}

func (c *pgCache) SynonymStats(ctx context.Context, appHash string) ([]SynonymStat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT app_hash, intent, pattern, kind, hit_count, last_hit_at
		FROM turncache.synonym_hits
		WHERE app_hash = $1
		ORDER BY hit_count DESC, last_hit_at DESC`,
		appHash,
	)
	if err != nil {
		return nil, fmt.Errorf("turncache.SynonymStats: %w", err)
	}
	defer rows.Close()
	var out []SynonymStat
	for rows.Next() {
		var (
			s   SynonymStat
			lha sql.NullInt64
		)
		if err := rows.Scan(&s.AppHash, &s.Intent, &s.Pattern, &s.Kind, &s.HitCount, &lha); err != nil {
			return nil, fmt.Errorf("turncache.SynonymStats: scan: %w", err)
		}
		s.LastHitAt = fromNullableMillis(lha)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("turncache.SynonymStats: rows: %w", err)
	}
	return out, nil
}

// Close marks the cache closed. Idempotent. The injected *sql.DB is NOT
// closed — the caller that opened the handle owns its lifecycle.
func (c *pgCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
