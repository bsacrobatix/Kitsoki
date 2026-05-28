// Tests for the Phase-7 replay-routing CLI. We exercise the calibration
// logic against a small synthetic app (NOT Oregon Trail — the
// end-to-end calibration belongs in internal/semroute/calibration_test.go
// and runs once). These tests cover:
//
//   - The CLI's exit-code behaviour: 0 when --target is met, non-zero
//     when it's missed.
//   - The CSV golden shape: column headers + a row-per-turn layout
//     downstream tooling depends on.
//
// The synthetic app declares two intents — `north` and `south` — each
// with one example string the deterministic tier accepts. The
// recording fixture forces a 50/50 split between deterministic hits
// and LLM-fallthroughs.
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
)

// syntheticAppYAML is the minimal app the replay-routing tests run
// against. Two intents, one state allowing both. One synonym on each
// intent so the synonym tier has something to fire on.
const syntheticAppYAML = `
app:
  id: replay-routing-test
  version: 0.1.0

world: {}

routing:
  enabled: true

intents:
  north:
    title: "Go north"
    examples: ["north"]
    synonyms: ["head north"]
  south:
    title: "Go south"
    examples: ["south"]
    synonyms: ["head south"]

root: start

states:
  start:
    view: "compass rose"
    on:
      north:
        - target: ended
      south:
        - target: ended

  ended:
    terminal: true
    view: "done"
`

const syntheticRecordingYAML = `
kind: recording
app_id: replay-routing-test
app_version: 0.1.0
generated_at: "2026-01-01T00:00:00Z"
generator: "test fixture"
min_confidence: 1.00

entries:
  # Deterministic: input equals example.
  - state: start
    input: "north"
    intent: { name: north, slots: {} }
    confidence: 1.00
    majority_of: 1
  # Deterministic: same idea, opposite direction.
  - state: start
    input: "south"
    intent: { name: south, slots: {} }
    confidence: 1.00
    majority_of: 1
  # Synonym hit.
  - state: start
    input: "head north"
    intent: { name: north, slots: {} }
    confidence: 1.00
    majority_of: 1
  # LLM fallthrough — input matches neither example nor synonym.
  - state: start
    input: "go forth into the unknown wilderness"
    intent: { name: south, slots: {} }
    confidence: 1.00
    majority_of: 1
`

// writeFixtures sets up a temp directory with the synthetic app and
// recording. Returns (appPath, recordingPath).
func writeFixtures(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.yaml")
	recPath := filepath.Join(dir, "recording.yaml")
	require.NoError(t, os.WriteFile(appPath, []byte(syntheticAppYAML), 0644))
	require.NoError(t, os.WriteFile(recPath, []byte(syntheticRecordingYAML), 0644))
	return appPath, recPath
}

// TestReplayRouting_SummaryCounts pins the per-tier breakdown on the
// synthetic recording. 4 turns: 2 deterministic, 1 synonym, 1 LLM →
// 25% LLM fallthrough.
func TestReplayRouting_SummaryCounts(t *testing.T) {
	t.Parallel()
	appPath, recPath := writeFixtures(t)

	def, err := app.Load(appPath)
	require.NoError(t, err)
	rec, err := loadRecording(recPath)
	require.NoError(t, err)

	sum, rows, err := ReplayRouting(context.Background(), def, rec, ReplayRoutingOptions{})
	require.NoError(t, err)

	require.Equal(t, 4, sum.TotalTurns)
	require.Equal(t, 2, sum.Deterministic)
	require.Equal(t, 1, sum.SynonymBare)
	require.Equal(t, 0, sum.SynonymTmpl)
	require.Equal(t, 1, sum.LLM)
	require.InDelta(t, 0.25, sum.LLMFallthroughRate(), 0.001)
	require.Len(t, rows, 4)
	// Spot-check row 1 (head north → synonym).
	require.Equal(t, "synonym", rows[2].ActualTier)
	require.Equal(t, "north", rows[2].ActualIntent)
}

// TestReplayRouting_NoCache_TogglesCacheTier confirms the --no-cache
// flag bypasses the cache. We run the same recording twice in one
// pass with cache=on (second occurrence of an LLM-resolved (state,
// sig) hits the cache); with cache=off (no row written so identical
// inputs both go to the LLM).
func TestReplayRouting_NoCache_TogglesCacheTier(t *testing.T) {
	t.Parallel()
	appPath, _ := writeFixtures(t)
	def, err := app.Load(appPath)
	require.NoError(t, err)

	// Build a 2-row recording with duplicated free-form LLM inputs so
	// the cache has a second occurrence to short-circuit.
	rec := &RecordingFile{
		Kind: "recording",
		Entries: []RecordingEntry{
			{State: "start", Input: "blah blah unique phrase", Intent: RecordingIntent{Name: "north"}, Confidence: 1.0, MajorityOf: 1},
			{State: "start", Input: "blah blah unique phrase", Intent: RecordingIntent{Name: "north"}, Confidence: 1.0, MajorityOf: 1},
		},
	}

	// With cache on: first turn LLM, second turn cache.
	sumOn, _, err := ReplayRouting(context.Background(), def, rec, ReplayRoutingOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, sumOn.LLM, "first turn must go LLM")
	require.Equal(t, 1, sumOn.Cache, "second turn must hit cache")

	// With cache off: both turns LLM.
	sumOff, _, err := ReplayRouting(context.Background(), def, rec, ReplayRoutingOptions{NoCache: true})
	require.NoError(t, err)
	require.Equal(t, 2, sumOff.LLM, "no-cache: both turns must hit LLM")
	require.Equal(t, 0, sumOff.Cache)
}

// TestReplayRouting_CSV_GoldenShape pins the CSV column ordering and
// header row. Downstream tooling parses this exact shape — a change
// would break it silently otherwise.
func TestReplayRouting_CSV_GoldenShape(t *testing.T) {
	t.Parallel()
	rows := []PerTurnResult{
		{
			TurnID: 1, State: "start", Input: "north",
			ExpectedIntent: "north", ExpectedSlots: map[string]any{},
			ActualTier: "deterministic", ActualIntent: "north",
			ActualSlots: map[string]any{}, Reason: "deterministic",
		},
	}
	var buf bytes.Buffer
	require.NoError(t, writeReplayCSVTo(&buf, rows))
	out := buf.String()

	want := []string{
		"turn_id,state,input,expected_intent,expected_slots,actual_tier,actual_intent,actual_slots,reason,mismatched",
		"1,start,north,north,{},deterministic,north,{},deterministic,false",
	}
	for _, line := range want {
		if !strings.Contains(out, line) {
			t.Fatalf("CSV missing expected line %q\nfull output:\n%s", line, out)
		}
	}
}

// TestReplayRouting_TargetMet_ExitsZero exercises the CLI's exit-code
// contract via the cobra command surface. With target=0.5 and the
// synthetic recording's 25% LLM rate, RunE must return nil.
func TestReplayRouting_TargetMet_ExitsZero(t *testing.T) {
	t.Parallel()
	appPath, recPath := writeFixtures(t)

	cmd := replayRoutingCmd()
	cmd.SetArgs([]string{appPath, recPath, "--target", "0.5"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())
}

// TestReplayRouting_TargetMissed_ReturnsError pairs with the above:
// target=0.10 is below the 25% rate, so RunE must return a non-nil
// error (which the cobra wrapper translates to exit code 1).
func TestReplayRouting_TargetMissed_ReturnsError(t *testing.T) {
	t.Parallel()
	appPath, recPath := writeFixtures(t)

	cmd := replayRoutingCmd()
	cmd.SetArgs([]string{appPath, recPath, "--target", "0.10"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds target")
}
