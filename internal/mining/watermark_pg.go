package mining

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"kitsoki/internal/store"
)

// SessionMinerConsumer is the consumer key the ambient session miner records
// its offsets under in mining.consumer_offsets. Other stream consumers (future
// stream-driven mining, kitsokipattern tails) pick their own keys; rows are
// isolated per consumer.
const SessionMinerConsumer = "session-miner"

// PGWatermarkStore is the Postgres-backed WatermarkStore: a real consumer
// offset table (plan §1.2) replacing the .kitsoki.yaml mined_through sibling
// ledger when the session store is Postgres-backed. Rows live in the dedicated
// "mining" schema per the schema-per-subsystem convention, keyed
// (consumer, stream_key) so multiple consumers advance independently.
//
// Today's pipeline still keys by slug + transcript mtime, so the mtime offset
// column carries the existing WatermarkStore semantics unchanged (row presence
// == "mined", mirroring MapWatermarkStore's seen set). Each row additionally
// carries a store.StreamCursor column so a future stream-driven miner can
// advance a global events-table cursor through the same ledger without a
// schema change.
//
// Like the satellite stores, the full ledger for the consumer is hydrated into
// memory at construction: Get stays cheap and error-free (the interface has no
// error return), and Set writes through — the in-memory advance stands even if
// the upsert errors, matching MapWatermarkStore's Persist contract.
type PGWatermarkStore struct {
	db       *sql.DB
	consumer string

	mu   sync.Mutex
	m    map[string]int64
	cur  map[string]store.StreamCursor
	seen map[string]struct{}
}

// NewPGWatermarkStore applies the mining schema idempotently, seeds the
// consumer's ledger from the given map (slug → newest-mined mtime — the
// webconfig.MiningConfig.MinedThrough hand-off), and hydrates the in-memory
// ledger from the table. Seeding never overwrites a durable row: a slug the
// table has already seen keeps its stored offset, so the config seed only
// fires for slugs the database has never mined.
func NewPGWatermarkStore(db *sql.DB, consumer string, seed map[string]int64) (*PGWatermarkStore, error) {
	if db == nil {
		return nil, fmt.Errorf("mining: nil database")
	}
	if consumer == "" {
		return nil, fmt.Errorf("mining: empty consumer key")
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS mining;
CREATE TABLE IF NOT EXISTS mining.consumer_offsets (
	consumer      TEXT NOT NULL,
	stream_key    TEXT NOT NULL,
	mined_offset  BIGINT NOT NULL DEFAULT 0,
	stream_cursor BIGINT NOT NULL DEFAULT 0,
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (consumer, stream_key)
)`); err != nil {
		return nil, fmt.Errorf("mining schema: %w", err)
	}
	s := &PGWatermarkStore{
		db:       db,
		consumer: consumer,
		m:        make(map[string]int64),
		cur:      make(map[string]store.StreamCursor),
		seen:     make(map[string]struct{}),
	}
	for k, v := range seed {
		if _, err := db.ExecContext(ctx, `INSERT INTO mining.consumer_offsets (consumer, stream_key, mined_offset)
VALUES ($1, $2, $3) ON CONFLICT (consumer, stream_key) DO NOTHING`, consumer, k, v); err != nil {
			return nil, fmt.Errorf("mining seed %q: %w", k, err)
		}
	}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// load hydrates the in-memory ledger from the consumer's rows.
func (s *PGWatermarkStore) load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT stream_key, mined_offset, stream_cursor
FROM mining.consumer_offsets WHERE consumer = $1`, s.consumer)
	if err != nil {
		return fmt.Errorf("mining load: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var off, cursor int64
		if err := rows.Scan(&key, &off, &cursor); err != nil {
			return fmt.Errorf("mining load: %w", err)
		}
		s.m[key] = off
		s.cur[key] = cursor
		s.seen[key] = struct{}{}
	}
	return rows.Err()
}

// Get returns the slug's newest-mined mtime and whether the slug has ever been
// mined (a row exists). An unmined slug ⇒ the seed fires.
func (s *PGWatermarkStore) Get(slug string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, seen := s.seen[slug]
	return s.m[slug], seen
}

// Set advances the slug's watermark, marks it mined, and upserts the durable
// row. The in-memory advance stands even if the write errors (the error is
// returned, mirroring MapWatermarkStore's Persist contract).
func (s *PGWatermarkStore) Set(slug string, mtime int64) error {
	s.mu.Lock()
	s.m[slug] = mtime
	s.seen[slug] = struct{}{}
	s.mu.Unlock()
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO mining.consumer_offsets (consumer, stream_key, mined_offset)
VALUES ($1, $2, $3)
ON CONFLICT (consumer, stream_key) DO UPDATE SET mined_offset = EXCLUDED.mined_offset, updated_at = now()`,
		s.consumer, slug, mtime)
	if err != nil {
		return fmt.Errorf("mining set %q: %w", slug, err)
	}
	return nil
}

// Cursor returns the slug's persisted stream cursor and whether the slug has a
// row. Zero means "never advanced" for a present row (BIGINT default 0; the
// events table's stream_pos is a BIGSERIAL starting at 1).
func (s *PGWatermarkStore) Cursor(slug string) (store.StreamCursor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, seen := s.seen[slug]
	return s.cur[slug], seen
}

// SetCursor advances the slug's stream cursor (and marks the slug present)
// without touching the mtime watermark — the seam a future stream-driven
// mining pass advances after consuming events past that position.
func (s *PGWatermarkStore) SetCursor(slug string, cursor store.StreamCursor) error {
	s.mu.Lock()
	s.cur[slug] = cursor
	s.seen[slug] = struct{}{}
	s.mu.Unlock()
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO mining.consumer_offsets (consumer, stream_key, stream_cursor)
VALUES ($1, $2, $3)
ON CONFLICT (consumer, stream_key) DO UPDATE SET stream_cursor = EXCLUDED.stream_cursor, updated_at = now()`,
		s.consumer, slug, int64(cursor))
	if err != nil {
		return fmt.Errorf("mining set cursor %q: %w", slug, err)
	}
	return nil
}

// Snapshot returns a copy of the current mtime ledger for inspection, matching
// MapWatermarkStore.Snapshot.
func (s *PGWatermarkStore) Snapshot() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}
