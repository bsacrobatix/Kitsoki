package mining

import (
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/store"
)

// TestPGWatermark_GetSet covers the WatermarkStore contract on the pg table:
// unmined ⇒ (0, false); Set advances and marks mined; re-Set with the same
// value is idempotent; Set(0) still records presence (mined == row presence,
// not non-zero value — the MapWatermarkStore seed-trigger semantics).
func TestPGWatermark_GetSet(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	ws, err := NewPGWatermarkStore(db, SessionMinerConsumer, nil)
	if err != nil {
		t.Fatalf("NewPGWatermarkStore: %v", err)
	}

	if got, mined := ws.Get("slug-a"); got != 0 || mined {
		t.Fatalf("unmined Get = (%d, %v); want (0, false)", got, mined)
	}
	if err := ws.Set("slug-a", 1700000000); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, mined := ws.Get("slug-a"); got != 1700000000 || !mined {
		t.Fatalf("Get = (%d, %v); want (1700000000, true)", got, mined)
	}
	// Idempotent re-set of the same value.
	if err := ws.Set("slug-a", 1700000000); err != nil {
		t.Fatalf("idempotent Set: %v", err)
	}
	if got, mined := ws.Get("slug-a"); got != 1700000000 || !mined {
		t.Fatalf("after idempotent Set: Get = (%d, %v); want (1700000000, true)", got, mined)
	}
	// Advance.
	if err := ws.Set("slug-a", 1700000500); err != nil {
		t.Fatalf("advance Set: %v", err)
	}
	if got, _ := ws.Get("slug-a"); got != 1700000500 {
		t.Fatalf("after advance: Get = %d; want 1700000500", got)
	}
	// Zero-mtime pass still records presence.
	if err := ws.Set("slug-empty", 0); err != nil {
		t.Fatalf("Set zero: %v", err)
	}
	if got, mined := ws.Get("slug-empty"); got != 0 || !mined {
		t.Fatalf("zero-mtime slug: Get = (%d, %v); want (0, true)", got, mined)
	}

	// Durability: a fresh store over the same database sees the ledger.
	re, err := NewPGWatermarkStore(db, SessionMinerConsumer, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, mined := re.Get("slug-a"); got != 1700000500 || !mined {
		t.Fatalf("reopened Get = (%d, %v); want (1700000500, true)", got, mined)
	}
	if _, mined := re.Get("slug-empty"); !mined {
		t.Fatal("reopened zero-mtime slug lost its mined mark")
	}
}

// TestPGWatermark_MultiConsumerIsolation proves rows are keyed per consumer:
// two consumers over the same database never see each other's offsets.
func TestPGWatermark_MultiConsumerIsolation(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	a, err := NewPGWatermarkStore(db, "consumer-a", nil)
	if err != nil {
		t.Fatalf("consumer-a: %v", err)
	}
	b, err := NewPGWatermarkStore(db, "consumer-b", nil)
	if err != nil {
		t.Fatalf("consumer-b: %v", err)
	}

	if err := a.Set("shared-slug", 100); err != nil {
		t.Fatalf("a.Set: %v", err)
	}
	if err := b.Set("shared-slug", 200); err != nil {
		t.Fatalf("b.Set: %v", err)
	}
	if got, _ := a.Get("shared-slug"); got != 100 {
		t.Fatalf("consumer-a sees %d; want 100", got)
	}
	if got, _ := b.Get("shared-slug"); got != 200 {
		t.Fatalf("consumer-b sees %d; want 200", got)
	}
	if _, mined := b.Get("a-only"); mined {
		t.Fatal("consumer-b sees a slug it never mined")
	}
	// Isolation survives rehydration too.
	ra, err := NewPGWatermarkStore(db, "consumer-a", nil)
	if err != nil {
		t.Fatalf("reopen consumer-a: %v", err)
	}
	if got, _ := ra.Get("shared-slug"); got != 100 {
		t.Fatalf("reopened consumer-a sees %d; want 100", got)
	}
}

// TestPGWatermark_SeedFromConfig covers the MinedThrough hand-off: the seed
// populates slugs the table has never seen, and a durable row always beats a
// (stale) config seed on reconstruction.
func TestPGWatermark_SeedFromConfig(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	seed := map[string]int64{"seeded-slug": 1600000000, "other-slug": 5}
	ws, err := NewPGWatermarkStore(db, SessionMinerConsumer, seed)
	if err != nil {
		t.Fatalf("NewPGWatermarkStore: %v", err)
	}
	if got, mined := ws.Get("seeded-slug"); got != 1600000000 || !mined {
		t.Fatalf("seeded Get = (%d, %v); want (1600000000, true)", got, mined)
	}
	if got, mined := ws.Get("other-slug"); got != 5 || !mined {
		t.Fatalf("seeded Get = (%d, %v); want (5, true)", got, mined)
	}

	// Advance one slug, then reconstruct with the same (now stale) seed: the
	// durable row must win, the never-mined slug still takes the seed.
	if err := ws.Set("seeded-slug", 1600000999); err != nil {
		t.Fatalf("Set: %v", err)
	}
	re, err := NewPGWatermarkStore(db, SessionMinerConsumer, seed)
	if err != nil {
		t.Fatalf("reopen with seed: %v", err)
	}
	if got, _ := re.Get("seeded-slug"); got != 1600000999 {
		t.Fatalf("durable row lost to stale seed: got %d; want 1600000999", got)
	}
	if got, _ := re.Get("other-slug"); got != 5 {
		t.Fatalf("reseeded slug = %d; want 5", got)
	}
}

// TestPGWatermark_StreamCursor covers the future-stream column: cursors
// advance independently of the mtime watermark and survive rehydration.
func TestPGWatermark_StreamCursor(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	ws, err := NewPGWatermarkStore(db, SessionMinerConsumer, nil)
	if err != nil {
		t.Fatalf("NewPGWatermarkStore: %v", err)
	}
	if cur, seen := ws.Cursor("slug"); cur != 0 || seen {
		t.Fatalf("fresh Cursor = (%d, %v); want (0, false)", cur, seen)
	}
	if err := ws.SetCursor("slug", store.StreamCursor(42)); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	if cur, seen := ws.Cursor("slug"); cur != 42 || !seen {
		t.Fatalf("Cursor = (%d, %v); want (42, true)", cur, seen)
	}
	// The cursor write must not disturb the mtime watermark and vice versa.
	if err := ws.Set("slug", 777); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if cur, _ := ws.Cursor("slug"); cur != 42 {
		t.Fatalf("Set clobbered cursor: %d; want 42", cur)
	}
	if err := ws.SetCursor("slug", 43); err != nil {
		t.Fatalf("SetCursor advance: %v", err)
	}
	if got, _ := ws.Get("slug"); got != 777 {
		t.Fatalf("SetCursor clobbered mtime: %d; want 777", got)
	}
	re, err := NewPGWatermarkStore(db, SessionMinerConsumer, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if cur, seen := re.Cursor("slug"); cur != 43 || !seen {
		t.Fatalf("reopened Cursor = (%d, %v); want (43, true)", cur, seen)
	}
	if got, _ := re.Get("slug"); got != 777 {
		t.Fatalf("reopened Get = %d; want 777", got)
	}
}

// The pg store must satisfy the miner's seam exactly (no adapter).
var _ WatermarkStore = (*PGWatermarkStore)(nil)
