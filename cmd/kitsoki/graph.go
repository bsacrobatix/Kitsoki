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
