// Fuzz tests for the SQLite backend. The corpus is the same as the
// in-memory fuzz; the invariants are the same too: no panics, Get
// after Put returns the row, RecordHit resets the strike count, N
// strikes evicts. The SQLite layer adds one extra failure mode the
// in-memory backend cannot have — schema or SQL errors. If a fuzz
// input pokes a hole in the SQL string encoding, the fuzzer surfaces
// it via the t.Fatalf path.
package turncache

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// FuzzSQLite seeds with a small interesting corpus and lets the fuzzer
// pick random Key + CachedVerdict fields. Each iteration gets its own
// SQLite file under a t.TempDir so iterations don't leak state.
func FuzzSQLite(f *testing.F) {
	seeds := []struct {
		app, hash, state, sig, intent, slots string
		conf                                 float64
	}{
		{"oregon-trail", "h1", "@start", "sig-a", "ford", `{}`, 0.9},
		{"oregon-trail", "h1", "@river_crossing.scouting", "sig-b", "propose_purchase", `{"items":[]}`, 0.5},
		{"x", "", "", "", "", "", 0},
		{"", "", "", "", "noop", "{}", 1.0},
		// Inputs with quote / backslash / unicode that any SQL string
		// encoder must round-trip correctly.
		{"a", "h", "@s", "sig'); DROP TABLE turn_cache;--", "x", `{"q":"\"o'r\""}`, 0.42},
		{"é", "☃", "@s", "sig-utf8", "go", `{"k":"é"}`, 0.7},
	}
	for _, s := range seeds {
		f.Add(s.app, s.hash, s.state, s.sig, s.intent, s.slots, s.conf)
	}

	cfg := testConfig()
	cfg.RevalidateStrikes = 3

	f.Fuzz(func(t *testing.T, app, hash, state, sig, intent, slotsJSON string, confidence float64) {
		path := filepath.Join(t.TempDir(), "cache.db")
		c, err := NewSQLite(path, cfg)
		if err != nil {
			t.Fatalf("NewSQLite: %v", err)
		}
		defer func() { _ = c.Close() }()

		ctx := context.Background()
		k := Key{App: app, AppHash: hash, StatePath: state, Signature: sig}
		v := CachedVerdict{Intent: intent, SlotsJSON: slotsJSON, Confidence: confidence}

		if err := c.Put(ctx, k, v); err != nil {
			t.Fatalf("Put: %v (key=%+v)", err, k)
		}
		got, ok, err := c.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !ok {
			t.Fatalf("Get after Put: want hit, got miss (key=%+v)", k)
		}
		if got.Intent != intent || got.SlotsJSON != slotsJSON {
			t.Fatalf("round-trip mismatch: want intent=%q slots=%q got intent=%q slots=%q",
				intent, slotsJSON, got.Intent, got.SlotsJSON)
		}

		// Hit resets strikes.
		now := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
		if _, err := c.RecordRevalidateFail(ctx, k, now); err != nil {
			t.Fatalf("RecordRevalidateFail: %v", err)
		}
		if err := c.RecordHit(ctx, k, now.Add(time.Hour)); err != nil {
			t.Fatalf("RecordHit: %v", err)
		}
		got, _, _ = c.Get(ctx, k)
		if got.RevalidateFails != 0 {
			t.Fatalf("RecordHit must reset RevalidateFails; got %d", got.RevalidateFails)
		}

		// N strikes evicts.
		for i := 0; i < cfg.RevalidateStrikes-1; i++ {
			if evicted, _ := c.RecordRevalidateFail(ctx, k, now); evicted {
				t.Fatalf("strike %d/%d: pre-emptive eviction", i+1, cfg.RevalidateStrikes)
			}
		}
		evicted, err := c.RecordRevalidateFail(ctx, k, now)
		if err != nil {
			t.Fatalf("final strike: %v", err)
		}
		if !evicted {
			t.Fatalf("after %d strikes: want evicted=true, got false", cfg.RevalidateStrikes)
		}
		if _, present, _ := c.Get(ctx, k); present {
			t.Fatalf("Get after eviction: want miss, got hit")
		}
	})
}
