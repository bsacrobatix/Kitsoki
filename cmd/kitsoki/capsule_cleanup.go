package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/hygiene"
)

func capsuleCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cleanup", Short: "Plan and apply local Capsule disk hygiene"}
	cmd.AddCommand(capsuleCleanupPlanCmd(), capsuleCleanupApplyCmd())
	return cmd
}

type capsuleCleanupFlags struct {
	project             string
	keepRuns            int
	includeCapsuleCache bool
	includeGoBuildCache bool
	jsonOut             bool
}

func (f *capsuleCleanupFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.project, "project", ".", "project root")
	cmd.Flags().IntVar(&f.keepRuns, "keep-runs", 20, "number of newest Capsule CI run records to retain")
	cmd.Flags().BoolVar(&f.includeCapsuleCache, "include-capsule-cache", false, "include project .capsules/cache entries")
	cmd.Flags().BoolVar(&f.includeGoBuildCache, "include-go-build-cache", false, "include the Go build/test cache via go clean")
	cmd.Flags().BoolVar(&f.jsonOut, "json", true, "print JSON")
}

func (f capsuleCleanupFlags) options() hygiene.Options {
	return hygiene.Options{ProjectRoot: f.project, KeepRuns: f.keepRuns, IncludeCapsuleCache: f.includeCapsuleCache, IncludeGoBuildCache: f.includeGoBuildCache}
}

func capsuleCleanupPlanCmd() *cobra.Command {
	var flags capsuleCleanupFlags
	cmd := &cobra.Command{Use: "plan", Short: "Show reclaimable Capsule-managed disk state without deleting it", RunE: func(cmd *cobra.Command, args []string) error {
		plan, err := hygiene.BuildPlan(cmd.Context(), flags.options())
		if err != nil {
			return err
		}
		return capsuleCleanupWrite(cmd, plan, flags.jsonOut)
	}}
	flags.bind(cmd)
	return cmd
}

func capsuleCleanupApplyCmd() *cobra.Command {
	var flags capsuleCleanupFlags
	cmd := &cobra.Command{Use: "apply", Short: "Apply a local Capsule cleanup plan", RunE: func(cmd *cobra.Command, args []string) error {
		result, err := hygiene.Apply(cmd.Context(), flags.options())
		if err != nil {
			return err
		}
		return capsuleCleanupWrite(cmd, result, flags.jsonOut)
	}}
	flags.bind(cmd)
	return cmd
}

func capsuleCleanupWrite(cmd *cobra.Command, value any, jsonOut bool) error {
	if jsonOut {
		return capsuleWorkspaceWrite(cmd, value, true)
	}
	switch v := value.(type) {
	case hygiene.Plan:
		fmt.Fprintf(cmd.OutOrStdout(), "cleanup candidates: %d (%d bytes)\n", len(v.Candidates), v.TotalBytes)
	case hygiene.ApplyResult:
		fmt.Fprintf(cmd.OutOrStdout(), "cleanup removed: %d (%d bytes)\n", len(v.Removed), v.TotalBytes)
	default:
		fmt.Fprintln(cmd.OutOrStdout(), value)
	}
	return nil
}
