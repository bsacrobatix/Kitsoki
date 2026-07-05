package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func gitopsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gitops",
		Short: "Story-owned git and ticket operations",
	}
	cmd.AddCommand(gitopsAutonomousFixCmd())
	return cmd
}

func gitopsAutonomousFixCmd() *cobra.Command {
	var (
		runDir        string
		ticketRepo    string
		agentDB       string
		agentStory    string
		publicBaseURL string
		projectRoot   string
		incidentRepo  string
		assetDir      string
		commentMode   string
		reportInvalid bool
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "autonomous-fix",
		Short: "Run the product-journey issue-to-fix gate through the gitops facade",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(runDir) == "" {
				return fmt.Errorf("gitops autonomous-fix: --run-dir is required")
			}
			if strings.TrimSpace(ticketRepo) == "" {
				return fmt.Errorf("gitops autonomous-fix: --ticket-repo is required")
			}
			if strings.TrimSpace(agentDB) == "" {
				return fmt.Errorf("gitops autonomous-fix: --agent-db is required")
			}
			args := []string{
				"tools/product-journey/run.py",
				"--autonomous-fix-loop",
				"--run-dir", runDir,
				"--ticket-repo", ticketRepo,
				"--gh-agent-db", agentDB,
				"--gh-agent-story", firstNonBlank(agentStory, "stories/bugfix"),
				"--gh-agent-public-base-url", publicBaseURL,
				"--gh-agent-project-root", projectRoot,
				"--gh-agent-incident-repo", incidentRepo,
				"--gh-agent-asset-dir", assetDir,
				"--gh-agent-comment-mode", firstNonBlank(commentMode, "none"),
			}
			if jsonOut {
				args = append(args, "--json-output")
			}
			if reportInvalid {
				args = append(args, "--report-invalid-autonomous-fix")
			}
			py := exec.CommandContext(cmd.Context(), "python3", args...)
			py.Dir = "."
			py.Stdout = cmd.OutOrStdout()
			py.Stderr = cmd.ErrOrStderr()
			py.Env = os.Environ()
			return py.Run()
		},
	}
	cmd.Flags().StringVar(&runDir, "run-dir", "", "product-journey run directory")
	cmd.Flags().StringVar(&ticketRepo, "ticket-repo", "", "owner/repo ticket target")
	cmd.Flags().StringVar(&agentDB, "agent-db", "", "sqlite path for durable gh-agent jobs")
	cmd.Flags().StringVar(&agentStory, "agent-story", "stories/bugfix", "story path queued for issue fixes")
	cmd.Flags().StringVar(&publicBaseURL, "public-base-url", "", "public agent base URL used for reviewable run links")
	cmd.Flags().StringVar(&projectRoot, "project-root", "", "local checkout root used by the agent for onboarded repos")
	cmd.Flags().StringVar(&incidentRepo, "incident-repo", "", "owner/repo for agent incidents; defaults to --ticket-repo")
	cmd.Flags().StringVar(&assetDir, "asset-dir", "", "root directory for agent fix evidence assets")
	cmd.Flags().StringVar(&commentMode, "comment-mode", "none", "comment mode for drained jobs: none or github")
	cmd.Flags().BoolVar(&reportInvalid, "report-invalid-autonomous-fix", false, "print invalid autonomous-fix results instead of exiting early")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON output")
	return cmd
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
