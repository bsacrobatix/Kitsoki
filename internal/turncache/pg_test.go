// Postgres-backend tests. The cross-backend conformance suite is the
// acceptance bar — every behavioural invariant the [Cache] interface
// promises must hold against Postgres exactly as it does against the
// in-memory reference and SQLite. Uses the embedded server via
// internal/dbruntime/pgtest; skips (never fails) when no Postgres can
// be started in this environment.
package turncache

import (
	"context"
	"testing"
	"time"

	"kitsoki/internal/dbruntime/pgtest"
)

// newPostgresCache opens a fresh per-test database and builds the
// Postgres cache over it. pgtest owns the database lifecycle (create on
// Open, drop in t.Cleanup); the cache's Close does not touch the handle.
func newPostgresCache(t *testing.T, cfg Config) Cache {
	t.Helper()
	db := pgtest.Open(t)
	c, err := NewPostgres(db, cfg)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestPostgresConformance runs the cross-backend conformance suite
// against the Postgres backend. A failure means a divergence from the
// in-memory reference. The suite itself lives in conformance_test.go.
func TestPostgresConformance(t *testing.T) {
	t.Parallel()
	runConformanceSuite(t, newPostgresCache)
}

// TestPostgres_SchemaIdempotent re-runs NewPostgres over an already
// migrated database — the CREATE … IF NOT EXISTS posture must make a
// second (and third) migration a no-op, matching NewSQLite.
func TestPostgres_SchemaIdempotent(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	for i := 0; i < 3; i++ {
		c, err := NewPostgres(db, testConformanceConfig())
		if err != nil {
			t.Fatalf("NewPostgres apply #%d: %v", i+1, err)
		}
		if err := c.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

// TestPostgres_PersistenceAcrossHandles writes through one cache
// instance and reads through a second built over the same database —
// the Postgres analogue of the SQLite cross-process persistence test.
func TestPostgres_PersistenceAcrossHandles(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	cfg := testConformanceConfig()
	c1, err := NewPostgres(db, cfg)
	if err != nil {
		t.Fatalf("first NewPostgres: %v", err)
	}
	k := keyFor("oregon-trail", "h1", "@start", "sig")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	v := CachedVerdict{
		Intent:       "ford",
		SlotsJSON:    `{"river":"green"}`,
		Confidence:   0.81,
		SourceModel:  "claude-haiku-4-5",
		SourceTurnID: "trn_persistence",
		HitCount:     7,
		LastHitAt:    now,
		CreatedAt:    now.Add(-time.Hour),
	}
	if err := c1.Put(context.Background(), k, v); err != nil {
		t.Fatalf("Put on first handle: %v", err)
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("close first handle: %v", err)
	}

	c2, err := NewPostgres(db, cfg)
	if err != nil {
		t.Fatalf("second NewPostgres: %v", err)
	}
	t.Cleanup(func() { _ = c2.Close() })
	got, ok, err := c2.Get(context.Background(), k)
	if err != nil || !ok {
		t.Fatalf("Get on second handle: ok=%v err=%v", ok, err)
	}
	if got.Intent != v.Intent || got.SlotsJSON != v.SlotsJSON || got.HitCount != v.HitCount {
		t.Errorf("row drifted across handles: got %+v want %+v", got, v)
	}
	if !got.LastHitAt.Equal(v.LastHitAt) || !got.CreatedAt.Equal(v.CreatedAt) {
		t.Errorf("timestamps drifted: got LastHitAt=%v CreatedAt=%v", got.LastHitAt, got.CreatedAt)
	}
}
