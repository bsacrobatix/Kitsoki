package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"kitsoki/internal/graph"
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
	return &cobra.Command{
		Use:   "lint <catalog-path>",
		Short: "Validate a project object graph catalog's cross-node invariants",
		Long: `Loads the catalog at <catalog-path> (a bundle directory, or a single
catalog file such as docs/proposals/project-object-graph/seed-objects.yaml)
and reports every dangling edge reference, edge target type mismatch, cycle
on an acyclic-marked edge, and internal node reachable from a public edge.

Exit code 0 when the catalog loads and lints clean; non-zero otherwise.`,
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
			if len(issues) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "graph lint: %d nodes, clean\n", len(cat.Nodes))
				return nil
			}
			for _, iss := range issues {
				fmt.Fprintln(cmd.OutOrStdout(), iss.Error())
			}
			return fmt.Errorf("graph lint: %d issue(s) found", len(issues))
		},
	}
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
