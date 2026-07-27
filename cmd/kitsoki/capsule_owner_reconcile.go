package main

import (
	"time"

	"github.com/spf13/cobra"
	"kitsoki/internal/capsule/hygiene"
)

func capsuleCleanupOwnerReconcileCmd() *cobra.Command {
	var project string
	var minAge time.Duration
	var apply, jsonOut bool
	cmd := &cobra.Command{Use: "reconcile-owners", Short: "Plan conservative recovery of proven-orphaned active Capsule records", Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		opts := hygiene.OwnerReconcileOptions{ProjectRoot: project, MinAge: minAge}
		if !apply {
			plan, err := hygiene.BuildOwnerReconcilePlan(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, plan, jsonOut)
		}
		result, err := hygiene.ApplyOwnerReconcilePlan(cmd.Context(), opts)
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, result, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().DurationVar(&minAge, "min-age", 72*time.Hour, "minimum inactive record age")
	cmd.Flags().BoolVar(&apply, "apply", false, "mark only proven orphaned records failed; never remove workspace paths")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	return cmd
}
