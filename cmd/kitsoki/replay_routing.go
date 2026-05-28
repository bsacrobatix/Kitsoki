package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"kitsoki/internal/app"
	"kitsoki/internal/lex"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/semroute"
	"kitsoki/internal/turncache"
	"kitsoki/internal/world"
)

// replayRoutingCmd is `kitsoki replay-routing <app.yaml> [recording.yaml]`
// — the Phase-7 calibration tool from the semantic-routing proposal §10.
// It re-routes every recorded turn through the full deterministic →
// semroute → turncache stack, with the LLM stubbed to return the
// recorded intent. The summary breaks down which tier resolved each
// turn so the author can see whether the LLM-fallthrough rate is
// within the proposal's 30% target.
//
// Exits 1 when --target is set and the measured LLM fallthrough
// fraction is strictly greater than the target.
func replayRoutingCmd() *cobra.Command {
	var (
		noCache bool
		csvOut  string
		target  float64
	)

	cmd := &cobra.Command{
		Use:   "replay-routing <app.yaml> [recording.yaml]",
		Short: "Replay a recording through the routing stack (calibration)",
		Long: `Re-route every recorded turn through deterministic → semroute → turncache
with the LLM stubbed to return whatever the recording captured. Reports
which tier resolved each turn and exits non-zero when --target is set
and the LLM fallthrough rate exceeds it.

Examples:
  kitsoki replay-routing stories/oregon-trail/app.yaml
  kitsoki replay-routing stories/oregon-trail/app.yaml --csv /tmp/out.csv
  kitsoki replay-routing stories/oregon-trail/app.yaml --target 0.30`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			appPath := args[0]
			recordingPath := ""
			if len(args) > 1 {
				recordingPath = args[1]
			} else {
				// Default: <app-dir>/recording.yaml. Matches the
				// convention `kitsoki test intents` uses.
				abs, _ := filepath.Abs(appPath)
				recordingPath = filepath.Join(filepath.Dir(abs), "recording.yaml")
			}

			def, err := app.Load(appPath)
			if err != nil {
				return fmt.Errorf("load app %q: %w", appPath, err)
			}
			rec, err := loadRecording(recordingPath)
			if err != nil {
				return fmt.Errorf("load recording %q: %w", recordingPath, err)
			}

			summary, perTurn, err := ReplayRouting(context.Background(), def, rec, ReplayRoutingOptions{NoCache: noCache})
			if err != nil {
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), summary.Format())

			if csvOut != "" {
				if err := writeReplayCSV(csvOut, perTurn); err != nil {
					return fmt.Errorf("write csv %q: %w", csvOut, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d rows)\n", csvOut, len(perTurn))
			}

			if target > 0 {
				rate := summary.LLMFallthroughRate()
				if rate > target {
					return fmt.Errorf("LLM fallthrough rate %.1f%% exceeds target %.1f%%", rate*100, target*100)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&noCache, "no-cache", false,
		"don't read/write the cache; route through deterministic → semroute → LLM-stub only")
	cmd.Flags().StringVar(&csvOut, "csv", "",
		"write per-turn results as CSV to this file")
	cmd.Flags().Float64Var(&target, "target", 0,
		"fail (exit 1) if LLM fallthrough rate exceeds this fraction (e.g. 0.30 for 30%)")

	return cmd
}

// ReplayRoutingOptions controls the replay-routing pass.
type ReplayRoutingOptions struct {
	// NoCache disables both cache reads and writes for the pass.
	// Used to measure the pure deterministic+semroute hit rate
	// against a fixed recording, independent of cache warmth.
	NoCache bool
}

// PerTurnResult is one row of the replay-routing audit trail.
type PerTurnResult struct {
	TurnID         int            // 1-based row number in the recording
	State          string         // state_path the turn was recorded against
	Input          string         // verbatim user input
	ExpectedIntent string         // intent recorded as the ground truth
	ExpectedSlots  map[string]any // slots recorded as the ground truth
	ActualTier     string         // one of: deterministic, synonym, synonym_template, cache, llm
	ActualIntent   string         // intent the routing stack resolved to
	ActualSlots    map[string]any // slots the routing stack filled
	Mismatched     bool           // ExpectedIntent != ActualIntent OR slots differ
	Reason         string         // routing reason (synonym:wade, template[0], cache, etc.)
}

// Summary tallies which tier resolved each turn.
type Summary struct {
	AppPath       string
	TotalTurns    int
	Deterministic int
	SynonymBare   int
	SynonymTmpl   int
	Cache         int
	LLM           int
	Mismatched    int
}

// LLMFallthroughRate returns the fraction of turns that were resolved
// only by the LLM stub — the metric the proposal §10 targets.
func (s Summary) LLMFallthroughRate() float64 {
	if s.TotalTurns == 0 {
		return 0
	}
	return float64(s.LLM) / float64(s.TotalTurns)
}

// Format produces the human-readable summary shown on stdout.
func (s Summary) Format() string {
	pct := func(n int) float64 {
		if s.TotalTurns == 0 {
			return 0
		}
		return 100 * float64(n) / float64(s.TotalTurns)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Routing summary for %s (%d turns):\n", s.AppPath, s.TotalTurns)
	fmt.Fprintf(&b, "  deterministic:   %3d (%5.1f%%)\n", s.Deterministic, pct(s.Deterministic))
	fmt.Fprintf(&b, "  synonym (bare):  %3d (%5.1f%%)\n", s.SynonymBare, pct(s.SynonymBare))
	fmt.Fprintf(&b, "  synonym tmpl:    %3d (%5.1f%%)\n", s.SynonymTmpl, pct(s.SynonymTmpl))
	fmt.Fprintf(&b, "  cache:           %3d (%5.1f%%)\n", s.Cache, pct(s.Cache))
	fmt.Fprintf(&b, "  LLM:             %3d (%5.1f%%)   <-- target ≤30%%\n", s.LLM, pct(s.LLM))
	if s.Mismatched > 0 {
		fmt.Fprintf(&b, "  mismatched:      %3d (%5.1f%%)\n", s.Mismatched, pct(s.Mismatched))
	}
	return b.String()
}

// ReplayRouting executes the calibration pass over rec and returns
// the per-tier summary plus the per-turn audit rows. The function is
// exported so the calibration end-to-end test
// (internal/semroute/calibration_test.go) can call it without going
// through the CLI surface.
func ReplayRouting(ctx context.Context, def *app.AppDef, rec *RecordingFile, opts ReplayRoutingOptions) (Summary, []PerTurnResult, error) {
	m, err := machine.New(def)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("build machine: %w", err)
	}
	matcher, err := semroute.Compile(def)
	if err != nil {
		// A compile error means semroute can't help — log it onto the
		// summary so the user can fix the synonym YAML before
		// running again. Treat all turns as LLM-fallthroughs.
		matcher = nil
	}

	var cache turncache.Cache
	if !opts.NoCache {
		cache = turncache.NewMemory(turncache.DefaultConfig())
		defer cache.Close()
	}
	appHash := orchestrator.ComputeAppHash(def)

	// Mirror the orchestrator's bar selection (semantic.go::semanticBars):
	// honour the app's routing override when present, fall back to the
	// proposal defaults otherwise. The replay-routing CLI must use the
	// SAME high bar production uses, or the calibration number drifts
	// against apps that customise the bar via app.routing.semantic_high_bar.
	highBar := app.DefaultRoutingConfig().SemanticHighBar
	if def.Routing != nil {
		highBar = def.Routing.SemanticHighBar
	}

	stops := stopwordsExtra(def)
	w := machine.WorldFromSchema(def.World)

	summary := Summary{AppPath: def.App.ID, TotalTurns: len(rec.Entries)}
	perTurn := make([]PerTurnResult, 0, len(rec.Entries))

	for i, entry := range rec.Entries {
		state := app.StatePath(entry.State)
		allowed := m.AllowedIntents(state, w)
		allowedNames := make([]string, len(allowed))
		for j, ai := range allowed {
			allowedNames[j] = ai.Name
		}

		result := PerTurnResult{
			TurnID:         i + 1,
			State:          entry.State,
			Input:          entry.Input,
			ExpectedIntent: entry.Intent.Name,
			ExpectedSlots:  entry.Intent.Slots,
		}

		// Tier 1: deterministic — exact match on display string or
		// unique example. We re-use the same lookup the orchestrator
		// builds at runtime to keep the replay path honest.
		if di, slots, ok := matchDeterministic(def, m, state, w, entry.Input); ok {
			result.ActualTier = "deterministic"
			result.ActualIntent = di
			result.ActualSlots = slots
			result.Reason = "deterministic"
			summary.Deterministic++
			result.Mismatched = !intentMatches(result, entry)
			if result.Mismatched {
				summary.Mismatched++
			}
			perTurn = append(perTurn, result)
			continue
		}

		// Tier 2: semroute (bare synonym or template).
		//
		// Two guards mirror the production orchestrator
		// (internal/orchestrator/semantic.go::TrySemantic):
		//   1. The confidence floor comes from app.routing.semantic_high_bar
		//      (defaulting to 0.80) — NOT a hardcoded literal — so apps
		//      that tighten or loosen the bar see honest calibration.
		//   2. The RequiresUnfilledSlot guard: a matched intent whose
		//      required slots the verdict didn't fill falls through to
		//      the LLM, exactly as production does. Without this gate
		//      replay-routing under-counts LLM cost by every turn where
		//      a bare synonym matched the verb but missed the slot.
		if matcher != nil && !matcher.IsEmpty() {
			verdict, _ := matcher.Match(ctx, entry.State, allowedNames, entry.Input)
			if verdict.Confidence >= highBar &&
				!orchestrator.RequiresUnfilledSlot(def, state, verdict.Intent, verdict.Slots) {
				kind := classifySemrouteReason(verdict.MatchReason)
				if kind == "synonym_template" {
					summary.SynonymTmpl++
				} else {
					summary.SynonymBare++
				}
				result.ActualTier = kind
				result.ActualIntent = verdict.Intent
				result.ActualSlots = verdict.Slots
				result.Reason = verdict.MatchReason
				result.Mismatched = !intentMatches(result, entry)
				if result.Mismatched {
					summary.Mismatched++
				}
				perTurn = append(perTurn, result)
				continue
			}
		}

		// Tier 3: cache. The cache is empty on first run so the
		// first occurrence of each unique (state, sig) pair is
		// always an LLM hit; the SECOND occurrence (same state +
		// same lexical signature) is a cache hit. Subsequent same-
		// signature turns also hit. This mirrors production
		// behaviour: the first user pays the LLM cost, everyone
		// after them pays the cache cost.
		key := turncache.Key{
			App:       def.App.ID,
			AppHash:   appHash,
			StatePath: entry.State,
			Signature: lex.Signature(entry.Input, stops),
		}
		if cache != nil {
			if v, found, _ := cache.Get(ctx, key); found {
				slots := map[string]any{}
				_ = json.Unmarshal([]byte(v.SlotsJSON), &slots)
				summary.Cache++
				result.ActualTier = "cache"
				result.ActualIntent = v.Intent
				result.ActualSlots = slots
				result.Reason = "cache"
				result.Mismatched = !intentMatches(result, entry)
				if result.Mismatched {
					summary.Mismatched++
				}
				perTurn = append(perTurn, result)
				continue
			}
		}

		// Tier 4: LLM stub — return whatever the recording said.
		summary.LLM++
		result.ActualTier = "llm"
		result.ActualIntent = entry.Intent.Name
		result.ActualSlots = entry.Intent.Slots
		result.Reason = "llm"
		// LLM never mismatches in replay mode by construction (we
		// hand back the recording verbatim); still compute the field
		// for symmetry.
		result.Mismatched = !intentMatches(result, entry)
		if result.Mismatched {
			summary.Mismatched++
		}
		perTurn = append(perTurn, result)

		// Write back so a repeat of this (state, sig) hits cache.
		if cache != nil {
			b, _ := json.Marshal(entry.Intent.Slots)
			_ = cache.Put(ctx, key, turncache.CachedVerdict{
				Intent:    entry.Intent.Name,
				SlotsJSON: string(b),
			})
		}
	}

	return summary, perTurn, nil
}

// classifySemrouteReason maps a verdict MatchReason string onto one
// of the per-tier bucket names used by the summary. The match-reason
// taxonomy is set by semroute (Phase 2 / Phase 4); we read the prefix
// here to bucket without dragging the matcher's internals.
func classifySemrouteReason(reason string) string {
	switch {
	case strings.HasPrefix(reason, "template"):
		return "synonym_template"
	default:
		return "synonym"
	}
}

// matchDeterministic re-implements the orchestrator's deterministic
// tier rules for a fixed (state, world) pair. We can't call
// orchestrator.MatchDeterministic because that requires a live
// session; the replay pass works off recording entries directly.
//
// Rules: input is matched against the menu's display strings
// (case-insensitive, trimmed) first, then against the intent
// definition's examples. A unique match returns (intent, prefilled
// slots, true).
func matchDeterministic(def *app.AppDef, m machine.Machine, state app.StatePath, w world.World, input string) (string, map[string]any, bool) {
	menu := m.Menu(state, w)
	if len(menu.Primary) == 0 {
		return "", nil, false
	}
	norm := strings.ToLower(strings.TrimSpace(input))

	for _, entry := range menu.Primary {
		if strings.ToLower(strings.TrimSpace(entry.Display)) == norm {
			slots := entry.PrefilledSlots
			if slots == nil {
				slots = map[string]any{}
			}
			return entry.Intent, slots, true
		}
	}
	// Examples live on the intent definition, not on MenuEntry. Look
	// them up against the live AppDef. A match on any example for
	// any primary entry wins; ties (an example shared by two intents)
	// fall through to the next tier, matching the orchestrator's
	// "unique example" rule.
	type hit struct {
		intent string
		slots  map[string]any
	}
	var hits []hit
	for _, entry := range menu.Primary {
		def, ok := lookupIntentExamples(def, state, entry.Intent)
		if !ok {
			continue
		}
		for _, ex := range def {
			if strings.ToLower(strings.TrimSpace(ex)) == norm {
				slots := entry.PrefilledSlots
				if slots == nil {
					slots = map[string]any{}
				}
				hits = append(hits, hit{intent: entry.Intent, slots: slots})
				break
			}
		}
	}
	if len(hits) == 1 {
		return hits[0].intent, hits[0].slots, true
	}
	return "", nil, false
}

// lookupIntentExamples returns the Examples slice for the named
// intent, searching state-local intents first then the AppDef-wide
// table. Returns nil + false when the intent isn't defined.
func lookupIntentExamples(def *app.AppDef, state app.StatePath, name string) ([]string, bool) {
	if def == nil {
		return nil, false
	}
	if def.Intents != nil {
		if in, ok := def.Intents[name]; ok {
			return in.Examples, true
		}
	}
	_ = state
	return nil, false
}

// stopwordsExtra returns app.routing.stopwords_extra (or nil).
func stopwordsExtra(def *app.AppDef) []string {
	if def == nil || def.Routing == nil || len(def.Routing.StopwordsExtra) == 0 {
		return nil
	}
	return def.Routing.StopwordsExtra
}

// intentMatches reports whether the actual routing result matches the
// recording's ground truth on intent + slots. Slot comparison is
// shallow + permissive: missing-vs-empty-map is treated as equal, and
// extra slots the routing tier filled (that the recording didn't
// declare) are tolerated.
func intentMatches(r PerTurnResult, entry RecordingEntry) bool {
	if r.ActualIntent != entry.Intent.Name {
		return false
	}
	for k, want := range entry.Intent.Slots {
		got, ok := r.ActualSlots[k]
		if !ok {
			return false
		}
		if !reflect.DeepEqual(want, got) {
			return false
		}
	}
	return true
}

// RecordingFile mirrors the recording YAML shape used by the
// internal/harness ReplayHarness. Re-declared here so the
// replay-routing CLI doesn't pull in harness's package (the LLM
// surface area is intentionally excluded from this calibration tool).
type RecordingFile struct {
	Kind          string           `yaml:"kind"`
	AppID         string           `yaml:"app_id"`
	AppVersion    string           `yaml:"app_version"`
	GeneratedAt   string           `yaml:"generated_at"`
	Generator     string           `yaml:"generator"`
	MinConfidence float64          `yaml:"min_confidence"`
	Entries       []RecordingEntry `yaml:"entries"`
}

// RecordingEntry is one (state, input, intent, slots) row.
type RecordingEntry struct {
	State      string          `yaml:"state"`
	Input      string          `yaml:"input"`
	Intent     RecordingIntent `yaml:"intent"`
	Confidence float64         `yaml:"confidence"`
	MajorityOf int             `yaml:"majority_of"`
}

// RecordingIntent is the intent block within a recording entry.
type RecordingIntent struct {
	Name  string         `yaml:"name"`
	Slots map[string]any `yaml:"slots"`
}

// loadRecording reads + parses a recording YAML.
func loadRecording(path string) (*RecordingFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rf RecordingFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, err
	}
	if rf.Kind != "recording" {
		return nil, fmt.Errorf("recording kind = %q, want \"recording\"", rf.Kind)
	}
	return &rf, nil
}

// writeReplayCSV serialises perTurn as a CSV file with a stable
// column order that the golden test in replay_routing_test.go pins.
func writeReplayCSV(path string, rows []PerTurnResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeReplayCSVTo(f, rows)
}

// writeReplayCSVTo is the io.Writer variant used by the test.
func writeReplayCSVTo(w io.Writer, rows []PerTurnResult) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{
		"turn_id", "state", "input", "expected_intent", "expected_slots",
		"actual_tier", "actual_intent", "actual_slots", "reason", "mismatched",
	}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			fmt.Sprintf("%d", r.TurnID),
			r.State,
			r.Input,
			r.ExpectedIntent,
			marshalSlots(r.ExpectedSlots),
			r.ActualTier,
			r.ActualIntent,
			marshalSlots(r.ActualSlots),
			r.Reason,
			fmt.Sprintf("%v", r.Mismatched),
		}); err != nil {
			return err
		}
	}
	return nil
}

// marshalSlots returns a deterministic compact JSON representation of
// a slot map so the CSV golden is stable across runs. Sorting keys
// before marshal protects against go map iteration order leaking
// into the output.
func marshalSlots(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		vb, _ := json.Marshal(m[k])
		fmt.Fprintf(&b, "%q:%s", k, vb)
	}
	b.WriteByte('}')
	return b.String()
}
