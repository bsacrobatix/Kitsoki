package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/host"
	"kitsoki/internal/jobs"

	_ "modernc.org/sqlite"
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
		Short: "Run the product-journey issue-to-fix gate through native gitops",
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
			result, err := runGitopsAutonomousFix(cmd.Context(), gitopsAutonomousFixOptions{
				RunDir:        runDir,
				TicketRepo:    ticketRepo,
				AgentDB:       agentDB,
				AgentStory:    firstNonBlank(agentStory, "stories/bugfix"),
				PublicBaseURL: publicBaseURL,
				ProjectRoot:   projectRoot,
				IncidentRepo:  incidentRepo,
				AssetDir:      assetDir,
				CommentMode:   firstNonBlank(commentMode, "none"),
			})
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetEscapeHTML(false)
				if err := enc.Encode(result); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", stringValue(result, "status"))
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", stringValue(result, "filing_summary"))
				fmt.Fprintf(cmd.OutOrStdout(), "GH-agent drain: %s (%d done, %d failed, %d active)\n",
					stringValue(result, "gh_agent_drain_status"),
					intValue(result, "gh_agent_done_count"),
					intValue(result, "gh_agent_failed_count"),
					intValue(result, "gh_agent_active_count"))
				fmt.Fprintf(cmd.OutOrStdout(), "Review: %s\n", stringValue(result, "review_summary"))
				fmt.Fprintf(cmd.OutOrStdout(), "Validation: %s (%d errors, %d warnings)\n",
					stringValue(result, "validation_status"),
					intValue(result, "validation_errors"),
					intValue(result, "validation_warnings"))
			}
			if stringValue(result, "status") != "autonomous_fix_valid" && !reportInvalid {
				return fmt.Errorf("gitops autonomous-fix: %s", stringValue(result, "autonomous_gate_summary"))
			}
			return nil
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

type gitopsAutonomousFixOptions struct {
	RunDir        string
	TicketRepo    string
	AgentDB       string
	AgentStory    string
	PublicBaseURL string
	ProjectRoot   string
	IncidentRepo  string
	AssetDir      string
	CommentMode   string
}

func runGitopsAutonomousFix(ctx context.Context, opts gitopsAutonomousFixOptions) (map[string]any, error) {
	if os.Getenv("KITSOKI_GITOPS_AUTOFIX_USE_KITSOKI_BIN_FAKE") == "1" {
		return runGitopsAutonomousFixViaRunner(ctx, opts)
	}
	runDir := strings.TrimSpace(opts.RunDir)
	assetDir := firstNonBlank(opts.AssetDir, filepath.Join(runDir, "gh-agent-assets"))
	filed, err := host.GitHubFileFindings(ctx, host.FindingsFilingInput{
		RunDir:     runDir,
		Repo:       opts.TicketRepo,
		KitsokiRev: gitShortRevCWD(),
		FiledBy:    os.Getenv("USER"),
	})
	if err != nil {
		return nil, err
	}
	result := gitopsFilingResult(runDir, opts.TicketRepo, filed)
	enqueue, err := gitopsEnqueueFixes(ctx, runDir, opts.AgentDB, opts.TicketRepo, opts.AgentStory)
	if err != nil {
		return nil, err
	}
	mergeMaps(result, enqueue)
	drain, err := gitopsDrainFixes(ctx, opts.AgentDB, opts.TicketRepo, opts.PublicBaseURL, opts.ProjectRoot, opts.IncidentRepo, assetDir, opts.CommentMode)
	if err != nil {
		return nil, err
	}
	mergeMaps(result, drain)
	if err := gitopsRecordGHAgent(runDir, result); err != nil {
		return nil, err
	}

	storySummary, _ := productJourneyJSON(ctx, "--summarize-run", "--run-dir", runDir)
	mergeMaps(result, storySummary)
	reportPath, err := writeGitopsAutonomousReport(runDir, result, nil, nil)
	if err != nil {
		return nil, err
	}
	result["autonomous_fix_report_path"] = reportPath

	review, err := productJourneyJSON(ctx, "--review-run", "--run-dir", runDir)
	if err != nil {
		return nil, err
	}
	validation, err := productJourneyJSON(ctx, "--validate-run", "--run-dir", runDir)
	if err != nil {
		return nil, err
	}
	storySummary, _ = productJourneyJSON(ctx, "--summarize-run", "--run-dir", runDir)
	mergeMaps(result, storySummary)

	reviewFailed := firstNonZero(intValue(review, "review_failed_count"), intValue(review, "failed"))
	reviewOK := stringValue(review, "review_status") == "ready" && reviewFailed == 0
	validationOK := stringValue(validation, "status") == "valid" && intValue(validation, "errors") == 0
	filedIssueCount := len(stringSliceValue(result, "filed_issue_urls"))
	filingOK := stringValue(result, "status") == "findings_filed" &&
		filedIssueCount > 0 &&
		intValue(result, "findings_failed_count") == 0 &&
		intValue(result, "findings_unfiled_count") == 0
	ghAgentOK := gitopsGHAgentGateOK(result)
	closeoutOK := false
	if reviewOK && validationOK && filingOK && ghAgentOK {
		closeout, err := gitopsCloseoutFixedIssues(ctx, runDir, opts.TicketRepo, result)
		if err != nil {
			return nil, err
		}
		mergeMaps(result, closeout)
		closeoutOK = stringValue(closeout, "issue_closeout_status") == "closed"
	} else {
		result["issue_closeout_status"] = "skipped"
		result["issue_closeout_summary"] = "Issue close-out skipped because one or more autonomous gates failed."
		result["issue_closeout_count"] = 0
	}

	status := "autonomous_fix_invalid"
	if reviewOK && validationOK && filingOK && ghAgentOK && closeoutOK {
		status = "autonomous_fix_valid"
	}
	result["status"] = status
	result["autonomous_fix_status"] = status
	result["autonomous_gate_summary"] = fmt.Sprintf("filing=%s, gh_agent=%s, review=%s, validation=%s",
		passFail(filingOK), passFail(ghAgentOK), passFail(reviewOK), passFail(validationOK))
	result["filing_status"] = "findings_filed"
	result["review_status"] = firstNonBlank(stringValue(review, "review_status"), stringValue(review, "status"))
	result["review_summary"] = stringValue(review, "summary")
	result["review_passed_count"] = firstNonZero(intValue(review, "review_passed_count"), intValue(review, "passed"))
	result["review_failed_count"] = reviewFailed
	result["review_warning_count"] = firstNonZero(intValue(review, "review_warning_count"), intValue(review, "warnings"))
	result["review_total_count"] = firstNonZero(intValue(review, "review_total_count"), intValue(review, "total"))
	result["review_backlog_summary"] = stringValue(review, "review_backlog_summary")
	result["validation_status"] = stringValue(validation, "status")
	result["validation_errors"] = intValue(validation, "errors")
	result["validation_warnings"] = intValue(validation, "warnings")
	result["validation_issue_summary"] = stringValue(validation, "validation_issue_summary")
	result["validation_issues"] = validation["issues"]

	storySummary, _ = productJourneyJSON(ctx, "--summarize-run", "--run-dir", runDir)
	mergeMaps(result, storySummary)
	reportPath, err = writeGitopsAutonomousReport(runDir, result, review, validation)
	if err != nil {
		return nil, err
	}
	result["autonomous_fix_report_path"] = reportPath
	return result, nil
}

func gitopsGHAgentGateOK(result map[string]any) bool {
	return stringValue(result, "gh_agent_enqueue_status") == "queued" &&
		intValue(result, "gh_agent_enqueued_count") > 0 &&
		stringValue(result, "gh_agent_drain_status") == "drained" &&
		intValue(result, "gh_agent_failed_count") == 0 &&
		intValue(result, "gh_agent_active_count") == 0 &&
		intValue(result, "gh_agent_done_count") >= intValue(result, "gh_agent_enqueued_count") &&
		intValue(result, "gh_agent_missing_evidence_count") == 0 &&
		intValue(result, "gh_agent_missing_verify_count") == 0 &&
		intValue(result, "gh_agent_missing_run_url_count") == 0
}

func gitopsCloseoutFixedIssues(ctx context.Context, runDir, ticketRepo string, status map[string]any) (map[string]any, error) {
	findingsPath := filepath.Join(runDir, "findings.json")
	findings, err := gitopsReadJSONFile(findingsPath)
	if err != nil {
		return nil, err
	}
	jobsByOrigin := make(map[string]map[string]any)
	for _, job := range mapSliceValue(status, "gh_agent_drained_jobs") {
		if stringValue(job, "state") != "done" {
			continue
		}
		if origin := stringValue(job, "origin_ref"); origin != "" {
			jobsByOrigin[origin] = job
		}
	}
	var closed []map[string]any
	var failed []string
	closeoutRev := gitShortRevCWD()
	for _, item := range gitopsFindingsItems(findings) {
		if stringValue(item, "kind") != "issue" || stringValue(item, "origin") == "seeded" {
			continue
		}
		repo, number, kind, issueURL := gitopsIssueRef(item, ticketRepo)
		if repo == "" || number == "" || kind != "issue" {
			continue
		}
		origin := "github:" + repo + "/" + kind + "/" + number
		job := jobsByOrigin[origin]
		if job == nil {
			failed = append(failed, fmt.Sprintf("%s missing done gh-agent job", issueURL))
			continue
		}
		body := gitopsFixedInCommentBody(runDir, item, job, closeoutRev)
		comment, err := host.GitHubTicketHandler(ctx, map[string]any{
			"op":   "comment",
			"repo": repo,
			"id":   number,
			"body": body,
		})
		if err != nil {
			return nil, err
		}
		if comment.Error != "" {
			failed = append(failed, fmt.Sprintf("%s comment failed: %s", issueURL, comment.Error))
			continue
		}
		transition, err := host.GitHubTicketHandler(ctx, map[string]any{
			"op":   "transition",
			"repo": repo,
			"id":   number,
			"to":   "closed",
		})
		if err != nil {
			return nil, err
		}
		if transition.Error != "" {
			failed = append(failed, fmt.Sprintf("%s close failed: %s", issueURL, transition.Error))
			continue
		}
		commentURL := stringValue(comment.Data, "url")
		closed = append(closed, map[string]any{
			"finding_id":  stringValue(item, "id"),
			"issue_url":   issueURL,
			"repo":        repo,
			"number":      number,
			"comment_url": commentURL,
			"run_url":     stringValue(job, "run_url"),
			"job_id":      stringValue(job, "job_id"),
			"closed":      true,
		})
		issue := mapValue(item, "github_issue")
		if issue == nil {
			issue = map[string]any{}
			item["github_issue"] = issue
		}
		issue["repo"] = repo
		issue["number"] = number
		issue["url"] = firstNonBlank(issueURL, "https://github.com/"+repo+"/issues/"+number)
		issue["state"] = "closed"
		issue["status"] = "closed"
		issue["closed_by"] = "kitsoki gitops autonomous-fix"
		issue["fixed_by"] = closeoutRev
		issue["closeout_comment_url"] = commentURL
		issue["comments"] = append(mapSliceValue(issue, "comments"), map[string]any{
			"body": body,
			"url":  commentURL,
		})
		item["status"] = "fixed"
		item["fixed_by"] = closeoutRev
		item["fixed_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	closeoutStatus := "closed"
	if len(closed) == 0 || len(failed) > 0 {
		closeoutStatus = "failed"
	}
	summary := fmt.Sprintf("Closed %d fixed GitHub issue(s).", len(closed))
	if len(failed) > 0 {
		summary = fmt.Sprintf("%s %d close-out failure(s): %s", summary, len(failed), strings.Join(firstStrings(failed, 3), "; "))
	}
	findings["issue_closeout"] = map[string]any{
		"updated_at": time.Now().UTC().Format(time.RFC3339),
		"status":     closeoutStatus,
		"count":      len(closed),
		"summary":    summary,
		"items":      closed,
		"errors":     failed,
	}
	if err := gitopsWriteJSONFile(findingsPath, findings); err != nil {
		return nil, err
	}
	return map[string]any{
		"issue_closeout_status":  closeoutStatus,
		"issue_closeout_count":   len(closed),
		"issue_closeout_summary": summary,
		"closed_issue_urls":      closeoutIssueURLs(closed),
		"issue_closeouts":        closed,
		"issue_closeout_errors":  failed,
	}, nil
}

func gitopsFixedInCommentBody(runDir string, finding, job map[string]any, closeoutRev string) string {
	issue := mapValue(finding, "github_issue")
	issueURL := stringValue(issue, "url")
	runURL := stringValue(job, "run_url")
	evidence := gitopsJobEvidenceLinks(job)
	verify := gitopsJobIndependentVerifyLinks(job)
	var lines []string
	lines = append(lines,
		"Fixed by the Kitsoki autonomous bugfix pipeline.",
		"",
		fmt.Sprintf("- Finding: `%s` - %s", firstNonBlank(stringValue(finding, "id"), "finding"), stringValue(finding, "title")),
		fmt.Sprintf("- Run: %s", firstNonBlank(runURL, "(missing run URL)")),
		fmt.Sprintf("- Close-out rev: `%s`", closeoutRev),
	)
	if len(evidence) > 0 {
		lines = append(lines, "- Evidence:")
		for _, link := range evidence {
			lines = append(lines, "  - "+link)
		}
	}
	if len(verify) > 0 {
		lines = append(lines, "- Independent verification:")
		for _, link := range verify {
			lines = append(lines, "  - "+link)
		}
	}
	lines = append(lines,
		"",
		"<!-- kitsoki-fixed-in",
		"issue: "+issueURL,
		"run: "+runURL,
		"job: "+stringValue(job, "job_id"),
		"closeout_rev: "+closeoutRev,
		"evidence:",
	)
	for _, link := range evidence {
		lines = append(lines, "  - "+link)
	}
	lines = append(lines, "verified:")
	for _, link := range verify {
		lines = append(lines, "  - "+link)
	}
	lines = append(lines, "-->")
	return strings.Join(lines, "\n")
}

func gitopsJobIndependentVerifyLinks(job map[string]any) []string {
	var links []string
	for _, asset := range mapSliceValue(job, "assets") {
		name := strings.ToLower(firstNonBlank(stringValue(asset, "name"), stringValue(asset, "path"), stringValue(asset, "url")))
		if strings.Contains(name, "independent") && strings.Contains(name, "verify") {
			for _, key := range []string{"url", "href", "path"} {
				if value := stringValue(asset, key); value != "" {
					links = append(links, value)
					break
				}
			}
		}
	}
	return links
}

func closeoutIssueURLs(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if url := stringValue(item, "issue_url"); url != "" {
			out = append(out, url)
		}
	}
	return out
}

func runGitopsAutonomousFixViaRunner(ctx context.Context, opts gitopsAutonomousFixOptions) (map[string]any, error) {
	args := []string{
		"tools/product-journey/run.py",
		"--autonomous-fix-loop",
		"--run-dir", opts.RunDir,
		"--ticket-repo", opts.TicketRepo,
		"--gh-agent-db", opts.AgentDB,
		"--gh-agent-story", firstNonBlank(opts.AgentStory, "stories/bugfix"),
		"--gh-agent-public-base-url", opts.PublicBaseURL,
		"--gh-agent-project-root", opts.ProjectRoot,
		"--gh-agent-incident-repo", opts.IncidentRepo,
		"--gh-agent-asset-dir", opts.AssetDir,
		"--gh-agent-comment-mode", firstNonBlank(opts.CommentMode, "none"),
		"--report-invalid-autonomous-fix",
		"--json-output",
	}
	py := exec.CommandContext(ctx, "python3", args...)
	py.Dir = "."
	py.Env = os.Environ()
	out, err := py.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("product-journey autonomous-fix test fallback failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("product-journey autonomous-fix test fallback printed invalid JSON: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return result, nil
}

func gitopsFilingResult(runDir, ticketRepo string, filed host.FindingsFilingResult) map[string]any {
	filedURLs := make([]string, 0, len(filed.Outcomes))
	for _, out := range filed.Outcomes {
		if out.IssueURL != "" {
			filedURLs = append(filedURLs, out.IssueURL)
		}
	}
	unfiled := 0
	for _, item := range gitopsCredibleFindings(runDir) {
		if stringValue(mapValue(item, "github_issue"), "url") == "" {
			unfiled++
		}
	}
	summary := fmt.Sprintf("Filed findings to %s: %d filed, %d already filed, %d failed; %d credible finding(s) remain unfiled.",
		ticketRepo, filed.Filed, filed.Skipped, filed.Failed, unfiled)
	return map[string]any{
		"status":                 filed.Status,
		"run_dir":                runDir,
		"ticket_repo":            ticketRepo,
		"dry_run":                "no",
		"findings_filed_count":   filed.Filed,
		"findings_skipped_count": filed.Skipped,
		"findings_failed_count":  filed.Failed,
		"findings_unfiled_count": unfiled,
		"filed_issue_urls":       filedURLs,
		"filed_issue_summary":    strings.Join(firstStrings(filedURLs, 5), "; "),
		"filing_summary":         summary,
		"outcomes":               filed.Outcomes,
		"deck_path":              filepath.Join(runDir, "deck.slidey.json"),
		"execution_plan_path":    filepath.Join(runDir, "execution-plan.md"),
		"driver_plan_path":       filepath.Join(runDir, "driver-plan.md"),
		"driver_journal_path":    filepath.Join(runDir, "driver-journal.md"),
		"agent_brief_path":       filepath.Join(runDir, "agent-brief.md"),
		"driver_handoff_path":    filepath.Join(runDir, "driver-handoff.md"),
		"media_manifest_path":    filepath.Join(runDir, "media-manifest.json"),
		"scenario_outcomes_path": filepath.Join(runDir, "scenario-outcomes.md"),
	}
}

func gitopsEnqueueFixes(ctx context.Context, runDir, dbPath, fallbackRepo, story string) (map[string]any, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("gitops autonomous-fix: open db %q: %w", dbPath, err)
	}
	defer db.Close()
	store, err := jobs.NewGHJobStore(db)
	if err != nil {
		return nil, err
	}
	findingsPath := filepath.Join(runDir, "findings.json")
	findings, err := gitopsReadJSONFile(findingsPath)
	if err != nil {
		return nil, err
	}
	var jobsOut []map[string]any
	var claims []map[string]any
	skipped := 0
	now := time.Now().UTC().Format(time.RFC3339)
	for _, item := range gitopsCredibleFindingsFrom(findings) {
		repo, number, kind, issueURL := gitopsIssueRef(item, fallbackRepo)
		if repo == "" || number == "" {
			skipped++
			continue
		}
		job, created, err := store.Enqueue(ctx, jobs.GHMention{
			OriginRef:    "github:" + repo + "/" + kind + "/" + number,
			Repo:         repo,
			ObjectKind:   kind,
			ObjectNumber: number,
		}, firstNonBlank(story, "stories/bugfix"))
		if err != nil {
			return nil, err
		}
		issue := mapValue(item, "github_issue")
		if issue == nil {
			issue = map[string]any{}
			item["github_issue"] = issue
		}
		claimURL := stringValue(issue, "claim_comment_url")
		if claimURL == "" {
			body := gitopsClaimCommentBody(item, job, issueURL)
			claim, err := host.GitHubTicketHandler(ctx, map[string]any{
				"op":   "comment",
				"repo": repo,
				"id":   number,
				"body": body,
			})
			if err != nil {
				return nil, err
			}
			if claim.Error != "" {
				return nil, fmt.Errorf("gitops autonomous-fix: claim %s: %s", firstNonBlank(issueURL, repo+"#"+number), claim.Error)
			}
			claimURL = stringValue(claim.Data, "url")
			issue["claim_comment_url"] = claimURL
			issue["claim_job_id"] = job.JobID
			issue["claimed_by"] = "kitsoki gitops autonomous-fix"
			issue["claimed_at"] = now
			issue["comments"] = append(mapSliceValue(issue, "comments"), map[string]any{
				"body": body,
				"url":  claimURL,
			})
		}
		jobOut := map[string]any{
			"status":        "queued",
			"created":       created,
			"job_id":        job.JobID,
			"origin_ref":    job.OriginRef,
			"repo":          job.Repo,
			"object_kind":   job.ObjectKind,
			"object_number": job.ObjectNumber,
			"story":         job.Story,
			"state":         job.State,
			"issue_url":     issueURL,
			"finding_id":    stringValue(item, "id"),
			"claim_url":     claimURL,
		}
		jobsOut = append(jobsOut, jobOut)
		claims = append(claims, map[string]any{
			"finding_id":  stringValue(item, "id"),
			"issue_url":   issueURL,
			"repo":        repo,
			"number":      number,
			"job_id":      job.JobID,
			"comment_url": claimURL,
		})
	}
	if len(jobsOut) > 0 {
		if err := gitopsWriteJSONFile(findingsPath, findings); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"gh_agent_enqueue_status": "queued",
		"gh_agent_enqueued_count": len(jobsOut),
		"gh_agent_skipped_count":  skipped,
		"gh_agent_job_summary":    originSummary(jobsOut),
		"gh_agent_jobs":           jobsOut,
		"gh_agent_claim_status":   "claimed",
		"gh_agent_claim_count":    len(claims),
		"gh_agent_claims":         claims,
	}, nil
}

func gitopsDrainFixes(ctx context.Context, dbPath, repo, publicBaseURL, projectRoot, incidentRepo, assetDir, commentMode string) (map[string]any, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("gitops autonomous-fix: open db %q: %w", dbPath, err)
	}
	defer db.Close()
	store, err := jobs.NewGHJobStore(db)
	if err != nil {
		return nil, err
	}
	store.DataDir = ghAgentDrainAssetDir(dbPath, assetDir)
	opts := ghAgentServeOptions{
		Repo:          repo,
		PublicBaseURL: publicBaseURL,
		Worker:        "gh-agent-1",
		IncidentRepo:  incidentRepo,
		ProjectRoot:   projectRoot,
		CommentMode:   commentMode,
	}
	drained, err := drainQueuedGHAgentJobs(ctx, store, opts)
	if err != nil {
		return nil, err
	}
	raw := ghAgentDrainResult(ctx, store, drained, publicBaseURL)
	jobsOut := mapSliceValue(raw, "jobs")
	return map[string]any{
		"gh_agent_drain_status":  stringValue(raw, "status"),
		"gh_agent_drained_count": intValue(raw, "drained_count"),
		"gh_agent_done_count":    intValue(raw, "done_count"),
		"gh_agent_failed_count":  intValue(raw, "failed_count"),
		"gh_agent_active_count":  intValue(raw, "active_count"),
		"gh_agent_run_summary":   runSummary(jobsOut),
		"gh_agent_drained_jobs":  jobsOut,
	}, nil
}

func productJourneyJSON(ctx context.Context, args ...string) (map[string]any, error) {
	all := append([]string{"tools/product-journey/run.py"}, args...)
	all = append(all, "--json-output")
	py := exec.CommandContext(ctx, "python3", all...)
	py.Dir = "."
	py.Env = os.Environ()
	out, err := py.CombinedOutput()
	var result map[string]any
	if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
		if err != nil {
			return nil, fmt.Errorf("product-journey %s failed: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil, fmt.Errorf("product-journey %s printed invalid JSON: %w\n%s", strings.Join(args, " "), jsonErr, strings.TrimSpace(string(out)))
	}
	return result, nil
}

func gitopsRecordGHAgent(runDir string, status map[string]any) error {
	path := filepath.Join(runDir, "findings.json")
	findings, err := gitopsReadJSONFile(path)
	if err != nil {
		return err
	}
	findings["gh_agent"] = map[string]any{
		"updated_at":     time.Now().UTC().Format(time.RFC3339),
		"enqueue_status": stringValue(status, "gh_agent_enqueue_status"),
		"enqueued_count": intValue(status, "gh_agent_enqueued_count"),
		"skipped_count":  intValue(status, "gh_agent_skipped_count"),
		"job_summary":    stringValue(status, "gh_agent_job_summary"),
		"jobs":           status["gh_agent_jobs"],
		"claim_status":   stringValue(status, "gh_agent_claim_status"),
		"claim_count":    intValue(status, "gh_agent_claim_count"),
		"claims":         status["gh_agent_claims"],
		"drain_status":   stringValue(status, "gh_agent_drain_status"),
		"drained_count":  intValue(status, "gh_agent_drained_count"),
		"done_count":     intValue(status, "gh_agent_done_count"),
		"failed_count":   intValue(status, "gh_agent_failed_count"),
		"active_count":   intValue(status, "gh_agent_active_count"),
		"run_summary":    stringValue(status, "gh_agent_run_summary"),
		"drained_jobs":   status["gh_agent_drained_jobs"],
	}
	return gitopsWriteJSONFile(path, findings)
}

func writeGitopsAutonomousReport(runDir string, status, review, validation map[string]any) (string, error) {
	findings, _ := gitopsReadJSONFile(filepath.Join(runDir, "findings.json"))
	var lines []string
	lines = append(lines,
		"# Autonomous Fix Report",
		"",
		fmt.Sprintf("- Run: `%s`", filepath.Base(runDir)),
		fmt.Sprintf("- Ticket repo: `%s`", stringValue(status, "ticket_repo")),
		fmt.Sprintf("- Status: `%s`", firstNonBlank(stringValue(status, "autonomous_fix_status"), stringValue(status, "status"))),
		fmt.Sprintf("- Gates: %s", firstNonBlank(stringValue(status, "autonomous_gate_summary"), "(not evaluated)")),
		fmt.Sprintf("- Issue close-out: `%s` (%d closed)", stringValue(status, "issue_closeout_status"), intValue(status, "issue_closeout_count")),
		"",
		"## Filed Issues",
		"",
	)
	issueLines := 0
	for _, item := range gitopsFindingsItems(findings) {
		issue := mapValue(item, "github_issue")
		if issueURL := stringValue(issue, "url"); issueURL != "" {
			lines = append(lines, fmt.Sprintf("- `%s`: %s", firstNonBlank(stringValue(item, "id"), stringValue(item, "title"), "finding"), issueURL))
			issueLines++
		}
	}
	if issueLines == 0 {
		lines = append(lines, "- (none)")
	}
	lines = append(lines,
		"",
		"## GH-agent Runs",
		"",
		fmt.Sprintf("- Queue: `%s` (enqueued %d, skipped %d)", stringValue(status, "gh_agent_enqueue_status"), intValue(status, "gh_agent_enqueued_count"), intValue(status, "gh_agent_skipped_count")),
		fmt.Sprintf("- Claims: `%s` (%d claimed)", stringValue(status, "gh_agent_claim_status"), intValue(status, "gh_agent_claim_count")),
		fmt.Sprintf("- Drain: `%s` (drained %d, done %d, failed %d, active %d)", stringValue(status, "gh_agent_drain_status"), intValue(status, "gh_agent_drained_count"), intValue(status, "gh_agent_done_count"), intValue(status, "gh_agent_failed_count"), intValue(status, "gh_agent_active_count")),
		"",
	)
	jobsOut := mapSliceValue(status, "gh_agent_drained_jobs")
	if len(jobsOut) == 0 {
		lines = append(lines, "- (none)")
	} else {
		for _, job := range jobsOut {
			lines = append(lines,
				fmt.Sprintf("### %s", firstNonBlank(stringValue(job, "origin_ref"), stringValue(job, "job_id"), "unknown")),
				"",
				fmt.Sprintf("- State: `%s`", stringValue(job, "state")),
				fmt.Sprintf("- Run URL: %s", firstNonBlank(stringValue(job, "run_url"), "(missing)")),
			)
			if incident := stringValue(job, "incident_url"); incident != "" {
				lines = append(lines, "- Incident: "+incident)
			}
			if errMsg := stringValue(job, "err_msg"); errMsg != "" {
				lines = append(lines, "- Error: "+errMsg)
			}
			lines = append(lines, "- Evidence:")
			links := gitopsJobEvidenceLinks(job)
			if len(links) == 0 {
				lines = append(lines, "  - (missing)")
			} else {
				for _, link := range links {
					lines = append(lines, "  - "+link)
				}
			}
			lines = append(lines, "")
		}
	}
	lines = append(lines,
		"## Issue Close-out",
		"",
		fmt.Sprintf("- Status: `%s`", stringValue(status, "issue_closeout_status")),
		fmt.Sprintf("- Summary: %s", firstNonBlank(stringValue(status, "issue_closeout_summary"), "(not run)")),
		"",
	)
	for _, item := range mapSliceValue(status, "issue_closeouts") {
		lines = append(lines, fmt.Sprintf("- %s — %s", stringValue(item, "issue_url"), stringValue(item, "comment_url")))
	}
	if intValue(status, "issue_closeout_count") == 0 {
		lines = append(lines, "- (none)")
	}
	if errors := stringSliceValue(status, "issue_closeout_errors"); len(errors) > 0 {
		lines = append(lines, "", "Errors:")
		for _, item := range errors {
			lines = append(lines, "- "+item)
		}
	}
	lines = append(lines,
		"",
		"## Review",
		"",
		fmt.Sprintf("- Status: `%s`", firstNonBlank(stringValue(review, "review_status"), stringValue(review, "status"), stringValue(status, "review_status"))),
		fmt.Sprintf("- Summary: %s", firstNonBlank(stringValue(review, "summary"), stringValue(status, "review_summary"))),
		"",
		"## Validation",
		"",
		fmt.Sprintf("- Status: `%s`", firstNonBlank(stringValue(validation, "status"), stringValue(status, "validation_status"))),
		fmt.Sprintf("- Issues: %s", firstNonBlank(stringValue(validation, "validation_issue_summary"), stringValue(status, "validation_issue_summary"), "(none)")),
		"",
	)
	path := filepath.Join(runDir, "autonomous-fix-report.md")
	return path, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func gitopsCredibleFindings(runDir string) []map[string]any {
	findings, err := gitopsReadJSONFile(filepath.Join(runDir, "findings.json"))
	if err != nil {
		return nil
	}
	return gitopsCredibleFindingsFrom(findings)
}

func gitopsCredibleFindingsFrom(findings map[string]any) []map[string]any {
	var out []map[string]any
	for _, item := range gitopsFindingsItems(findings) {
		if stringValue(item, "kind") != "issue" || stringValue(item, "status") == "blocked" || stringValue(item, "origin") == "seeded" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func gitopsClaimCommentBody(finding map[string]any, job *jobs.GHJob, issueURL string) string {
	lines := []string{
		"Claimed by the Kitsoki autonomous bugfix pipeline.",
		"",
		fmt.Sprintf("- Finding: `%s` - %s", firstNonBlank(stringValue(finding, "id"), "finding"), stringValue(finding, "title")),
		fmt.Sprintf("- Job: `%s`", job.JobID),
		fmt.Sprintf("- Story: `%s`", job.Story),
		"",
		"<!-- kitsoki-autofix-claim",
		"issue: " + issueURL,
		"job: " + job.JobID,
		"origin: " + job.OriginRef,
		"finding: " + stringValue(finding, "id"),
		"-->",
	}
	return strings.Join(lines, "\n")
}

func gitopsFindingsItems(findings map[string]any) []map[string]any {
	rows, _ := findings["items"].([]any)
	var out []map[string]any
	for _, row := range rows {
		if item, ok := row.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

func gitopsIssueRef(item map[string]any, fallbackRepo string) (string, string, string, string) {
	issue := mapValue(item, "github_issue")
	issueURL := stringValue(issue, "url")
	repo := firstNonBlank(stringValue(issue, "repo"), fallbackRepo)
	number := stringValue(issue, "number")
	kind := "issue"
	if (repo == "" || number == "") && issueURL != "" {
		if parsed, err := url.Parse(issueURL); err == nil && strings.EqualFold(parsed.Host, "github.com") {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) >= 4 && (parts[2] == "issues" || parts[2] == "pull") {
				repo = firstNonBlank(repo, parts[0]+"/"+parts[1])
				number = firstNonBlank(number, parts[3])
				if parts[2] == "pull" {
					kind = "pr"
				}
			}
		}
	}
	return repo, number, kind, issueURL
}

func gitopsReadJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(data, &out)
}

func gitopsWriteJSONFile(path string, value map[string]any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func mapValue(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	value, _ := m[key].(map[string]any)
	return value
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return fmt.Sprintf("%g", v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func intValue(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func stringSliceValue(m map[string]any, key string) []string {
	raw, ok := m[key].([]string)
	if ok {
		return raw
	}
	rows, _ := m[key].([]any)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if s, ok := row.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func mapSliceValue(m map[string]any, key string) []map[string]any {
	if m == nil {
		return nil
	}
	if rows, ok := m[key].([]map[string]any); ok {
		return rows
	}
	rawRows, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(rawRows))
	for _, raw := range rawRows {
		if row, ok := raw.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func mergeMaps(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

func passFail(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstStrings(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

func originSummary(jobs []map[string]any) string {
	var parts []string
	for _, job := range firstMaps(jobs, 5) {
		parts = append(parts, stringValue(job, "origin_ref"))
	}
	return strings.Join(parts, "; ")
}

func runSummary(jobs []map[string]any) string {
	var parts []string
	for _, job := range firstMaps(jobs, 5) {
		part := stringValue(job, "origin_ref") + "=" + stringValue(job, "state")
		if runURL := stringValue(job, "run_url"); runURL != "" {
			part += " " + runURL
		}
		parts = append(parts, strings.TrimSpace(part))
	}
	return strings.Join(parts, "; ")
}

func firstMaps(values []map[string]any, n int) []map[string]any {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

func gitopsJobEvidenceLinks(job map[string]any) []string {
	var links []string
	for _, asset := range mapSliceValue(job, "assets") {
		for _, key := range []string{"url", "href", "path"} {
			if value := stringValue(asset, key); value != "" {
				links = append(links, value)
				break
			}
		}
	}
	return links
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
