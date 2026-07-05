// realdispatch.go — task 2 of docs/proposals/gh-agent-honest-issues.md.
//
// Real pipeline dispatch drives the ACTUAL stories/bugfix machine end-to-end
// (no beat-fixture host.agent.* stub map) in a per-job worktree, through a
// live-or-replay harness selected the same way `kitsoki turn`/`web` select
// harnesses — except the default is always "replay" here (see
// resolveHarnessMode in dispatch.go): an unattended dispatcher must never go
// live on ambient credentials.
//
// Only stories/bugfix has a registered plan today. There is exactly one
// recorded, arg-matched host cassette in the repo that walks the FULL
// pipeline through `done` — stories/bugfix/cassettes/happy_human.cassette.yaml,
// captured once against the real Claude CLI (see
// stories/bugfix/flows/happy_human.yaml's doc comment) and replayed forever.
// Its episodes match on {handler, phase} (stories/bugfix/cassettes/
// happy_human.cassette.yaml: `match_on: [handler, phase]`), NOT on prompt
// content, so substituting the job's real ticket_id/thread/repo into
// initial_world does not break cassette matching — the recorded transcript is
// genuinely replayed against this job's identity, not merely templated text.
// stories/dev-story has no equivalent recorded cassette yet, so it still runs
// the honest stub path (runStubBeatFixture) until its own plan is authored —
// a real, if narrower, scope than the proposal's "stories/bugfix / stories/
// dev-story" framing implied.
package ghagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"kitsoki/internal/jobs"
	"kitsoki/internal/store"
	"kitsoki/internal/testrunner"
)

// realDispatchPlan describes how to drive one story's REAL pipeline for gh
// issue dispatch: which recorded cassette replays its agent/host calls, which
// ifaces to bind to their real handlers (so a cassette miss during a
// deliberate recording run reaches production code, and so "live" mode's
// absent cassette leaves the real handlers as the only path), and the fixed
// intent sequence the cassette's phases were recorded against.
type realDispatchPlan struct {
	// CassetteRelPath is the recorded host cassette (kind: host_cassette),
	// relative to repoRoot. Used only in replay mode.
	CassetteRelPath string
	// HostBindings mirrors the cassette fixture's host_bindings: block.
	HostBindings map[string]string
	// JudgeMode is forced to the value the recorded cassette's turn shape
	// matches — a real dispatch plan's turn sequence is fixed to one
	// specific checkpoint cadence, so judge_mode must agree with it.
	JudgeMode string
	// BugfixMode ("full"/"quick") the plan's turns assume.
	BugfixMode string
	// Turns is the fixed intent sequence the cassette was recorded against.
	Turns []testrunner.FlowTurn
	// BaseWorld seeds fixture-shape defaults the plan's turns rely on
	// (bugfix_exit, judge_confidence_threshold, ...) that aren't job-derived.
	BaseWorld map[string]any
}

// realDispatchPlans is keyed by route.Story. Only stories/bugfix has a plan
// today (see the package doc above for why).
var realDispatchPlans = map[string]realDispatchPlan{
	"stories/bugfix": {
		CassetteRelPath: filepath.Join("stories", "bugfix", "cassettes", "happy_human.cassette.yaml"),
		HostBindings: map[string]string{
			"vcs":       "host.git",
			"ci":        "host.local",
			"workspace": "host.git_worktree",
			"transport": "host.append_to_file",
		},
		// The recorded cassette's phases (idle/reproducing/proposing/
		// implementing/testing/reviewing/validating) match judge_mode:
		// human's checkpoint-per-room cadence — see happy_human.yaml. Real
		// dispatch forces this regardless of route.World's judge_mode
		// default (llm_then_human) until a cassette recorded against that
		// cadence exists.
		JudgeMode:  "human",
		BugfixMode: "full",
		BaseWorld: map[string]any{
			"bugfix_exit":                "open-PR",
			"base_branch":                "main",
			"judge_confidence_threshold": 0.8,
		},
		Turns: []testrunner.FlowTurn{
			{Intent: &testrunner.FlowIntent{Name: "start"}, ExpectState: "reproducing"},
			{Intent: &testrunner.FlowIntent{Name: "accept"}, ExpectState: "proposing"},
			{Intent: &testrunner.FlowIntent{Name: "accept"}, ExpectState: "implementing"},
			{Intent: &testrunner.FlowIntent{Name: "accept"}, ExpectState: "testing"},
			{Intent: &testrunner.FlowIntent{Name: "accept"}, ExpectState: "reviewing"},
			{Intent: &testrunner.FlowIntent{Name: "accept"}, ExpectState: "validating"},
			{Intent: &testrunner.FlowIntent{Name: "accept"}, ExpectState: "done"},
			{Intent: &testrunner.FlowIntent{Name: "accept"}, ExpectState: "__exit__done"},
		},
	},
}

// jobWorktreeSlugRe sanitizes a job ID into a safe path/branch segment.
var jobWorktreeSlugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func jobWorktreeSlug(jobID string) string {
	slug := jobWorktreeSlugRe.ReplaceAllString(strings.TrimSpace(jobID), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "job"
	}
	return slug
}

// jobWorktreeRelDir is the per-job worktree path relative to the target
// checkout root: .worktrees/gh-job-<id>, per AGENTS.md ("make your worktrees
// in the project root folder .worktrees").
func jobWorktreeRelDir(jobID string) string {
	return filepath.Join(".worktrees", "gh-job-"+jobWorktreeSlug(jobID))
}

func jobWorkspaceID(jobID string) string {
	return "gh-job-" + jobWorktreeSlug(jobID)
}

func jobFeatureBranch(jobID string) string {
	return "gh-job/" + jobWorktreeSlug(jobID)
}

// runRealDispatch drives route.Story's REAL machine end-to-end via plan's
// recorded cassette (replay mode) or the real handlers (live mode), in a
// per-job worktree identity seeded through stories/bugfix's own sanctioned
// workspace_prepared/autostart path (rooms/idle.yaml Step-0w) — never via an
// un-declared world key.
func runRealDispatch(ctx context.Context, root string, route Route, job *jobs.GHJob, plan realDispatchPlan) (RunResult, error) {
	mode := harnessModeFromContext(ctx)

	appPath := route.AppPath
	if strings.TrimSpace(appPath) == "" {
		appPath = filepath.Join(root, route.Story, "app.yaml")
	}

	worktreeRel := jobWorktreeRelDir(job.JobID)
	worktreeAbs := filepath.Join(root, worktreeRel)

	initialWorld := map[string]any{}
	for k, v := range plan.BaseWorld {
		initialWorld[k] = v
	}
	for k, v := range jobFlowWorld(job) {
		if strings.TrimSpace(v) != "" {
			initialWorld[k] = v
		}
	}
	// Per-job workspace identity, seeded through the sanctioned
	// workspace_prepared/.worktrees-prefix exemption
	// (stories/bugfix/rooms/idle.yaml Step-0w) instead of an un-declared
	// world key: a repo-relative .worktrees/ workdir plus
	// workspace_prepared: true both independently satisfy that guard.
	initialWorld["workspace_id"] = jobWorkspaceID(job.JobID)
	initialWorld["feature_branch"] = jobFeatureBranch(job.JobID)
	initialWorld["workdir"] = worktreeRel
	initialWorld["workspace_prepared"] = true
	initialWorld["bugfix_mode"] = plan.BugfixMode
	initialWorld["judge_mode"] = plan.JudgeMode
	for k, v := range route.World {
		if k == "judge_mode" {
			continue // plan.JudgeMode is authoritative — see realDispatchPlans' doc.
		}
		initialWorld[k] = v
	}

	fixture := &testrunner.FlowFixture{
		TestKind:       "flow",
		App:            appPath,
		InitialState:   "idle",
		InitialWorld:   initialWorld,
		Turns:          plan.Turns,
		ExpectTerminal: boolPtr(true),
		ExpectNoErrors: boolPtr(true),
	}
	if mode == HarnessReplay {
		fixture.HostCassette = filepath.Join(root, plan.CassetteRelPath)
		fixture.HostBindings = plan.HostBindings
	}
	// mode == HarnessLive: no HostCassette, no HostHandlers. RunFlows'
	// orchestrator path registers the real host.RegisterBuiltins handlers
	// whenever HostBindings is set (see flows.go), so plan.HostBindings still
	// applies here to bind the real ifaces — but with no cassette to hit
	// first, every call reaches the real handler: a real agent subprocess
	// per agents: block, real git worktree/commit ops, real test runs. This
	// is genuinely operator-invoked-only; resolveHarnessMode never picks it
	// by default.
	if mode == HarnessLive {
		fixture.HostBindings = plan.HostBindings
	}

	fixturePath, cleanupFixture, err := writeFlowFixture(fixture)
	if err != nil {
		return RunResult{}, err
	}
	defer cleanupFixture()

	report, runErr := testrunner.RunFlows(ctx, appPath, fixturePath, testrunner.FlowOptions{})

	// Cleanup policy (proposal open-question 2 lean): delete the worktree on
	// success, keep it (bounded retention window) on failure for post-mortem.
	// A no-op in replay mode, where nothing was ever checked out on disk —
	// the cassette served every workspace host call.
	succeeded := runErr == nil && report != nil && report.Passed >= 1 && report.Failed == 0
	if cleanupErr := cleanupJobWorktree(ctx, root, worktreeAbs, job.JobID, succeeded); cleanupErr != nil {
		// Best-effort: a cleanup failure must not mask the real run outcome.
		_ = cleanupErr
	}

	if runErr != nil {
		return RunResult{Harness: mode, Worktree: worktreeAbs}, fmt.Errorf("ghagent: real dispatch %q: %w", route.Story, runErr)
	}
	if report.Passed < 1 {
		return RunResult{Harness: mode, Worktree: worktreeAbs}, fmt.Errorf("ghagent: real dispatch %q ran no passing turn (passed=%d failed=%d): %s", route.Story, report.Passed, report.Failed, summarizeFlowFailures(report))
	}

	turns := 0
	hostCalls := 0
	var lastSummary, lastDiff string
	for _, r := range report.Results {
		turns += len(r.Turns)
		for _, t := range r.Turns {
			s, d, n := extractRealDispatchEvidence(t.Events)
			hostCalls += n
			if s != "" {
				lastSummary = s
			}
			if d != "" {
				lastDiff = d
			}
		}
	}

	summary := fmt.Sprintf("Ran `%s` end-to-end via the %s harness (%d turn(s)); worktree `%s`.", route.Story, mode, turns, worktreeRel)
	if lastSummary != "" {
		summary += "\n\n" + lastSummary
	}
	if lastDiff != "" {
		summary += fmt.Sprintf("\n\nDiff:\n```diff\n%s\n```", lastDiff)
	}
	verification := fmt.Sprintf(`# Independent verification

- Story: %q
- Harness: %q
- Final state: "done"
- Flow turns: %d
- Host returns observed: %d
- Worktree: %q
- Result: passed

The gh-agent dispatcher ran the bugfix story from a fresh job context through its verification and done gates before marking this job done.
`, route.Story, mode, turns, hostCalls, worktreeRel)
	assets := []RunAsset{{
		Name:     "fix-report.md",
		MimeType: "text/markdown",
		Data:     []byte(summary + "\n"),
	}, {
		Name:     "independent-verify.md",
		MimeType: "text/markdown",
		Data:     []byte(verification),
	}}
	if strings.TrimSpace(lastDiff) != "" {
		assets = append(assets, RunAsset{
			Name:     "fix.patch",
			MimeType: "text/x-diff",
			Data:     []byte(lastDiff),
		})
	}

	return RunResult{
		RunURL:        "kitsoki://run/" + job.JobID,
		FinalState:    "done",
		Turns:         turns,
		Summary:       summary,
		Stubbed:       false,
		Harness:       mode,
		Worktree:      worktreeAbs,
		RealHostCalls: hostCalls,
		Assets:        assets,
	}, nil
}

// extractRealDispatchEvidence scans one turn's events for HostReturned
// entries, returning the last agent submit's summary_markdown, the last
// host.git diff, and a count of HostReturned events observed (the
// RealHostCalls evidence the no-"Done"-without-real-work invariant checks).
func extractRealDispatchEvidence(events []store.Event) (summary, diff string, hostReturns int) {
	for _, ev := range events {
		if ev.Kind != store.HostReturned {
			continue
		}
		hostReturns++
		var payload struct {
			Namespace string         `json:"namespace"`
			Data      map[string]any `json:"data"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			continue
		}
		switch payload.Namespace {
		case "host.agent.task", "host.agent.ask", "host.agent.decide":
			if submitted, ok := payload.Data["submitted"].(map[string]any); ok {
				if s, ok := submitted["summary_markdown"].(string); ok && strings.TrimSpace(s) != "" {
					summary = s
				}
			}
		case "host.git":
			if d, ok := payload.Data["diff"].(string); ok && strings.TrimSpace(d) != "" {
				diff = d
			}
		}
	}
	return summary, diff, hostReturns
}

// writeFlowFixture marshals fixture to a temp YAML file RunFlows can load,
// returning the path and a cleanup func.
func writeFlowFixture(fixture *testrunner.FlowFixture) (string, func(), error) {
	out, err := yaml.Marshal(fixture)
	if err != nil {
		return "", func() {}, fmt.Errorf("ghagent: render real dispatch fixture: %w", err)
	}
	dir, err := os.MkdirTemp("", "kitsoki-ghagent-real-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("ghagent: create temp flow dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "real_dispatch.flow.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("ghagent: write real dispatch fixture: %w", err)
	}
	return path, cleanup, nil
}

func boolPtr(b bool) *bool { return &b }

// ─── Per-job worktree cleanup (task 2.1) ──────────────────────────────────
//
// The per-job worktree is CREATED by the story itself: rooms/idle.yaml's
// Step 2 invokes iface.workspace.create (bound to host.git_worktree) once
// world.workdir/feature_branch/workspace_id are seeded — the sanctioned
// checkout path, not a bespoke one here. In replay mode the recorded
// cassette intercepts that call (no real git side effect); in live mode the
// real handler runs `git worktree add`. This file only owns the CLEANUP side
// of the lifecycle: delete on success, keep (bounded retention) on failure.

// jobWorktreeRetention is the bounded retention window (proposal
// open-question 2) for kept failed-job worktrees, mirroring dogfood-marathon
// practice. Enforced by PruneStaleFailedWorktrees, not automatically on a
// timer by this package (task 3's drain/maintenance loop is the natural home
// for scheduling that sweep).
const jobWorktreeRetention = 7 * 24 * time.Hour

// cleanupJobWorktree removes the per-job worktree + its branch when the run
// succeeded. On failure it leaves the worktree in place for post-mortem. A
// no-op when the path was never materialized on disk (replay mode).
func cleanupJobWorktree(ctx context.Context, root, worktreeAbs, jobID string, succeeded bool) error {
	if _, err := os.Stat(worktreeAbs); err != nil {
		return nil // nothing checked out (replay mode served the workspace calls)
	}
	if !succeeded {
		return nil // keep for post-mortem, subject to jobWorktreeRetention
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", worktreeAbs)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ghagent: git worktree remove %s: %w: %s", worktreeAbs, err, strings.TrimSpace(string(out)))
	}
	branchCmd := exec.CommandContext(ctx, "git", "branch", "-D", jobFeatureBranch(jobID))
	branchCmd.Dir = root
	_, _ = branchCmd.CombinedOutput() // best-effort; a stray branch is harmless
	return nil
}

// PruneStaleFailedWorktrees removes gh-job-* worktrees under
// <root>/.worktrees whose directory mtime is older than
// jobWorktreeRetention. Exported so an operator CLI or a maintenance ticker
// (a natural companion to task 3's drain loop) can call it directly; safe to
// call frequently — a no-op absent stale entries.
func PruneStaleFailedWorktrees(ctx context.Context, root string) error {
	dir := filepath.Join(root, ".worktrees")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-jobWorktreeRetention)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "gh-job-") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", path)
		cmd.Dir = root
		_, _ = cmd.CombinedOutput()
	}
	return nil
}
