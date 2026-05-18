package orchestrator_test

// Integration smoke tests for the kitsoki-dev dogfood app — the layer
// the existing flow-fixture suite can't reach (every flow stubs every
// host to {ok:true}, so any code path predicated on a host returning
// Result.Error is invisible to fixtures). See
// docs/proposals/dogfood-regression-testing-gap.md §4 and §5 for the
// motivation; in particular these tests would have caught the
// 2026-05-18 `go_bugfix` redirect-loop hang (commit 9b58dc4) before
// fa39746's `maxRedirectDepth` cap landed.
//
// Shape:
//   - real on-disk `git init` repo under t.TempDir() with one commit
//     on `main`, so `git worktree add` has a base to root at.
//   - real host.RegisterBuiltins; only host.oracle.ask_with_mcp is
//     stubbed (canned artifact payload — no real LLM call).
//   - hard `context.WithTimeout(ctx, …)` per turn so a regression
//     FAILS in seconds rather than hanging CI.
//
// Conceptual mirror of stories/kitsoki-dev/scenarios/verify_autostart.yaml.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/inbox"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// setupDogfoodRepo builds a self-contained working tree at t.TempDir():
// a fresh `git init` repo with one commit on `main`, plus snapshots of
// the kitsoki `stories/` tree (so `app.Load` resolves all imports) and
// the `issues/` tree (so `host.local_files.ticket` finds real bug
// files). Sets the process cwd via t.Chdir so `host.git_worktree`
// (which uses `dir == "" → cwd`) operates on the temp repo.
//
// Returns the repo root and the canonical ticket id we drive through
// the pipeline.
func setupDogfoodRepo(t *testing.T) (repoRoot string, ticketID string) {
	t.Helper()

	repoRoot = t.TempDir()

	// Copy stories/ and issues/ from the live repo. We resolve the
	// kitsoki repo root relative to this test file: package dir is
	// internal/orchestrator/, so two levels up is the repo root.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	kitsokiRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	for _, sub := range []string{"stories", "issues"} {
		src := filepath.Join(kitsokiRoot, sub)
		dst := filepath.Join(repoRoot, sub)
		require.NoError(t, copyTree(src, dst),
			"copy %s → %s", src, dst)
	}

	// Initialise a real git repo so `git worktree add` has a base.
	gitConfig := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}
	gitConfig("init", "--quiet", "--initial-branch=main")
	gitConfig("config", "user.email", "smoke@test.invalid")
	gitConfig("config", "user.name", "Smoke Test")
	gitConfig("add", "-A")
	gitConfig("commit", "--quiet", "-m", "init")

	// Chdir into the temp repo so host.git_worktree (dir=cwd) and
	// host.local_files.ticket (root=cwd fallback) both operate here.
	// t.Chdir restores the prior cwd on test completion.
	t.Chdir(repoRoot)

	// Pick the canonical integration-smoke ticket. It lives under
	// issues/bugs/ in the live repo and was copied above.
	ticketID = "2026-05-17T111838Z-integration-smoke-bug-picked-up-by-dogfood"
	bugPath := filepath.Join(repoRoot, "issues", "bugs", ticketID+".md")
	_, statErr := os.Stat(bugPath)
	require.NoError(t, statErr, "integration-smoke ticket must exist at %s", bugPath)

	return repoRoot, ticketID
}

// copyTree mirrors src → dst recursively. Files only — git metadata
// (.git/) is not copied; we run `git init` fresh on dst's parent.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// dogfoodArtifact is the canned schema-shaped payload the oracle stub
// returns. It covers every bf room's bind path (reproduction_artifact,
// propose_fix_artifact, implement_review_artifact, validate_artifact,
// done_artifact, and the llm_verdict shape — though judge_mode=human
// skips the verdict branches). Modeled on
// stories/bugfix/flows/happy_human.yaml's stub.
var dogfoodArtifact = map[string]any{
	"summary_title":    "Stub artifact",
	"summary_markdown": "# Stub artifact body\n\nCanned payload for the dogfood smoke test.\n",
	"bug_verified":    true,
	"steps":           []string{"step A", "step B"},
	"involved_components": []map[string]any{
		{"name": "internal/orchestrator", "reason": "lives here"},
	},
	"fix_description":  "stub",
	"root_cause":       "stub",
	"affected_files":   []string{"internal/orchestrator/orchestrator.go"},
	"confidence":       0.9,
	"reasoning":        "stub",
	"status":           "passed",
	"tests_added":      []string{"internal/orchestrator/dogfood_smoke_test.go"},
	"tests_run":        map[string]any{"passed": 1, "failed": 0, "log": "PASS"},
	"outcome":          "pass",
	"evidence":         map[string]any{"build": "ok", "api": "n/a", "ui": "n/a"},
	"next_action_hint": "",
	"lessons":          []map[string]any{},
	// Verdict-shaped keys so judge branches (when llm_then_human) are
	// also covered if a future change flips judge_mode.
	"verdict":    "accept",
	"intent":     "accept",
	"reason":     "stub verdict",
}

// newSmokeOrchestrator builds an orchestrator pinned to the temp-repo
// kitsoki-dev app with the real host registry (oracle stubbed out).
// Returns the orchestrator, the underlying store (for direct history
// reads), an open session id, and the count pointer the oracle stub
// increments per call (handy for sanity asserts).
//
// The oracle stub is registered FIRST and the rest of the builtins are
// registered piecemeal via registerBuiltinsExceptOracle. host.Registry
// panics on duplicate Register, so to override the prod oracle handler
// we have to skip its line in RegisterBuiltins. The set is small and
// the dogfood smoke doesn't need every builtin — just the handlers the
// kitsoki-dev `hosts:` allow-list (kitsoki-dev/app.yaml:64-73) declares.
func newSmokeOrchestrator(t *testing.T, repoRoot string) (*orchestrator.Orchestrator, store.Store, app.SessionID, *int) {
	t.Helper()
	appPath := filepath.Join(repoRoot, "stories", "kitsoki-dev", "app.yaml")
	def, err := app.Load(appPath)
	require.NoError(t, err, "load kitsoki-dev/app.yaml from %s", appPath)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	reg := host.NewRegistry()
	oracleCalls := 0
	stub := func(ctx context.Context, args map[string]any) (host.Result, error) {
		oracleCalls++
		stdoutJSON, _ := json.Marshal(dogfoodArtifact)
		return host.Result{Data: map[string]any{
			"submitted": dogfoodArtifact,
			"stdout":    string(stdoutJSON),
			"ok":        true,
		}}, nil
	}
	reg.Register("host.oracle.ask_with_mcp", stub)

	// Register the prod handlers kitsoki-dev declares in its hosts
	// allow-list, MINUS host.oracle.ask_with_mcp (already stubbed).
	reg.Register("host.local_files.ticket", host.LocalFilesTicketHandler)
	reg.Register("host.git", host.GitVCSHandler)
	reg.Register("host.local", host.LocalCIHandler)
	reg.Register("host.git_worktree", host.GitWorktreeHandler)
	reg.Register("host.append_to_file", host.AppendFileTransportHandler)
	reg.Register("host.inbox.add", host.InboxAddHandler)

	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))

	sid, err := orch.NewSession(context.Background())
	require.NoError(t, err)

	return orch, s, sid, &oracleCalls
}

// seedDogfoodWorld returns the slot bag mirroring
// stories/kitsoki-dev/scenarios/verify_autostart.yaml: pin the ticket,
// set judge_mode=human + auto_accept_on_post=true at every fold level,
// and seed bf's per-pipeline keys empty so on_enter actually fires the
// auto-create + auto-start chain.
func seedDogfoodWorld(ticketID string) map[string]any {
	threadPath := filepath.Join("issues", "bugs", ticketID+".md")
	return map[string]any{
		"core__ticket_id":    ticketID,
		"core__ticket_title": "integration smoke — bug picked up by dogfood",
		"core__ticket_url":   threadPath,
		"core__ticket_type":  "bug",
		"core__thread":       threadPath,

		"judge_mode":                       "human",
		"judge_confidence_threshold":       0.8,
		"core__judge_mode":                 "human",
		"core__judge_confidence_threshold": 0.8,
		"core__bf__judge_mode":             "human",
		"core__bf__judge_confidence_threshold": 0.8,

		// auto_accept_on_post was removed when the bugfix story
		// merged _executing+_awaiting_reply into one room per phase.
		// The accept arc posts the artifact AND advances in a single
		// turn, so no separate auto-accept step is needed.

		"core__bf__workspace_id":           "",
		"core__bf__feature_branch":         "",
		"core__bf__workdir":                "",
		"core__bf__base_branch":            "main",
		"core__bf__bf_autostart_attempted": false,
		"core__bf__bugfix_mode":            "full",
	}
}

// TestDogfoodSmoke_AutoStartThroughBugfix is the regression test for
// the `go_bugfix` redirect-loop hang flagged in the dogfood-regression-
// testing-gap proposal. The class of bug: an on_error: <sibling-room>
// arc whose redirect target's on_enter re-invokes the failing host
// call, looping until the orchestrator's `maxRedirectDepth` cap fires.
//
// Setup mirrors `verify_autostart.yaml`: a clean temp git repo (no
// stale `.worktrees/bf-<id>/`), the integration-smoke bug seeded, then
// `core__go_bugfix` from `core.main`. Expected: workspace.create
// succeeds against the real repo, the auto-start emit fires, the
// session lands at `core.bf.reproducing` within seconds.
//
// We then drive ONE `core__bf__accept` to prove the auto-accept arm
// also lands the next phase (reproducing →
// proposing). Walking past `implementing_executing`
// currently requires fixing a separate workspace_id mismatch in
// bf.idle.yaml (`workspace_id="bf-<id>"` vs. the on-disk dir
// `fix-<id>` produced by git_worktree.create from feature_branch
// "fix/<id>"), which is out of scope per the task spec; one proceed
// is enough to prove the auto-start + auto-accept chain doesn't loop.
func TestDogfoodSmoke_AutoStartThroughBugfix(t *testing.T) {
	repoRoot, ticketID := setupDogfoodRepo(t)
	orch, _, sid, oracleCalls := newSmokeOrchestrator(t, repoRoot)

	ctx := context.Background()

	// 1. Run initial on_enter for core.main (which invokes
	//    iface.ticket.list_mine → host.local_files.ticket).
	{
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		require.NoError(t, orch.RunInitialOnEnter(c, sid), "RunInitialOnEnter must finish within 10s")
		cancel()
	}

	// 2. Teleport to core.main with the ticket+mode seeded.
	{
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := orch.Teleport(c, sid, inbox.TeleportTarget{
			State: app.StatePath("core.main"),
			Slots: seedDogfoodWorld(ticketID),
		})
		require.NoError(t, err, "Teleport to core.main with seeded ticket must succeed")
		cancel()
	}

	// 3. Submit core__go_bugfix — the regression's trigger. Under the
	//    cap fix the session must land at core.bf.reproducing
	//    (auto-start fired through bf.idle.on_enter; workspace.create
	//    succeeded against the temp git repo; emit_intent advanced the
	//    leaf). A loop regression hits the 10s deadline.
	{
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		out, err := orch.SubmitDirect(c, sid, "core__go_bugfix", nil)
		cancel()
		require.NoError(t, err, "core__go_bugfix must complete within 10s (loop regression?)")
		require.NotNil(t, out)
		require.Equal(t, app.StatePath("core.bf.reproducing"), out.NewState,
			"go_bugfix should auto-start through bf.idle and land at reproducing; got %q (view: %q)",
			out.NewState, out.View)
	}

	// Worktree must exist on disk. The created dir is
	// `.worktrees/fix-<ticket_id>` because
	// `internal/host/git_worktree.go::worktreeCreate` derives the dir
	// basename from the branch name (feature_branch="fix/<id>"),
	// flattening slashes to dashes. Note the proposal text mentions
	// `.worktrees/bf-<id>` — that's `world.workdir`, which doesn't
	// match the actual on-disk dir produced by the handler. The
	// real-handler contract is what we verify here.
	workdir := filepath.Join(repoRoot, ".worktrees", "fix-"+ticketID)
	_, statErr := os.Stat(workdir)
	require.NoError(t, statErr,
		"worktree dir must exist after go_bugfix; expected %s", workdir)

	// bf_autostart_attempted must be true so a re-entry to idle is a no-op.
	journey, err := orch.LoadJourney(sid)
	require.NoError(t, err)
	require.Equal(t, true, journey.World.Vars["core__bf__bf_autostart_attempted"],
		"bf_autostart_attempted must be set true after the auto-start chain ran")

	// Oracle was called once for reproducing.on_enter.
	require.GreaterOrEqual(t, *oracleCalls, 1,
		"oracle stub should have been invoked at least once for reproducing.on_enter")

	// 4. Drive one proceed → auto-accept fires through
	//    reproducing, lands at proposing.
	{
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		out, err := orch.SubmitDirect(c, sid, "core__bf__accept", nil)
		cancel()
		require.NoError(t, err, "core__bf__accept must complete within 10s")
		require.NotNil(t, out)
		require.Equal(t, app.StatePath("core.bf.proposing"), out.NewState,
			"proceed → auto-accept on _awaiting_reply (auto_accept_on_post=true, judge_mode=human) should land at proposing; got %q",
			out.NewState)
	}
}

// TestDogfoodSmoke_StaleWorktreeRecoversOrFailsCleanly verifies the
// failure-mode the regression actually surfaced in the wild: a stale
// `.worktrees/<dir>/` from a previous aborted run makes `git worktree
// add` exit non-zero. Pre-fa39746 this looped forever; post-fix the
// session must EITHER recover (priority 3 of the proposal — make the
// handler idempotent — not landed) OR surface the failure cleanly
// (current behaviour: the redirect cap fires or the bf_autostart_
// attempted guard parks the session at bf.idle).
//
// Either is a valid contract; the test asserts the session DOES NOT
// hang and lands at a coherent resting place with the error trail
// either in a HarnessError event or pinned via bf_autostart_attempted
// (so a manual `start` won't re-fire the failure).
func TestDogfoodSmoke_StaleWorktreeRecoversOrFailsCleanly(t *testing.T) {
	repoRoot, ticketID := setupDogfoodRepo(t)

	// Pre-create the worktree dir AND a sibling branch ref so `git
	// worktree add -b fix/<id> .worktrees/fix-<id> main` fails with
	// `fatal: '<path>' already exists` — exactly the shape the
	// proposal flagged.
	staleDir := filepath.Join(repoRoot, ".worktrees", "fix-"+ticketID)
	require.NoError(t, os.MkdirAll(staleDir, 0o755))
	// Leave a marker file so the dir is non-empty (git refuses to
	// add into a non-empty dir even if the branch ref doesn't exist).
	require.NoError(t, os.WriteFile(filepath.Join(staleDir, "stale"), []byte("stale"), 0o644))

	orch, s, sid, _ := newSmokeOrchestrator(t, repoRoot)
	ctx := context.Background()

	{
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		require.NoError(t, orch.RunInitialOnEnter(c, sid))
		cancel()
	}

	{
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := orch.Teleport(c, sid, inbox.TeleportTarget{
			State: app.StatePath("core.main"),
			Slots: seedDogfoodWorld(ticketID),
		})
		require.NoError(t, err)
		cancel()
	}

	// 5s deadline: a loop regression times out fast. The cap fires
	// at depth 4 + 1 = 5 host invocations max, well under 5s.
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := orch.SubmitDirect(c, sid, "core__go_bugfix", nil)
	require.NoError(t, err,
		"core__go_bugfix with a stale worktree must complete within 5s; a loop regression hangs here")
	require.NotNil(t, out)

	// The session must land at a coherent resting place. The two
	// valid contracts are:
	//   (a) HarnessError event with reason=on_error.depth_cap_exceeded
	//       (the redirect cap fired — proves the cap is doing its job).
	//   (b) bf_autostart_attempted == true and state is bf.idle (the
	//       per-room guard pinned the autostart so a manual `start`
	//       won't re-fire). The proposal's priority-2 defence-in-depth.
	journey, err := orch.LoadJourney(sid)
	require.NoError(t, err)

	history, histErr := s.LoadHistory(sid)
	require.NoError(t, histErr)

	hasCapHarnessError := false
	for _, ev := range history {
		if ev.Kind != store.HarnessError {
			continue
		}
		var p map[string]any
		if jsonErr := json.Unmarshal(ev.Payload, &p); jsonErr != nil {
			continue
		}
		if reason, _ := p["reason"].(string); reason == "on_error.depth_cap_exceeded" {
			hasCapHarnessError = true
			break
		}
	}

	autostartAttempted, _ := journey.World.Vars["core__bf__bf_autostart_attempted"].(bool)

	t.Logf("stale-worktree landed at state=%q autostart_attempted=%v cap_fired=%v history_events=%d",
		journey.State, autostartAttempted, hasCapHarnessError, len(history))

	require.True(t, hasCapHarnessError || autostartAttempted,
		"stale-worktree run must either fire the redirect cap (HarnessError) or pin bf_autostart_attempted; got state=%q, autostart_flag=%v, cap_fired=%v",
		journey.State, autostartAttempted, hasCapHarnessError)

	// And it must NOT be parked somewhere incoherent (e.g. a half-
	// transitioned compound state). Any of: bf.idle (the guard kept
	// it parked), bf.reproducing (the create somehow
	// succeeded — unlikely with the stale dir but possible if a
	// future patch makes it idempotent), or core.main (the redirect
	// bounced through @exit:abandoned) are acceptable.
	acceptable := map[app.StatePath]bool{
		"core.bf.idle":                   true,
		"core.bf.reproducing":  true,
		"core.main":                      true,
	}
	require.True(t, acceptable[journey.State],
		"session must settle at a coherent resting place after stale-worktree failure; got %q (acceptable: %v)", journey.State, acceptable)
}
