// mcp_graph.go — implements `kitsoki mcp-graph`.
//
// Runs the dedicated stdio MCP server exposing kitsoki's project-object-graph
// read family and feedback channel (graph-mcp plan §3, stage P2). Every
// tool invokes host.graph.* engine ops through a host.Registry — never
// internal/graph directly.
package main

import (
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"kitsoki/internal/mcp/graphsrv"
)

func mcpGraphCmd() *cobra.Command {
	var (
		catalogFlags []string
		mode         string
		actor        string
		feedbackSink string
		journalPath  string
		clockFixed   string
	)
	cmd := &cobra.Command{
		Use:   "mcp-graph",
		Short: "Run the stdio MCP server for the project object graph (read family + feedback)",
		Long: `mcp-graph exposes kitsoki's project-object-graph read family (graph.open,
graph.get, graph.find, graph.neighbors, graph.type, graph.lint, graph.impact)
and feedback channel (feedback.report, feedback.list) as a standalone stdio
MCP server. Write tools (propose/authorize/apply/withdraw/changeset-mutate)
are not part of this stage.

Every tool call is bound to one of the catalogs named by --catalog; a tool's
"catalog" argument selects among bound aliases and never accepts a raw
filesystem path.

Examples:

  kitsoki mcp-graph --catalog pog/catalog.yaml

  kitsoki mcp-graph --catalog main=pog/catalog.yaml --catalog docs=docs/catalog.yaml --mode read`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := graphsrv.ValidateMode(mode); err != nil {
				return err
			}
			if err := graphsrv.ValidateFeedbackSink(feedbackSink); err != nil {
				return err
			}
			srv, err := graphsrv.NewServer(graphsrv.Config{
				CatalogFlags: catalogFlags,
				Mode:         mode,
				Actor:        actor,
				FeedbackSink: feedbackSink,
				JournalPath:  journalPath,
				ClockFixed:   clockFixed,
			})
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringArrayVar(&catalogFlags, "catalog", nil, "[alias=]path to a bound catalog; repeatable, first is default (default: probe pog/catalog.yaml under cwd)")
	cmd.Flags().StringVar(&mode, "mode", graphsrv.DefaultMode, "one of: read, propose, steward (gates future write-tool registration)")
	cmd.Flags().StringVar(&actor, "actor", "", "actor name stamped on future write-tool calls (P4)")
	cmd.Flags().StringVar(&feedbackSink, "feedback-sink", graphsrv.FeedbackSinkLocal, "one of: local, catalog, github (P2 always writes locally; catalog/github record the choice but degrade to local-only)")
	cmd.Flags().StringVar(&journalPath, "journal", "", "receipts JSONL path for future write-tool calls (P4); accepted now for forward compat")
	cmd.Flags().StringVar(&clockFixed, "clock-fixed", "", "RFC3339 timestamp to pin the server's clock to (also honors KITSOKI_GRAPH_CLOCK_FIXED)")
	return cmd
}
