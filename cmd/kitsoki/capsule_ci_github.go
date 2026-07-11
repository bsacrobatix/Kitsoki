package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/ci"
)

func capsuleCIGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Project GitHub webhook and check-run payloads for Capsule CI",
		Long: `Project GitHub webhook and check-run payloads for Capsule CI.

These commands are pure adapters: they normalize GitHub webhook JSON into the
standard Capsule CI trigger shape and project persisted Capsule CI run records
into GitHub check-run JSON. They do not call GitHub.`,
	}
	cmd.AddCommand(capsuleCIGitHubTriggerCmd(), capsuleCIGitHubCheckCmd())
	return cmd
}

func capsuleCIGitHubTriggerCmd() *cobra.Command {
	var payloadPath, event, pipeline string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "Normalize a GitHub webhook payload into a Capsule CI trigger",
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := readPayloadFile(cmd, payloadPath)
			if err != nil {
				return err
			}
			switch event {
			case "", "pull_request":
				var payload ci.GitHubPullRequestEvent
				if err := json.Unmarshal(raw, &payload); err != nil {
					return fmt.Errorf("capsule ci github trigger: parse pull_request payload: %w", err)
				}
				trigger, err := ci.NormalizeGitHubPullRequestTrigger(payload, pipeline)
				if err != nil {
					return err
				}
				return capsuleWorkspaceWrite(cmd, trigger, jsonOut)
			default:
				return fmt.Errorf("capsule ci github trigger: unsupported event %q", event)
			}
		},
	}
	cmd.Flags().StringVar(&payloadPath, "payload", "", "GitHub webhook JSON payload path, or - for stdin")
	cmd.Flags().StringVar(&event, "event", "pull_request", "GitHub event name")
	cmd.Flags().StringVar(&pipeline, "pipeline", "change", "requested Capsule CI pipeline")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("payload")
	return cmd
}

func capsuleCIGitHubCheckCmd() *cobra.Command {
	var project, job, detailsURL string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Project a Capsule CI run record to a GitHub check-run payload",
		RunE: func(cmd *cobra.Command, _ []string) error {
			record, err := (ci.FileRunStore{ProjectRoot: project}).Get(job)
			if err != nil {
				return err
			}
			check, err := ci.BuildGitHubCheckRun(record.Result, detailsURL)
			if err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, check, jsonOut)
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&job, "job", "", "Capsule CI job id")
	cmd.Flags().StringVar(&detailsURL, "details-url", "", "GitHub check details URL")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("job")
	return cmd
}

func readPayloadFile(cmd *cobra.Command, path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("payload path is required")
	}
	if path == "-" {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read payload %s: %w", path, err)
	}
	return raw, nil
}
