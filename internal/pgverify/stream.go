package pgverify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

const streamAppIDPrefix = MarkerPrefix + "stream-"

// CheckStreamMonotonicity drives opts.concurrencyWriters() goroutines, each
// appending opts.concurrencyAppendsPerWriter() batches of
// opts.concurrencyEventsPerAppend() events to its own namespaced session
// concurrently, then tails EVERY one of those sessions' streams the same way
// a real durable consumer would (store.EventStream.ReadStream, advancing a
// per-session cursor), and asserts:
//
//   - every event this check wrote is observed exactly once (no duplicate,
//     no missing row) when tailed with a monotonically advancing cursor —
//     the property a real stream consumer depends on;
//   - stream_pos is strictly increasing within each session's tail;
//   - across the union of all sessions' stream_pos values, there are no
//     duplicates (stream_pos is supposed to be a single global sequence).
//
// When opts.AssumeExclusiveWindow is set, it additionally asserts GLOBAL
// contiguity: querying the whole events table for stream_pos values inside
// [min, max] of what this check wrote must return exactly
// (max-min+1) rows with none missing — which only holds if nothing else
// wrote to the table during the run. That is an operator-asserted
// precondition (a maintenance window), not something this package can
// verify on its own, hence the option rather than a default.
func CheckStreamMonotonicity(ctx context.Context, svc store.Store, pgDB *sql.DB, opts Options) CheckResult {
	const id = "stream_pos_monotonicity"
	const name = "stream_pos has no gap and no duplicate under concurrent appends"

	if svc == nil {
		return notEstablished(id, name, "no service store configured (Options.OpenServiceStore)", nil)
	}
	stream, ok := store.AsEventStream(svc)
	if !ok {
		return notEstablished(id, name, "the configured store.Store does not implement store.EventStream (not a Postgres-backed store?)", nil)
	}

	writers := opts.concurrencyWriters()
	appendsPerWriter := opts.concurrencyAppendsPerWriter()
	eventsPerAppend := opts.concurrencyEventsPerAppend()
	totalPerSession := appendsPerWriter * eventsPerAppend

	sids := make([]app.SessionID, writers)
	for i := 0; i < writers; i++ {
		def := &app.AppDef{App: app.AppMeta{ID: fmt.Sprintf("%s%d", streamAppIDPrefix, i), Version: "pgverify-1"}}
		sid, err := svc.CreateSession(ctx, def)
		if err != nil {
			return notEstablished(id, name, fmt.Sprintf("CreateSession writer %d: %v", i, err), nil)
		}
		sids[i] = sid
	}
	cleanup := func() {
		for _, sid := range sids {
			_ = svc.DeleteSession(context.Background(), sid)
		}
	}
	if !opts.KeepTestData {
		defer cleanup()
	}

	var wg sync.WaitGroup
	writeErrs := make([]error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for a := 0; a < appendsPerWriter; a++ {
				evs := make([]store.Event, eventsPerAppend)
				for i := range evs {
					payload, _ := json.Marshal(map[string]any{"writer": w, "append": a, "i": i})
					evs[i] = store.Event{Turn: app.TurnNumber(a + 1), Kind: store.TransitionApplied, Payload: payload}
				}
				if err := svc.AppendEvents(sids[w], evs); err != nil {
					writeErrs[w] = fmt.Errorf("writer %d append %d: %w", w, a, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	for _, err := range writeErrs {
		if err != nil {
			return fail(id, name, fmt.Sprintf("a concurrent writer failed: %v", err), nil)
		}
	}

	var (
		allPos               []int64
		perWriter            = make([]int, writers)
		globalMin, globalMax int64
		first                = true
	)
	for w := 0; w < writers; w++ {
		var (
			cursor  store.StreamCursor
			seen    int
			lastPos int64 = -1
		)
		for {
			if err := ctx.Err(); err != nil {
				return fail(id, name, fmt.Sprintf("context cancelled while tailing writer %d: %v", w, err), nil)
			}
			page, err := stream.ReadSessionStream(ctx, sids[w], cursor, 0)
			if err != nil {
				return fail(id, name, fmt.Sprintf("ReadSessionStream writer %d: %v", w, err), nil)
			}
			if len(page) == 0 {
				break
			}
			for _, entry := range page {
				if int64(entry.Pos) <= lastPos {
					return fail(id, name, fmt.Sprintf("writer %d: stream_pos %d did not strictly increase after %d — duplicate or out-of-order delivery", w, entry.Pos, lastPos), nil)
				}
				lastPos = int64(entry.Pos)
				allPos = append(allPos, int64(entry.Pos))
				if first {
					globalMin, globalMax = int64(entry.Pos), int64(entry.Pos)
					first = false
				} else {
					if int64(entry.Pos) < globalMin {
						globalMin = int64(entry.Pos)
					}
					if int64(entry.Pos) > globalMax {
						globalMax = int64(entry.Pos)
					}
				}
				seen++
			}
			cursor = store.StreamCursor(page[len(page)-1].Pos)
		}
		perWriter[w] = seen
		if seen != totalPerSession {
			return fail(id, name, fmt.Sprintf("writer %d: expected %d events tailed back, got %d", w, totalPerSession, seen), map[string]any{"per_writer_counts": perWriter})
		}
	}

	// Global no-duplicate check across the union of all sessions' positions.
	sort.Slice(allPos, func(i, j int) bool { return allPos[i] < allPos[j] })
	for i := 1; i < len(allPos); i++ {
		if allPos[i] == allPos[i-1] {
			return fail(id, name, fmt.Sprintf("duplicate global stream_pos %d observed across two different sessions' tails", allPos[i]), nil)
		}
	}

	evidence := map[string]any{
		"writers":            writers,
		"appends_per_writer": appendsPerWriter,
		"events_per_append":  eventsPerAppend,
		"total_events":       len(allPos),
		"per_writer_counts":  perWriter,
		"global_min_pos":     globalMin,
		"global_max_pos":     globalMax,
	}

	if opts.AssumeExclusiveWindow {
		if pgDB == nil {
			return notEstablished(id, name, "AssumeExclusiveWindow set but no direct Postgres handle configured to verify global contiguity", evidence)
		}
		var rawCount int64
		if err := pgDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE stream_pos BETWEEN $1 AND $2`, globalMin, globalMax).Scan(&rawCount); err != nil {
			return notEstablished(id, name, fmt.Sprintf("global contiguity COUNT query: %v", err), evidence)
		}
		expected := globalMax - globalMin + 1
		evidence["global_exclusive_window_expected"] = expected
		evidence["global_exclusive_window_actual"] = rawCount
		if rawCount != expected {
			return fail(id, name, fmt.Sprintf("AssumeExclusiveWindow set but the [%d,%d] stream_pos range has %d rows, expected exactly %d — either another writer was active, or a genuine gap exists", globalMin, globalMax, rawCount, expected), evidence)
		}
	}

	return pass(id, name,
		fmt.Sprintf("%d writers x %d appends x %d events tailed back with no gap and no duplicate across %d sessions", writers, appendsPerWriter, eventsPerAppend, writers),
		evidence)
}
