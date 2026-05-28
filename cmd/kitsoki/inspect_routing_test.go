// Tests for the routing-inspect surfaces: --routing-stats,
// --unused-synonyms, --synonym-suggestions. Each seeds a SQLite
// turncache fixture with known rows then asserts the rendered text
// captures the right shape.
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/turncache"
)

const inspectAppYAML = `
app:
  id: inspect-routing-test
  version: 0.1.0

world: {}

routing:
  enabled: true

intents:
  greet:
    title: "Greet"
    examples: ["hello"]
    synonyms: ["hi", "hey"]
  farewell:
    title: "Farewell"
    examples: ["goodbye"]
    synonyms: ["bye"]

root: start

states:
  start:
    view: "greeting"
    on:
      greet:
        - target: ended
      farewell:
        - target: ended
  ended:
    terminal: true
    view: "done"
`

// buildFixtureCache seeds a SQLite cache fixture and returns the
// (appPath, cachePath, appHash) triple for use by each inspect test.
func buildFixtureCache(t *testing.T) (appPath, cachePath, appHash string) {
	t.Helper()
	dir := t.TempDir()
	appPath = filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(appPath, []byte(inspectAppYAML), 0644))
	def, err := app.Load(appPath)
	require.NoError(t, err)

	cachePath = filepath.Join(dir, "cache.sqlite")
	cache, err := turncache.NewSQLite(cachePath, turncache.DefaultConfig())
	require.NoError(t, err)

	appHash = orchestrator.ComputeAppHash(def)
	ctx := context.Background()
	now := time.Now()

	// Seed cache rows: greet @ start, two signatures, one hot.
	require.NoError(t, cache.Put(ctx, turncache.Key{
		App: def.App.ID, AppHash: appHash, StatePath: "start", Signature: "sighi-long-suffix",
	}, turncache.CachedVerdict{
		Intent: "greet", SlotsJSON: "{}",
		Confidence: 0.9, SourceModel: "claude-haiku",
		CreatedAt: now.Add(-24 * time.Hour),
	}))
	for i := 0; i < 5; i++ {
		require.NoError(t, cache.RecordHit(ctx, turncache.Key{
			App: def.App.ID, AppHash: appHash, StatePath: "start", Signature: "sighi-long-suffix",
		}, now))
	}
	require.NoError(t, cache.Put(ctx, turncache.Key{
		App: def.App.ID, AppHash: appHash, StatePath: "start", Signature: "sigbye-long-suffix",
	}, turncache.CachedVerdict{
		Intent: "farewell", SlotsJSON: "{}",
		Confidence: 0.85,
		CreatedAt:  now.Add(-1 * time.Hour),
	}))

	// Seed synonym hits: "hi" gets two hits; "hey" and "bye" stay at 0.
	require.NoError(t, cache.RecordSynonymHit(ctx, turncache.SynonymKey{
		AppHash: appHash, Intent: "greet", Pattern: "hi", Kind: "bare",
	}, now))
	require.NoError(t, cache.RecordSynonymHit(ctx, turncache.SynonymKey{
		AppHash: appHash, Intent: "greet", Pattern: "hi", Kind: "bare",
	}, now))

	require.NoError(t, cache.Close())
	return appPath, cachePath, appHash
}

// TestInspect_RoutingStats_PopulatedFixture covers the
// --routing-stats surface end-to-end.
func TestInspect_RoutingStats_PopulatedFixture(t *testing.T) {
	t.Parallel()
	appPath, cachePath, _ := buildFixtureCache(t)

	cmd := inspectCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--routing-stats", "--cache-db", cachePath, appPath})
	require.NoError(t, cmd.Execute())

	out := stdout.String()
	require.Contains(t, out, "Routing stats for inspect-routing-test")
	require.Contains(t, out, "greet")
	require.Contains(t, out, "farewell")
	require.Contains(t, out, "Hottest cached signatures:")
	// The hot signature should show up first.
	require.Contains(t, out, "sighi-lo")
}

// TestInspect_UnusedSynonyms_SurfacesZeroHitPattern: after the seed
// only "hi" has recorded hits; "hey" and "bye" stay at zero — so
// both should appear in the output.
func TestInspect_UnusedSynonyms_SurfacesZeroHitPattern(t *testing.T) {
	t.Parallel()
	appPath, cachePath, _ := buildFixtureCache(t)

	cmd := inspectCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--unused-synonyms", "--cache-db", cachePath, appPath})
	require.NoError(t, cmd.Execute())

	out := stdout.String()
	// "hey" and "bye" are unused; "hi" is not.
	require.Contains(t, out, `"hey"`)
	require.Contains(t, out, `"bye"`)
	require.NotContains(t, out, `"hi"  # unused`)
}

// TestInspect_SynonymSuggestions_GroupsByIntent: the suggestions
// output groups cache rows by intent and emits a YAML stanza per
// (state, intent) pair. We can't assert exact phrasings because the
// signature-only fallback emits placeholder text, but we can check
// the YAML structure.
func TestInspect_SynonymSuggestions_GroupsByIntent(t *testing.T) {
	t.Parallel()
	appPath, cachePath, _ := buildFixtureCache(t)

	cmd := inspectCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--synonym-suggestions", "--cache-db", cachePath, appPath})
	require.NoError(t, cmd.Execute())

	out := stdout.String()
	require.Contains(t, out, "intents:")
	require.Contains(t, out, "greet:")
	require.Contains(t, out, "farewell:")
	require.Contains(t, out, "synonyms:")
	// Hit-count comment must appear so authors can judge the
	// candidate's strength.
	require.True(t, strings.Contains(out, "hits"), "missing hit-count comment in suggestions output:\n%s", out)
}
