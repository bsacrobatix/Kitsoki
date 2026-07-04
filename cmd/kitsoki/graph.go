package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/graph"
	"kitsoki/internal/graph/proposalsadapter"
)

// graphCmd — W1.1: `kitsoki graph lint <dir>` loads a project object graph
// catalog (bundle dir or the single-file seed-objects.yaml shape) and runs
// the cross-node catalog lint (dangling refs, edge type mismatches, cycles
// on acyclic-marked edges, internal nodes reachable from a public edge).
func graphCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Project object graph catalog tools",
	}
	cmd.AddCommand(graphLintCmd())
	cmd.AddCommand(graphApplyCmd())
	return cmd
}

func graphLintCmd() *cobra.Command {
	var checkIndex bool
	var proposalsDir string
	cmd := &cobra.Command{
		Use:   "lint <catalog-path>",
		Short: "Validate a project object graph catalog's cross-node invariants",
		Long: `Loads the catalog at <catalog-path> (a bundle directory, or a single
catalog file such as docs/proposals/project-object-graph/seed-objects.yaml)
and reports every dangling edge reference, edge target type mismatch, cycle
on an acyclic-marked edge, and internal node reachable from a public edge.

With --check-index (W6.0), also regenerates each graph-sourced proposal's
docs/proposals/README.md "Current proposals" index entry and byte-compares
it against --proposals-dir/README.md (default: docs/proposals), failing on
any drift — the machine-checkable-docs principle: a hand-maintained index
next to graph-sourced data rots.

Exit code 0 when the catalog loads and lints clean (and the index doesn't
drift, if checked); non-zero otherwise.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			cat, err := graph.LoadCatalog(path)
			if err != nil {
				return fmt.Errorf("graph lint: %w", err)
			}
			for _, w := range cat.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			issues := graph.Lint(cat)
			out := cmd.OutOrStdout()
			for _, iss := range issues {
				fmt.Fprintln(out, iss.Error())
			}

			var indexErrs []string
			if checkIndex {
				indexErrs = checkProposalsIndex(cat, proposalsDir)
				for _, e := range indexErrs {
					fmt.Fprintln(out, "index-drift:", e)
				}
			}

			if len(issues) == 0 && len(indexErrs) == 0 {
				fmt.Fprintf(out, "graph lint: %d nodes, clean\n", len(cat.Nodes))
				return nil
			}
			return fmt.Errorf("graph lint: %d issue(s), %d index-drift error(s) found", len(issues), len(indexErrs))
		},
	}
	cmd.Flags().BoolVar(&checkIndex, "check-index", false, "also check docs/proposals/README.md's generated index for drift (W6.0)")
	cmd.Flags().StringVar(&proposalsDir, "proposals-dir", "docs/proposals", "directory containing README.md for --check-index")
	return cmd
}

// checkProposalsIndex regenerates every graph-sourced proposal's README
// index entry and reports any that don't byte-match a line in
// <proposalsDir>/README.md.
func checkProposalsIndex(cat *graph.Catalog, proposalsDir string) []string {
	readmePath := filepath.Join(proposalsDir, "README.md")
	raw, err := os.ReadFile(readmePath)
	if err != nil {
		return []string{fmt.Sprintf("read %s: %v", readmePath, err)}
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		lines[l] = true
	}

	var errs []string
	for _, node := range proposalsadapter.GraphSourcedProposals(cat) {
		entry, err := proposalsadapter.RenderIndexEntry(node)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", node.ID, err))
			continue
		}
		if !lines[entry] {
			errs = append(errs, fmt.Sprintf("%s: generated entry not found in %s (regenerate and update the index): %s", node.ID, readmePath, entry))
		}
	}
	return errs
}

func graphApplyCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "apply <changeset-id> <catalog-path>",
		Short: "Apply an authorized changeset node's operations to a catalog",
		Long: `Loads the catalog at <catalog-path>, finds the changeset node
<changeset-id>, and — dry-run-first — builds a candidate catalog on a scratch
copy, re-loads and re-lints it, and only if that comes back clean commits the
changed files. A rejected changeset (failing pre-apply validation or
post-apply lint) never touches the real catalog.

The changeset's status must be "authorized" to apply for real; pass --dry-run
to preview a changeset in any status without requiring authorization or
committing anything.

Exit code 0 on a successful apply (or a clean dry-run); non-zero on rejection.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			changesetID, path := graph.NodeID(args[0]), args[1]
			res, err := graph.Apply(path, changesetID, dryRun)
			if err != nil {
				return fmt.Errorf("graph apply: %w", err)
			}
			out := cmd.OutOrStdout()
			for _, r := range res.RejectReasons {
				fmt.Fprintln(out, "reject:", r)
			}
			for _, iss := range res.LintIssues {
				fmt.Fprintln(out, "reject (post-apply lint):", iss.Error())
			}
			if res.Rejected() {
				return fmt.Errorf("graph apply: changeset %q rejected, catalog untouched", changesetID)
			}
			verb := "applied"
			if dryRun {
				verb = "dry-run clean"
			}
			fmt.Fprintf(out, "graph apply: %s, %d file(s) changed: %v\n", verb, len(res.ChangedFiles), res.ChangedFiles)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the changeset without requiring authorization or committing")
	return cmd
}
