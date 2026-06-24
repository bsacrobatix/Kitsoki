package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"kitsoki/internal/ghagent"
	"kitsoki/internal/host"
	"kitsoki/internal/jobs"

	_ "modernc.org/sqlite"
)

// newGHAgentCmd builds `kitsoki gh-agent`, whose single `poll` subcommand runs
// ONE poll cycle of the @kitsoki mention -> dispatch -> run -> ack loop:
// ListGitHubInboxItems (through the cliExec seam) -> FilterMentions -> for each
// mention, Dispatcher.Dispatch. Single-shot; the serve daemon is deferred.
func newGHAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gh-agent",
		Short: "Drive the @kitsoki GitHub mention -> dispatch -> run -> ack loop",
	}
	cmd.AddCommand(newGHAgentPollCmd())
	return cmd
}

func newGHAgentPollCmd() *cobra.Command {
	var (
		repo        string
		mentionFile string
		dbPath      string
		trigger     string
		worker      string
	)
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Run one poll cycle: list mentions, dispatch the mapped story, ack",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			items, err := pollInboxItems(ctx, repo, mentionFile)
			if err != nil {
				return err
			}
			mentions := ghagent.FilterMentions(items, repo, trigger)

			if dbPath == "" {
				dbPath = ":memory:"
			}
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				return fmt.Errorf("gh-agent: open db %q: %w", dbPath, err)
			}
			defer db.Close()

			store, err := jobs.NewGHJobStore(db)
			if err != nil {
				return err
			}

			d := &ghagent.Dispatcher{
				Jobs:     store,
				Routes:   ghagent.DefaultLabelStoryMap(),
				Comments: &ghagent.CommentStore{Exec: host.GitHubTicketHandler, Repo: repo},
				WorkerID: worker,
				SpawnFn:  ghagent.RunStorySession,
			}

			for _, m := range mentions {
				job, err := d.Dispatch(ctx, m, nil)
				if err != nil {
					fmt.Fprintf(os.Stderr, "gh-agent: dispatch %s: %v\n", m.OriginRef, err)
					continue
				}
				fmt.Printf("%s -> %s (state=%s)\n", job.OriginRef, job.Story, job.State)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo slug to poll")
	cmd.Flags().StringVar(&mentionFile, "mention", "", "JSON file with []host.GitHubInboxItem (bypasses the live gh list)")
	cmd.Flags().StringVar(&dbPath, "db", "", "sqlite path for the gh_jobs store (default in-memory)")
	cmd.Flags().StringVar(&trigger, "trigger", ghagent.DefaultMentionTrigger, "mention trigger literal")
	cmd.Flags().StringVar(&worker, "worker", "gh-agent-1", "worker id holding the claim")
	return cmd
}

// pollInboxItems reads the inbox: from a JSON fixture when --mention is set,
// otherwise via ListGitHubInboxItems (which shells gh through the cliExec seam).
func pollInboxItems(ctx context.Context, repo, mentionFile string) ([]host.GitHubInboxItem, error) {
	if mentionFile != "" {
		raw, err := os.ReadFile(mentionFile)
		if err != nil {
			return nil, fmt.Errorf("gh-agent: read mention file: %w", err)
		}
		var items []host.GitHubInboxItem
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("gh-agent: parse mention file: %w", err)
		}
		return items, nil
	}
	return host.ListGitHubInboxItems(ctx, host.GitHubInboxOptions{
		Repo:          repo,
		IncludeIssues: true,
		IncludePRs:    true,
	})
}
