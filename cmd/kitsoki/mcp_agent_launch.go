package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	kitsokimcp "kitsoki/internal/mcp"
)

func mcpAgentLaunchCmd() *cobra.Command {
	var allowed []string
	var configPath string
	cmd := &cobra.Command{
		Use:   "mcp-agent-launch",
		Short: "Run a minimal MCP server for allowlisted Kitsoki agent launches",
		Long:  "Expose only agent.launch_plan and agent.launch. Every child agent name must be supplied with --allow-agent and every execution goes through the normal Kitsoki agent launch policy checks. This server does not expose Studio, raw backend arguments, arbitrary agent files, or a shell.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := kitsokimcp.NewAgentLaunchServer(kitsokimcp.AgentLaunchConfig{AllowedAgents: allowed, ConfigPath: configPath})
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringArrayVar(&allowed, "allow-agent", nil, "agent name permitted through this server; repeatable (required)")
	cmd.Flags().StringVar(&configPath, "config", "", "Kitsoki config file passed to child launches")
	return cmd
}
