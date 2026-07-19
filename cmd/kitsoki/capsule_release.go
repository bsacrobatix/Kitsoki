package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/release"
)

// capsuleReleaseCmd deliberately exposes planning and durable manifest lookup
// only. verify/publish need a configured repository inspector/publisher and
// must not silently fall back to the operator's checkout or credentials.
func capsuleReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "release", Short: "Plan and inspect immutable release manifests"}
	cmd.AddCommand(capsuleReleasePlanCmd(), capsuleReleaseCreateCmd(), capsuleReleaseShowCmd())
	return cmd
}

func capsuleReleasePlanCmd() *cobra.Command {
	var input string
	var impacts string
	cmd := &cobra.Command{Use: "plan", Short: "Produce a dry-run release candidate plan", RunE: func(cmd *cobra.Command, _ []string) error {
		candidate, err := readReleaseCandidate(input)
		if err != nil {
			return err
		}
		plan, err := release.BuildPlan(release.PlanInput{Candidate: candidate, Impacts: parseReleaseImpacts(impacts)})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
	}}
	cmd.Flags().StringVar(&input, "input", "", "candidate JSON input")
	cmd.Flags().StringVar(&impacts, "impacts", "", "comma-separated compatibility impacts")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

func capsuleReleaseCreateCmd() *cobra.Command {
	var planPath, storeRoot string
	cmd := &cobra.Command{Use: "create", Short: "Persist a previously dry-run release plan", RunE: func(cmd *cobra.Command, _ []string) error {
		b, err := os.ReadFile(planPath)
		if err != nil {
			return err
		}
		var plan release.Plan
		if err := json.Unmarshal(b, &plan); err != nil {
			return fmt.Errorf("release plan: %w", err)
		}
		candidate, err := release.Create(cmd.Context(), release.FileStore{Root: storeRoot}, plan)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(candidate)
	}}
	cmd.Flags().StringVar(&planPath, "plan", "", "dry-run plan JSON")
	cmd.Flags().StringVar(&storeRoot, "store", ".kitsoki/releases", "manifest store root")
	_ = cmd.MarkFlagRequired("plan")
	return cmd
}

func capsuleReleaseShowCmd() *cobra.Command {
	var storeRoot string
	cmd := &cobra.Command{Use: "show <digest>", Short: "Show an immutable release candidate", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		candidate, err := (release.FileStore{Root: storeRoot}).Get(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(candidate)
	}}
	cmd.Flags().StringVar(&storeRoot, "store", ".kitsoki/releases", "manifest store root")
	return cmd
}

func readReleaseCandidate(path string) (release.Candidate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return release.Candidate{}, err
	}
	var candidate release.Candidate
	if err := json.Unmarshal(b, &candidate); err != nil {
		return release.Candidate{}, fmt.Errorf("release candidate: %w", err)
	}
	return candidate, nil
}
func parseReleaseImpacts(raw string) []release.Impact {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]release.Impact, 0, len(parts))
	for _, part := range parts {
		out = append(out, release.Impact(strings.TrimSpace(part)))
	}
	return out
}
