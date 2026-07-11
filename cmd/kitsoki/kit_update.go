// `kitsoki kit update` / `kitsoki kit reject` — the S7 staging half of the
// kit lifecycle. update re-resolves a locked kit, gates the candidate on the
// recorded semver constraint, and STAGES it (kits.staged.lock +
// .kitsoki/kit-update/<name>/plan.yaml) without touching the accepted
// lockfile; reject removes the staged candidate residue-free. `kit trial`
// judges a staged candidate and `kit accept` promotes it (later slices).
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/kitgit"
	"kitsoki/internal/kitlock"
	"kitsoki/internal/kitstage"
)

func kitUpdateCmd() *cobra.Command {
	var (
		target    string
		to        string
		source    string
		checkOnly bool
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Re-resolve a locked kit and stage the result as an update candidate",
		Long: `Re-resolves a locked kit's source, checks the result against the version
constraint recorded at 'kit add --version' time (overridable with --to),
and stages the resolution as an update CANDIDATE:

  .kitsoki/kits.staged.lock            the pinned candidate (accepted
                                       kits.lock is not touched)
  .kitsoki/kit-update/<name>/plan.yaml the upgrade plan: from/to versions,
                                       changed files, and the candidate's
                                       compat.renamed hints annotated with
                                       the local references they affect

Nothing resolves against the candidate unless asked: pass --staged (any
command) to resolve staged kits, run 'kit trial <name>' to judge the
candidate, then 'kit accept <name>' to promote it into kits.lock or
'kit reject <name>' to drop it.

For a git-sourced kit, --to rewrites the source's @ref (tag/branch/commit);
for every other tier --to is a version constraint the candidate must
satisfy (bare versions mean exact match). --source replaces the source
string entirely, e.g. to migrate a kit from @kitsoki/<name> to a git
remote.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			targetAbs, err := absTarget(target)
			if err != nil {
				return err
			}
			lockPath := kitlock.Path(targetAbs)
			if !kitlock.Exists(lockPath) {
				return fmt.Errorf("no lockfile at %s — run `kitsoki kit add` first", lockPath)
			}
			lf, err := kitlock.Load(lockPath)
			if err != nil {
				return err
			}
			locked := lf.Kits[name]
			if locked == nil {
				return fmt.Errorf("kit %q is not locked in %s — run `kitsoki kit add` first", name, lockPath)
			}

			// Candidate source: --source replaces outright; --to rewrites a
			// git source's @ref, and is a version gate for every other tier.
			candSource := locked.Source
			if source != "" {
				candSource = source
			}
			gate := locked.Constraint
			if to != "" {
				if url, _, ok := kitgit.ParseSource(candSource); ok {
					candSource = "git+" + url + "@" + to
				} else {
					gate = to
				}
			}

			resolved, resolvedDir, err := resolveKitEntry(cmd.Context(), candSource, targetAbs, gate)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			candidate := &kitstage.Entry{
				Source:   candSource,
				Version:  resolved.Version,
				Commit:   resolved.Commit,
				TreeHash: resolved.TreeHash,
				From:     kitstage.SnapshotOfLock(locked),
				StagedAt: time.Now().UTC().Format(time.RFC3339),
			}

			if candidate.Snapshot().Equal(kitstage.SnapshotOfLock(locked)) {
				fmt.Fprintf(out, "%s is already up to date (%s, tree %s) — nothing staged\n",
					name, displayVersion(locked.Version), shortHash(locked.TreeHash))
				return nil
			}

			// Pin candidate bytes for non-git tiers: the resolved directory
			// is mutable (a checkout, the embedded materialization), so
			// snapshot it into the content-addressed tree cache. Git-tier
			// candidates were just materialized into the commit cache by
			// resolution itself.
			if candidate.Commit == "" {
				if _, _, err := kitgit.MaterializeTree(resolvedDir); err != nil {
					return fmt.Errorf("snapshot candidate tree: %w", err)
				}
			}

			plan := &kitstage.Plan{
				Schema:   kitstage.PlanSchema,
				Kit:      name,
				Source:   candSource,
				From:     candidate.From,
				To:       candidate.Snapshot(),
				StagedAt: candidate.StagedAt,
			}
			plan.ChangedFiles, plan.ChangedFilesNote = kitstage.DiffAgainstAccepted(locked, candidate)
			hints, err := kitstage.ScanRenameHints(resolvedDir, kitstage.InstanceApps(targetAbs), kitstage.SourceMatcher(name, locked.Source, candSource))
			if err != nil {
				return err
			}
			plan.RenameHints = hints

			if checkOnly {
				printUpdatePlan(out, plan, jsonOut)
				fmt.Fprintln(out, "\n--check-only: nothing staged")
				return nil
			}

			if err := kitstage.Stage(targetAbs, name, candidate); err != nil {
				return err
			}
			planPath, err := kitstage.WritePlan(targetAbs, name, plan)
			if err != nil {
				return err
			}
			printUpdatePlan(out, plan, jsonOut)
			if !jsonOut {
				fmt.Fprintf(out, "\nstaged: %s\nplan:   %s\nnext:   kitsoki kit trial %s   (judge)   ·   kitsoki kit reject %s   (drop)\n",
					kitstage.Path(targetAbs), planPath, name, name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", ".", "project root the lockfile is read from")
	cmd.Flags().StringVar(&to, "to", "", "git tier: new @ref for the source; other tiers: version constraint the candidate must satisfy (bare version = exact)")
	cmd.Flags().StringVar(&source, "source", "", "replace the kit's source string entirely (e.g. migrate to a git+ remote)")
	cmd.Flags().BoolVar(&checkOnly, "check-only", false, "resolve and print the plan without staging anything")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the plan as JSON instead of plain text")
	return cmd
}

func kitRejectCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "reject <name>",
		Short: "Drop a staged update candidate (kits.staged.lock entry + kit-update workdir)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			targetAbs, err := absTarget(target)
			if err != nil {
				return err
			}
			f, err := kitstage.Load(kitstage.Path(targetAbs))
			if err != nil {
				return err
			}
			entry := f.Kits[name]
			if entry == nil {
				return fmt.Errorf("kit %q has no staged candidate under %s", name, targetAbs)
			}
			if err := kitstage.Remove(targetAbs, name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rejected staged %s@%s (accepted resolution unchanged)\n", name, displayVersion(entry.Version))
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", ".", "project root the staged lockfile is read from")
	return cmd
}

// printUpdatePlan renders a kitstage.Plan for operators (or as JSON).
func printUpdatePlan(out interface{ Write([]byte) (int, error) }, plan *kitstage.Plan, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(plan)
		return
	}
	fmt.Fprintf(out, "update plan for %s\n", plan.Kit)
	fmt.Fprintf(out, "  from: %s (tree %s)\n", displayVersion(plan.From.Version), shortHash(plan.From.TreeHash))
	fmt.Fprintf(out, "  to:   %s (tree %s)\n", displayVersion(plan.To.Version), shortHash(plan.To.TreeHash))
	switch {
	case plan.ChangedFilesNote != "":
		fmt.Fprintf(out, "  changed files: %s\n", plan.ChangedFilesNote)
	case len(plan.ChangedFiles) == 0:
		fmt.Fprintln(out, "  changed files: none")
	default:
		fmt.Fprintf(out, "  changed files (%d):\n", len(plan.ChangedFiles))
		for _, c := range plan.ChangedFiles {
			fmt.Fprintf(out, "    %-8s %s\n", c.Kind, c.Path)
		}
	}
	if len(plan.RenameHints) > 0 {
		fmt.Fprintln(out, "  rename hints (compat.renamed):")
		for _, h := range plan.RenameHints {
			if h.Detected != "" {
				fmt.Fprintf(out, "    ! %s: %s -> %s — %s\n", h.Category, h.Old, h.New, h.SuggestedAction)
			} else {
				fmt.Fprintf(out, "    · %s: %s -> %s (declared upstream, not detected locally)\n", h.Category, h.Old, h.New)
			}
		}
	}
}

// shortHash abbreviates a tree/commit hash for display.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "(none)"
	}
	return h
}
