package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// _ ensures *RingBuffer satisfies slog.Handler at compile time. The
// test file is enough to exercise this — a behavioural test would not
// catch a future signature drift.
var _ slog.Handler = (*RingBuffer)(nil)

// TestRingBuffer_AppendAndSnapshot emits three records and asserts the
// snapshot contains three JSON lines in order, each parseable as JSON
// with the expected msg field.
func TestRingBuffer_AppendAndSnapshot(t *testing.T) {
	rb := NewRingBuffer(10)
	logger := slog.New(rb)

	logger.Info("first", "k", 1)
	logger.Info("second", "k", 2)
	logger.Info("third", "k", 3)

	snap := rb.Snapshot()
	lines := splitLines(snap)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), snap)
	}

	wantMsgs := []string{"first", "second", "third"}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", i, err, line)
		}
		if got, _ := rec["msg"].(string); got != wantMsgs[i] {
			t.Errorf("line %d msg = %q, want %q", i, got, wantMsgs[i])
		}
	}
}

// TestRingBuffer_WrapsAtCap configures a cap of 3, emits 5 records,
// and verifies only the last 3 survive in order.
func TestRingBuffer_WrapsAtCap(t *testing.T) {
	rb := NewRingBuffer(3)
	logger := slog.New(rb)

	for i := 0; i < 5; i++ {
		logger.Info("evt", "i", i)
	}

	if got := rb.Len(); got != 3 {
		t.Fatalf("ring length = %d, want 3", got)
	}

	snap := rb.Snapshot()
	lines := splitLines(snap)
	if len(lines) != 3 {
		t.Fatalf("snapshot lines = %d, want 3:\n%s", len(lines), snap)
	}

	wantIs := []float64{2, 3, 4}
	for idx, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %d not JSON: %v", idx, err)
		}
		gotI, _ := rec["i"].(float64)
		if gotI != wantIs[idx] {
			t.Errorf("line %d i = %v, want %v", idx, gotI, wantIs[idx])
		}
	}
}

// TestRingBuffer_Concurrent fires 50 goroutines emitting 100 events
// each. The result must respect the cap and every recorded line must
// be valid JSON. Total emitted (5000) far exceeds the cap (256) so
// wrap-around is exercised under contention.
func TestRingBuffer_Concurrent(t *testing.T) {
	const goroutines = 50
	const perGoroutine = 100
	const cap = 256

	rb := NewRingBuffer(cap)
	logger := slog.New(rb)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				logger.Info("concurrent", "g", g, "i", i)
			}
		}(g)
	}
	wg.Wait()

	if got := rb.Len(); got > cap {
		t.Fatalf("ring length = %d > cap %d", got, cap)
	}

	snap := rb.Snapshot()
	lines := splitLines(snap)
	if len(lines) > cap {
		t.Fatalf("snapshot lines = %d > cap %d", len(lines), cap)
	}
	for idx, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %d not JSON: %v\n%s", idx, err, line)
		}
	}
}

// TestRingBuffer_HandlerInterface is a compile-time + runtime sanity
// check that *RingBuffer implements slog.Handler — the var _ above
// catches it at compile time; this test prevents the var line from
// being mistakenly deleted in a future refactor.
func TestRingBuffer_HandlerInterface(t *testing.T) {
	var _ slog.Handler = (*RingBuffer)(nil)
	var _ slog.Handler = NewRingBuffer(1)
}

// TestRingBuffer_WithAttrsSharesBuffer ensures WithAttrs / WithGroup
// clones land their events in the same underlying ring.
func TestRingBuffer_WithAttrsSharesBuffer(t *testing.T) {
	rb := NewRingBuffer(10)
	root := slog.New(rb)
	child := root.With("component", "child")

	root.Info("from-root")
	child.Info("from-child")

	if got := rb.Len(); got != 2 {
		t.Fatalf("ring length = %d, want 2 (events emitted via root + child)", got)
	}

	snap := string(rb.Snapshot())
	if !strings.Contains(snap, `"msg":"from-root"`) {
		t.Errorf("snapshot missing root event:\n%s", snap)
	}
	if !strings.Contains(snap, `"msg":"from-child"`) {
		t.Errorf("snapshot missing child event:\n%s", snap)
	}
	if !strings.Contains(snap, `"component":"child"`) {
		t.Errorf("snapshot missing With() attr on child event:\n%s", snap)
	}
}

// TestRingBuffer_EnabledAtAllLevels confirms the ring catches every
// level — it's the "everything in case we need it" sink.
func TestRingBuffer_EnabledAtAllLevels(t *testing.T) {
	rb := NewRingBuffer(4)
	ctx := context.Background()
	levels := []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}
	for _, l := range levels {
		if !rb.Enabled(ctx, l) {
			t.Errorf("RingBuffer.Enabled(%v) = false, want true", l)
		}
	}
}

// splitLines breaks a JSONL blob into individual line slices, dropping
// the trailing empty entry produced by the final '\n'.
func splitLines(b []byte) [][]byte {
	parts := bytes.Split(b, []byte("\n"))
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}
