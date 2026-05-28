// Runnable godoc examples. The // Output: block is verified by
// `go test -run "^Example" ./internal/turncache/...`.
package turncache_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kitsoki/internal/turncache"
)

// ExampleNewMemory shows the canonical Put -> Get round-trip the
// orchestrator runs on every turn.
func ExampleNewMemory() {
	c := turncache.NewMemory(turncache.DefaultConfig())
	defer c.Close()

	ctx := context.Background()
	k := turncache.Key{
		App:       "oregon-trail",
		AppHash:   "h1",
		StatePath: "@river_crossing.scouting",
		Signature: "06ad...c1",
	}
	v := turncache.CachedVerdict{
		Intent:     "ford",
		SlotsJSON:  `{}`,
		Confidence: 0.92,
		CreatedAt:  time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := c.Put(ctx, k, v); err != nil {
		fmt.Println("put err:", err)
		return
	}

	got, ok, err := c.Get(ctx, k)
	if err != nil {
		fmt.Println("get err:", err)
		return
	}
	fmt.Println("ok:", ok)
	fmt.Println("intent:", got.Intent)
	fmt.Println("confidence:", got.Confidence)
	// Output:
	// ok: true
	// intent: ford
	// confidence: 0.92
}

// ExampleNewSQLite shows the canonical Open -> Put -> Get -> Close
// flow for the SQLite-backed cache. Cross-session reuse is the
// distinguishing feature: the row written below survives until the DB
// file is deleted, even across kitsoki restarts.
func ExampleNewSQLite() {
	dir, err := os.MkdirTemp("", "turncache-example-")
	if err != nil {
		fmt.Println("mktemp err:", err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "cache.db")

	c, err := turncache.NewSQLite(path, turncache.DefaultConfig())
	if err != nil {
		fmt.Println("open err:", err)
		return
	}
	defer c.Close()

	ctx := context.Background()
	k := turncache.Key{
		App:       "oregon-trail",
		AppHash:   "h1",
		StatePath: "@river_crossing.scouting",
		Signature: "06ad...c1",
	}
	v := turncache.CachedVerdict{
		Intent:     "ford",
		SlotsJSON:  `{}`,
		Confidence: 0.92,
		CreatedAt:  time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := c.Put(ctx, k, v); err != nil {
		fmt.Println("put err:", err)
		return
	}

	got, ok, err := c.Get(ctx, k)
	if err != nil {
		fmt.Println("get err:", err)
		return
	}
	fmt.Println("ok:", ok)
	fmt.Println("intent:", got.Intent)
	fmt.Println("confidence:", got.Confidence)
	// Output:
	// ok: true
	// intent: ford
	// confidence: 0.92
}
