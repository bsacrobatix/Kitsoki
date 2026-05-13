// bug.go — implements `kitsoki bug create` and `kitsoki bug list`.
//
// File-system backend for bug reports filed via /meta bug (handled by the
// builtin `bug-reporter` agent). Each report is written as a single
// markdown file under <app-dir>/bugs/<UTC-timestamp>-<slug>.md so the
// pile is grep-friendly and survives without any database. The agent
// invokes `kitsoki bug create` via Bash; the kitsoki binary's own
// directory is prepended to PATH for every claude subprocess
// (internal/host/oracle_runner.go) so this resolves whether kitsoki
// was launched via `go run`, `go install`, or a packaged build.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
)

// bugCmd is the top-level `bug` subcommand. Today it has one child
// (`create`); future work adds `list` for the agent to find prior
// reports before filing a duplicate.
func bugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bug",
		Short: "File and inspect local bug reports",
		Long: `Local-filesystem bug-tracking primitives.

Bugs are stored as markdown files under <app-dir>/bugs/, one file per
report, named "<UTC timestamp>-<slug>.md". The agent (/meta bug) calls
this subcommand to record what the user described; humans grep or
edit the files directly.

No external service, no schema beyond the markdown template. Move a
bug to an external tracker by copying the file's body verbatim.`,
	}
	cmd.AddCommand(bugCreateCmd())
	return cmd
}

// bugCreateCmd implements `kitsoki bug create`. Writes one markdown
// file to <app-dir>/bugs/ and prints its relative path on stdout so
// the agent can echo it back to the user.
func bugCreateCmd() *cobra.Command {
	var (
		title       string
		body        string
		reproSteps  []string
		statePath   string
		appID       string
		appDir      string
		clockNowSec int64
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "File a new bug report (writes a markdown file)",
		Long: `Append a bug report to <app-dir>/bugs/.

Required:
  --title       one-line title (becomes the slug after lowercasing + hyphenating)
  --body        the narrative — what was expected, what happened, why it matters

Optional:
  --repro       repeatable: one reproduction step per flag (numbered in output)
  --state-path  state where the bug surfaced (e.g. main.foyer)
  --app-id      running app's id (cloak, dev-story, etc.)
  --app-dir     directory under which bugs/ lives (default: cwd)

Output: prints the relative path to the created file. Exit 1 on error.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if strings.TrimSpace(title) == "" {
				return fmt.Errorf("--title is required")
			}
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("--body is required")
			}
			now := time.Now().UTC()
			if clockNowSec > 0 {
				now = time.Unix(clockNowSec, 0).UTC()
			}
			dir := appDir
			if dir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve cwd: %w", err)
				}
				dir = cwd
			}
			bugsDir := filepath.Join(dir, "bugs")
			if err := os.MkdirAll(bugsDir, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", bugsDir, err)
			}
			filename := bugFilename(now, title)
			full := filepath.Join(bugsDir, filename)
			content := renderBugMarkdown(bugRecord{
				Title:      title,
				Body:       body,
				ReproSteps: reproSteps,
				StatePath:  statePath,
				AppID:      appID,
				FiledAt:    now,
			})
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", full, err)
			}
			// Print the relative path under app-dir so the agent's echo
			// stays portable across machines.
			rel, err := filepath.Rel(dir, full)
			if err != nil {
				rel = full
			}
			fmt.Fprintln(cmd.OutOrStdout(), rel)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "one-line bug title (required)")
	cmd.Flags().StringVar(&body, "body", "", "the narrative — what was expected, what happened (required)")
	cmd.Flags().StringArrayVar(&reproSteps, "repro", nil,
		"reproduction step; pass --repro repeatedly to record numbered steps")
	cmd.Flags().StringVar(&statePath, "state-path", "", "FSM state where the bug surfaced (optional)")
	cmd.Flags().StringVar(&appID, "app-id", "", "id of the running app (optional)")
	cmd.Flags().StringVar(&appDir, "app-dir", "", "directory under which bugs/ lives (default: cwd)")
	cmd.Flags().Int64Var(&clockNowSec, "clock-now", 0,
		"Unix-seconds override for the filed-at timestamp (tests only; 0 = use real clock)")
	return cmd
}

// bugRecord is the in-memory representation of a single bug, passed to
// renderBugMarkdown.
type bugRecord struct {
	Title      string
	Body       string
	ReproSteps []string
	StatePath  string
	AppID      string
	FiledAt    time.Time
}

// bugFilename produces the on-disk name for a bug: "<UTC timestamp>-<slug>.md".
// Timestamp format is RFC-3339-ish but filesystem-safe (no colons). Slug is
// the title lowercased, ASCII-only, hyphenated, and truncated to keep paths
// reasonable. Two bugs filed in the same second with the same title produce
// the same filename — that's intentional: the second WriteFile silently
// overwrites the first, which is the right behaviour for an agent that
// re-runs the same call after a transient error.
func bugFilename(filedAt time.Time, title string) string {
	ts := filedAt.Format("2006-01-02T150405Z")
	slug := bugSlug(title)
	return ts + "-" + slug + ".md"
}

// bugSlug converts a freeform title into a filesystem-safe slug:
// lowercase, ASCII letters/digits and hyphens, hyphen-separated, trimmed
// to 60 chars. Empty result falls back to "bug" so the filename is
// always well-formed.
func bugSlug(title string) string {
	var b strings.Builder
	lastHyphen := true
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// non-ASCII letters/digits — collapse to a hyphen so the
			// slug stays portable across filesystems.
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		out = "bug"
	}
	if len(out) > 60 {
		out = strings.TrimRight(out[:60], "-")
	}
	return out
}

// renderBugMarkdown produces the file body. Format is human-edit-friendly:
// a YAML front-matter block for machine fields (state, app, filed-at)
// followed by markdown narrative. The agent that re-reads bugs/ for
// duplicate detection can parse the front-matter; humans skim the body.
func renderBugMarkdown(r bugRecord) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("title: ")
	sb.WriteString(yamlQuoteLine(r.Title))
	sb.WriteString("\nfiled_at: ")
	sb.WriteString(r.FiledAt.Format(time.RFC3339))
	if r.AppID != "" {
		sb.WriteString("\napp_id: ")
		sb.WriteString(yamlQuoteLine(r.AppID))
	}
	if r.StatePath != "" {
		sb.WriteString("\nstate_path: ")
		sb.WriteString(yamlQuoteLine(r.StatePath))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString("# ")
	sb.WriteString(r.Title)
	sb.WriteString("\n\n")
	sb.WriteString(strings.TrimSpace(r.Body))
	sb.WriteString("\n")
	if len(r.ReproSteps) > 0 {
		sb.WriteString("\n## Steps to reproduce\n\n")
		for i, step := range r.ReproSteps {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(step))
		}
	}
	return sb.String()
}

// yamlQuoteLine returns s wrapped in double quotes with inner quotes and
// backslashes escaped. Keeps the front-matter parseable when the title
// contains colons, quotes, or other YAML metacharacters.
func yamlQuoteLine(s string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + escaped + `"`
}
