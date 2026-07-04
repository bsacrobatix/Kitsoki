package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"kitsoki/internal/testrunner"
)

func testFlowCoverageCmd() *cobra.Command {
	var (
		flowsGlob          string
		jsonOut            string
		minBranchCoverage  float64
		requireAllBranches bool
		maxCombinations    int
	)

	cmd := &cobra.Command{
		Use:   "flow-coverage <app.yaml>",
		Short: "Report static coverage of story flow fixtures",
		Long: `Report which authored story transition branches are exercised by flow fixtures.

This is a static coverage ledger for fixture completeness. It does not execute
the flows and never calls an LLM; run 'kitsoki test flows' for behavioral
correctness. A transition branch is credited when a fixture turn submits the
same state+intent and either the branch is unambiguous or the turn pins an
expect_state / expect_state_in that matches the branch target.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appPath := args[0]
			if flowsGlob == "" {
				flowsGlob = defaultFlowsGlob(appPath)
			}
			report, err := testrunner.RunFlowCoverage(context.Background(), appPath, testrunner.FlowCoverageOptions{
				FlowsGlob:          flowsGlob,
				JSONOut:            jsonOut,
				MinBranchCoverage:  minBranchCoverage,
				RequireAllBranches: requireAllBranches,
				MaxCombinations:    maxCombinations,
				ImportResolver:     buildImportResolver(),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "kitsoki test flow-coverage: %v\n", err)
				os.Exit(2)
			}
			printFlowCoverageReport(report)
			if !report.Passed {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&flowsGlob, "flows", "", "glob for flow fixture files (default: <app-dir>/flows/*.yaml)")
	cmd.Flags().StringVar(&jsonOut, "json", "", "write JSON report to this file")
	cmd.Flags().Float64Var(&minBranchCoverage, "min-branch", 0, "minimum authored branch coverage percentage required to pass")
	cmd.Flags().BoolVar(&requireAllBranches, "require-all-branches", false, "fail when any authored transition branch is uncovered")
	cmd.Flags().IntVar(&maxCombinations, "max-combinations", 64, "maximum enum-slot combination product to expand per state+intent")
	return cmd
}

func printFlowCoverageReport(report *testrunner.FlowCoverageReport) {
	fmt.Printf("Story flow coverage for %s\n", report.App)
	fmt.Printf("Flows: %s\n\n", report.FlowsGlob)

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(w, "STATUS\tSTATE\tINTENT\tBRANCH\tTARGET\n")
	for _, branch := range report.Branches {
		status := "MISS"
		if branch.Covered {
			status = "HIT"
		}
		target := branch.Target
		if branch.When != "" {
			target += " when " + branch.When
		} else if branch.Default {
			target += " default"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", status, branch.State, branch.Intent, branch.Index, target)
	}
	_ = w.Flush()

	if len(report.ParameterChecks) > 0 {
		fmt.Println("\nParameter coverage gaps:")
		w = tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		for _, check := range report.ParameterChecks {
			if check.Passed {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t", check.State, check.Intent)
			if len(check.MissingValues) > 0 {
				fmt.Fprintf(w, "missing enum values: %v", check.MissingValues)
			}
			if len(check.MissingCombinations) > 0 {
				fmt.Fprintf(w, " missing combinations: %d", len(check.MissingCombinations))
			}
			if check.SkippedReason != "" {
				fmt.Fprintf(w, " combinations skipped: %s", check.SkippedReason)
			}
			fmt.Fprintln(w)
		}
		_ = w.Flush()
	}

	if len(report.Problems) > 0 {
		fmt.Println("\nProblems:")
		for _, problem := range report.Problems {
			fmt.Printf("  - %s\n", problem)
		}
	}

	total := report.BranchCoverage.Total
	fmt.Printf("\nSummary: %d/%d authored branches covered (%.1f%%)\n",
		report.BranchCoverage.Covered, total, report.BranchCoverage.Percent)
	if report.Passed {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
	}
	if report.BranchCoverage.Covered != total {
		fmt.Printf("Tip: add expect_state to ambiguous guarded turns so coverage can prove the branch, then inspect %s with --json for CI artifacts.\n", filepath.Base(report.AppPath))
	}
}
