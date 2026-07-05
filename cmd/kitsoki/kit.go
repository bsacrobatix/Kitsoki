package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"kitsoki/internal/kitverify"
	"kitsoki/internal/testrunner"
)

// kitCmd is the `kitsoki kit ...` command group (S4,
// .context/kits-implementation-plan.md). Today it holds only `kit verify`;
// the plan doc's S2 slice (wi-2-3, landing in parallel) scaffolds
// `kit add|list|update|dev` as siblings under this same group — expect a
// small, mechanical merge conflict here (this file's own AddCommand calls
// vs theirs), not a design conflict.
func kitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kit",
		Short: "Kit manifest tools (kit.yaml — see docs/proposals/kits.md)",
	}
	cmd.AddCommand(kitVerifyCmd())
	return cmd
}

// kitVerifyCmd implements `kitsoki kit verify <kit-dir>`.
func kitVerifyCmd() *cobra.Command {
	var (
		jsonOut       bool
		recordingPath string
		failFast      bool
		verbose       bool
	)

	cmd := &cobra.Command{
		Use:   "verify <kit-dir>",
		Short: "Run a kit's standalone contract checks and no-LLM conformance flow suite",
		Long: `Loads the kit.yaml manifest at <kit-dir> and, for each story it
provides:

  - checks that every exit-firing transition sets the world keys its
    exits.<name>.requires: declares (standalone — no importer needed)
  - checks that every name in exports.intents: is actually defined in
    intents:
  - checks that every host_interfaces.<name>.default's declared operation
    input/output shapes match either its starlark sidecar (when
    host_bindings bound it to a script) or a registered Go handler schema
    (internal/host/opschema) — an unregistered handler is skipped, not
    flagged

It then runs every glob in conformance.flows: (kit-root-relative) as a
no-LLM flow/cassette suite via the same runner as ` + "`kitsoki test flows`" + `,
and — when the manifest declares extends: dependencies — notes that a base
kit's own conformance suite would be re-run too, given a resolver (S2's
kit-resolution/lockfile machinery is not wired into this command yet; see
the PR description's flagged decisions).

Exit codes:
  0  every check and every flow passed
  1  a contract check failed or a flow failed
  2  fatal error (bad kit.yaml, bad glob, ...)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kitDir := args[0]
			opts := kitverify.Options{
				ImportResolver: buildImportResolver(),
				Flow: testrunner.FlowOptions{
					RecordingOverride: recordingPath,
					FailFast:          failFast,
					Verbose:           verbose,
				},
			}
			report, err := kitverify.VerifyKit(kitDir, opts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "kitsoki kit verify: %v\n", err)
				os.Exit(2)
			}

			out := cmd.OutOrStdout()
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return fmt.Errorf("encode report: %w", err)
				}
			} else {
				printVerifyReport(out, report)
			}

			if !report.OK() {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON instead of plain text")
	cmd.Flags().StringVar(&recordingPath, "recording", "", "override the recording path declared in conformance flow fixtures")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "stop each flow suite at its first failure")
	cmd.Flags().BoolVar(&verbose, "v", false, "verbose per-turn flow output")

	return cmd
}

// printVerifyReport renders a kitverify.Report as the plain-text default
// output, mirroring validateCmd's ✓/✗ convention.
func printVerifyReport(out interface{ Write([]byte) (int, error) }, report *kitverify.Report) {
	status := "✓"
	if !report.OK() {
		status = "✗"
	}
	fmt.Fprintf(out, "%s %s (%s)\n", status, report.Kit, report.Dir)

	if len(report.ParamIssues) > 0 {
		fmt.Fprintln(out, "  parameters:")
		for _, issue := range report.ParamIssues {
			fmt.Fprintf(out, "    - %s\n", issue)
		}
	}

	for _, s := range report.Stories {
		st := "✓"
		if len(s.Issues) > 0 {
			st = "✗"
		}
		fmt.Fprintf(out, "  %s story %s\n", st, s.Story)
		for _, issue := range s.Issues {
			fmt.Fprintf(out, "      - %s\n", issue)
		}
	}

	for _, f := range report.Flows {
		switch {
		case f.Err != nil:
			fmt.Fprintf(out, "  ✗ flows %s: %v\n", f.Pattern, f.Err)
		case f.Report == nil:
			fmt.Fprintf(out, "  · flows %s: no fixtures matched\n", f.Pattern)
		default:
			st := "✓"
			if f.Report.Failed > 0 {
				st = "✗"
			}
			fmt.Fprintf(out, "  %s flows %s: %d passed, %d failed (app %s)\n", st, f.Pattern, f.Report.Passed, f.Report.Failed, f.AppPath)
		}
	}

	for _, e := range report.Extends {
		if e.Err != nil {
			fmt.Fprintf(out, "  · extends %s: skipped (%v)\n", e.Kit, e.Err)
			continue
		}
		st := "✓"
		if e.Report != nil && !e.Report.OK() {
			st = "✗"
		}
		fmt.Fprintf(out, "  %s extends %s: re-ran base kit's own conformance suite\n", st, e.Kit)
	}
}
