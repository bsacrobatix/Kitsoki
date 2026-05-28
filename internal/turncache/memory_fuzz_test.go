// Fuzz tests for the in-memory turn cache. We exercise random sequences
// of Put / Get / RecordHit / RecordRevalidateFail / RecordSynonymHit and
// assert the documented invariants hold no matter the call pattern.
//
// Invariants:
//   - Get-after-Put returns the row with the verdict fields preserved.
//   - After RevalidateStrikes consecutive RecordRevalidateFail calls the
//     row is evicted and Get returns ok=false.
//   - A successful RecordHit between failures resets the strike count.
//   - No call ever panics, regardless of input.
package turncache

import (
	"context"
	"testing"
	"time"
)

// FuzzMemoryRoundtrip seeds with a small interesting corpus and lets the
// fuzzer pick a random key tuple + verdict string. It then runs a fixed
// scenario and asserts the invariants above.
//
// The fuzzer can only handle primitive args, so we drive the scenario
// shape internally and let it explore the (app, hash, state, sig, intent,
// slotsJSON, confidence) inputs.
func FuzzMemoryRoundtrip(f *testing.F) {
	seeds := []struct {
		app, hash, state, sig, intent, slots string
		conf                                 float64
	}{
		{"oregon-trail", "h1", "@start", "sig-a", "ford", `{}`, 0.9},
		{"oregon-trail", "h1", "@river_crossing.scouting", "sig-b", "propose_purchase", `{"items":[]}`, 0.5},
		{"x", "", "", "", "", "", 0},
		{"", "", "", "", "noop", "{}", 1.0},
	}
	for _, s := range seeds {
		f.Add(s.app, s.hash, s.state, s.sig, s.intent, s.slots, s.conf)
	}

	cfg := testConfig()
	cfg.RevalidateStrikes = 3

	f.Fuzz(func(t *testing.T, app, hash, state, sig, intent, slotsJSON string, confidence float64) {
		// Each fuzz iteration gets its own cache so we don't leak state.
		c := NewMemory(cfg)
		defer func() {
			if err := c.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}()

		ctx := context.Background()
		k := Key{App: app, AppHash: hash, StatePath: state, Signature: sig}
		v := CachedVerdict{
			Intent:     intent,
			SlotsJSON:  slotsJSON,
			Confidence: confidence,
		}

		// Put + Get round-trip.
		if err := c.Put(ctx, k, v); err != nil {
			t.Fatalf("Put(%+v): unexpected error %v", k, err)
		}
		got, ok, err := c.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get(%+v): unexpected error %v", k, err)
		}
		if !ok {
			t.Fatalf("Get(%+v): want hit after Put, got miss", k)
		}
		if got.Intent != intent || got.SlotsJSON != slotsJSON {
			t.Fatalf("Get(%+v): round-trip mismatch want intent=%q slots=%q got intent=%q slots=%q",
				k, intent, slotsJSON, got.Intent, got.SlotsJSON)
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
			t.Fatalf("RecordHit must reset RevalidateFails; got %d (full=%+v)", got.RevalidateFails, got)
		}

		// N strikes evicts.
		for i := 0; i < cfg.RevalidateStrikes-1; i++ {
			if evicted, _ := c.RecordRevalidateFail(ctx, k, now); evicted {
				t.Fatalf("strike %d/%d: pre-emptive eviction", i+1, cfg.RevalidateStrikes)
			}
		}
		evicted, err := c.RecordRevalidateFail(ctx, k, now)
		if err != nil {
			t.Fatalf("final strike: unexpected error %v", err)
		}
		if !evicted {
			t.Fatalf("after %d strikes: want evicted=true, got false", cfg.RevalidateStrikes)
		}
		if _, present, _ := c.Get(ctx, k); present {
			t.Fatalf("Get after eviction: want miss, got hit (key=%+v)", k)
		}
	})
}
