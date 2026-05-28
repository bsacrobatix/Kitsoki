package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/app"
	"kitsoki/internal/testrunner"
)

func cassetteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cassette",
		Short: "Diff and lint host cassette files",
		Long: `cassette sub-commands:
  kitsoki cassette diff old.yaml new.yaml   — structural diff keyed by episode id
  kitsoki cassette lint cassette.yaml        — validate a cassette file`,
	}
	cmd.AddCommand(cassetteDiffCmd())
	cmd.AddCommand(cassetteLintCmd())
	return cmd
}

// cassetteDiffCmd implements `kitsoki cassette diff old.yaml new.yaml`.
func cassetteDiffCmd() *cobra.Command {
	var (
		verbose bool
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "diff <old.yaml> <new.yaml>",
		Short: "Structural diff of two cassettes keyed by episode id",
		Long: `Compare two cassette files by episode id.

Output lines:
  + added_id     — episode present in new, absent in old
  - removed_id   — episode present in old, absent in new
  ~ changed_id   — episode present in both but match: or response: differ
    matched_id   — unchanged (only printed with --verbose / -v)

Exit code 0 when no differences, 1 when any differ.
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			oldPath, newPath := args[0], args[1]

			oldCas, err := testrunner.LoadCassette(oldPath)
			if err != nil {
				return fmt.Errorf("load %q: %w", oldPath, err)
			}
			newCas, err := testrunner.LoadCassette(newPath)
			if err != nil {
				return fmt.Errorf("load %q: %w", newPath, err)
			}

			result := diffCassettes(oldCas, newCas)

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			for _, id := range result.Added {
				fmt.Fprintf(cmd.OutOrStdout(), "+ %s\n", id)
			}
			for _, id := range result.Removed {
				fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", id)
			}
			for _, ch := range result.Changed {
				fmt.Fprintf(cmd.OutOrStdout(), "~ %s\n", ch.ID)
			}
			if verbose {
				for _, id := range result.Matched {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", id)
				}
			}

			if len(result.Added) > 0 || len(result.Removed) > 0 || len(result.Changed) > 0 {
				return fmt.Errorf("cassettes differ")
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also print unchanged episode ids")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

// cassetteDiffResult is the structured result of a cassette diff.
type cassetteDiffResult struct {
	Added   []string              `json:"added"`
	Removed []string              `json:"removed"`
	Changed []cassetteDiffChanged `json:"changed"`
	Matched []string              `json:"matched,omitempty"`
}

type cassetteDiffChanged struct {
	ID           string         `json:"id"`
	MatchDiff    *fieldDiffPair `json:"match_diff,omitempty"`
	ResponseDiff *fieldDiffPair `json:"response_diff,omitempty"`
}

type fieldDiffPair struct {
	Old any `json:"old"`
	New any `json:"new"`
}

// diffCassettes compares two loaded cassettes and returns a structured diff.
func diffCassettes(old, new *testrunner.Cassette) cassetteDiffResult {
	oldByID := make(map[string]*testrunner.CassetteEpisode, len(old.Episodes))
	for i := range old.Episodes {
		ep := &old.Episodes[i]
		oldByID[ep.ID] = ep
	}
	newByID := make(map[string]*testrunner.CassetteEpisode, len(new.Episodes))
	for i := range new.Episodes {
		ep := &new.Episodes[i]
		newByID[ep.ID] = ep
	}

	result := cassetteDiffResult{
		Added:   []string{},
		Removed: []string{},
		Changed: []cassetteDiffChanged{},
		Matched: []string{},
	}

	// Episodes in new but not old → added.
	for _, ep := range new.Episodes {
		if _, exists := oldByID[ep.ID]; !exists {
			result.Added = append(result.Added, ep.ID)
		}
	}

	// Episodes in old but not new → removed.
	for _, ep := range old.Episodes {
		if _, exists := newByID[ep.ID]; !exists {
			result.Removed = append(result.Removed, ep.ID)
		}
	}

	// Episodes in both — check for changes.
	for _, newEp := range new.Episodes {
		oldEp, exists := oldByID[newEp.ID]
		if !exists {
			continue
		}
		var ch cassetteDiffChanged
		ch.ID = newEp.ID

		if !reflect.DeepEqual(oldEp.Match, newEp.Match) {
			ch.MatchDiff = &fieldDiffPair{Old: oldEp.Match, New: newEp.Match}
		}
		if !reflect.DeepEqual(oldEp.Response, newEp.Response) {
			ch.ResponseDiff = &fieldDiffPair{Old: oldEp.Response, New: newEp.Response}
		}

		if ch.MatchDiff != nil || ch.ResponseDiff != nil {
			result.Changed = append(result.Changed, ch)
		} else {
			result.Matched = append(result.Matched, newEp.ID)
		}
	}

	return result
}

// cassetteLintCmd implements `kitsoki cassette lint cassette.yaml [--against-app app.yaml]`.
func cassetteLintCmd() *cobra.Command {
	var (
		againstApp string
		strict     bool
	)

	cmd := &cobra.Command{
		Use:   "lint <cassette.yaml>",
		Short: "Validate a cassette file for duplicate ids, missing includes, and orphaned episodes",
		Long: `Lint a host cassette file.

Checks performed:
  - Duplicate episode id within the cassette (error).
  - Missing !include references — surfaced as a load error.
  - Orphaned episodes: when --against-app is provided, any episode whose
    match.handler is not dispatched anywhere in the app (error).

Exit codes:
  0  no errors (warnings alone are exit 0 unless --strict)
  1  one or more errors, or warnings with --strict
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cassPath := args[0]

			cas, loadErr := testrunner.LoadCassette(cassPath)
			if loadErr != nil {
				// Surface include errors and parse errors clearly.
				msg := loadErr.Error()
				if strings.Contains(msg, "!include") {
					// Extract a clean file name from the error for the lint format.
					fmt.Fprintf(cmd.OutOrStdout(), "ERROR: %s\n", msg)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "ERROR: %s\n", msg)
				}
				return fmt.Errorf("cassette lint failed")
			}

			var errors []string
			var warnings []string

			// Check for duplicate ids.
			seen := make(map[string]int, len(cas.Episodes))
			for _, ep := range cas.Episodes {
				seen[ep.ID]++
			}
			for id, count := range seen {
				if count > 1 {
					errors = append(errors, fmt.Sprintf("duplicate episode id %q (appears %d times)", id, count))
				}
			}

			// Check for orphaned episodes when --against-app is provided.
			if againstApp != "" {
				def, appErr := app.Load(againstApp)
				if appErr != nil {
					return fmt.Errorf("load app %q: %w", againstApp, appErr)
				}
				dispatched := collectDispatchedHandlers(def)
				for _, ep := range cas.Episodes {
					handler, _ := ep.Match["handler"].(string)
					if handler == "" {
						continue
					}
					if !dispatched[handler] {
						errors = append(errors, fmt.Sprintf("orphaned episode %q — handler %q not dispatched by app.yaml", ep.ID, handler))
					}
				}
			}

			hasErrors := len(errors) > 0
			hasWarnings := len(warnings) > 0

			for _, e := range errors {
				fmt.Fprintf(cmd.OutOrStdout(), "ERROR: %s\n", e)
			}
			for _, w := range warnings {
				fmt.Fprintf(cmd.OutOrStdout(), "WARN:  %s\n", w)
			}

			if hasErrors || (strict && hasWarnings) {
				return fmt.Errorf("cassette lint failed")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&againstApp, "against-app", "", "path to app.yaml; check for orphaned episode handlers")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors")
	return cmd
}

// collectDispatchedHandlers walks an AppDef and returns the set of all handler
// names referenced in Invoke fields across all states, transitions, and on_enter
// effect chains (recursively including on_complete chains).
func collectDispatchedHandlers(def *app.AppDef) map[string]bool {
	set := make(map[string]bool)
	for _, state := range def.States {
		collectFromState(state, set)
	}
	return set
}

func collectFromState(s *app.State, set map[string]bool) {
	if s == nil {
		return
	}
	collectFromEffects(s.OnEnter, set)
	for _, transitions := range s.On {
		for _, t := range transitions {
			collectFromEffects(t.Effects, set)
		}
	}
	for _, child := range s.States {
		collectFromState(child, set)
	}
}

func collectFromEffects(effects []app.Effect, set map[string]bool) {
	for _, e := range effects {
		if e.Invoke != "" {
			set[e.Invoke] = true
		}
		if len(e.OnComplete) > 0 {
			collectFromEffects(e.OnComplete, set)
		}
	}
}
