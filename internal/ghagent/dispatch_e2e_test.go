package ghagent

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/jobs"

	_ "modernc.org/sqlite"
)

// stubGHCli installs a cliExec fake that answers the three gh calls the ingress
// poll makes (gh --version for ghAvailable, gh issue list, gh pr list) entirely
// offline. issuesJSON/prsJSON are the --json stdout payloads. Returns a restore.
func stubGHCli(t *testing.T, issuesJSON, prsJSON string) func() {
	t.Helper()
	return host.SetExecRunnerForTest(func(_ context.Context, _ /*dir*/ string, name string, args ...string) (string, string, int, error) {
		if name != "gh" {
			t.Fatalf("unexpected exec %q %v", name, args)
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "--version"):
			return "gh version 2.0.0", "", 0, nil
		case strings.HasPrefix(joined, "issue list"):
			return issuesJSON, "", 0, nil
		case strings.HasPrefix(joined, "pr list"):
			return prsJSON, "", 0, nil
		default:
			t.Fatalf("unexpected gh args: %s", joined)
			return "", "", 1, nil
		}
	})
}

// recordingComments is a host.Handler bound as the CommentStore.Exec seam. It
// captures every op=comment body (so the test can assert the fenced metadata
// block) and returns a synthetic comment id. This is the DI seam in place of a
// real gh — no network, no cassette file needed.
type recordingComments struct {
	mu        sync.Mutex
	bodies    []string
	commentID string
}

func (r *recordingComments) handler(_ context.Context, args map[string]any) (host.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if body, _ := args["body"].(string); body != "" {
		r.bodies = append(r.bodies, body)
	}
	return host.Result{Data: map[string]any{"comment_id": r.commentID}}, nil
}

func newGHJobStore(t *testing.T) *jobs.GHJobStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := jobs.NewGHJobStore(db)
	if err != nil {
		t.Fatalf("NewGHJobStore: %v", err)
	}
	return store
}

// TestDispatch_MentionToAckLoop drives the FULL @kitsoki loop end-to-end across
// package boundaries: cliExec-stubbed ingress -> FilterMentions -> Classify ->
// Claim (SQLite) -> Dispatcher -> a REAL no-LLM story spawn via
// testrunner.RunFlows -> rolling-status ack comment. Fully offline, zero LLM,
// zero network.
func TestDispatch_MentionToAckLoop(t *testing.T) {
	ctx := context.Background()

	issuesJSON := `[{"number":42,"title":"@kitsoki please fix the crash","assignees":[{"login":"alice"}],"url":"https://github.com/o/r/issues/42"}]`
	restore := stubGHCli(t, issuesJSON, `[]`)
	defer restore()

	// Real ingress: ListGitHubInboxItems shells gh through the cliExec seam.
	items, err := host.ListGitHubInboxItems(ctx, host.GitHubInboxOptions{
		Repo: "o/r", IncludeIssues: true, IncludePRs: true,
	})
	if err != nil {
		t.Fatalf("ListGitHubInboxItems: %v", err)
	}
	mentions := FilterMentions(items, "o/r", DefaultMentionTrigger)
	if len(mentions) != 1 {
		t.Fatalf("FilterMentions: want 1, got %d", len(mentions))
	}
	if got, want := mentions[0].OriginRef, "github:o/r/issue/42"; got != want {
		t.Fatalf("OriginRef = %q, want %q", got, want)
	}

	store := newGHJobStore(t)
	rec := &recordingComments{commentID: "https://github.com/o/r/issues/42#issuecomment-1"}
	d := &Dispatcher{
		Jobs:     store,
		Routes:   DefaultLabelStoryMap(),
		Comments: &CommentStore{Exec: rec.handler, Repo: "o/r"},
		WorkerID: "worker-test",
		SpawnFn:  RunStorySession, // the REAL spawn through testrunner.RunFlows
	}

	job, err := d.Dispatch(ctx, mentions[0], []string{"bug"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Assertion A: the gh_jobs row advanced to done with the bug story routed.
	got, err := store.GetByOriginRef(ctx, "github:o/r/issue/42")
	if err != nil {
		t.Fatalf("GetByOriginRef: %v", err)
	}
	if got.Story != "stories/bugfix" {
		t.Errorf("Story = %q, want stories/bugfix", got.Story)
	}
	if got.State != jobs.GHDone {
		t.Errorf("State = %q, want %q", got.State, jobs.GHDone)
	}

	// Assertion B: the mapped story actually ran >= 1 turn through the real
	// machine. Dispatch synthesises a run URL only on a successful spawn.
	if got.RunURL == "" {
		t.Errorf("RunURL empty — story spawn did not complete")
	}

	// Assertion C: the ack comment body carries the fenced ```kitsoki block and
	// host.GHParseMetadata round-trips job_id + origin_ref + story + run_url.
	rec.mu.Lock()
	bodies := append([]string(nil), rec.bodies...)
	rec.mu.Unlock()
	if len(bodies) < 2 {
		t.Fatalf("want >=2 ack comments (post + update), got %d", len(bodies))
	}
	last := bodies[len(bodies)-1]
	meta := host.GHParseMetadata(last)
	if meta == nil {
		t.Fatalf("no ```kitsoki block in final ack body:\n%s", last)
	}
	if meta["job_id"] != job.JobID {
		t.Errorf("meta job_id = %v, want %s", meta["job_id"], job.JobID)
	}
	if meta["origin_ref"] != "github:o/r/issue/42" {
		t.Errorf("meta origin_ref = %v", meta["origin_ref"])
	}
	if meta["story"] != "stories/bugfix" {
		t.Errorf("meta story = %v", meta["story"])
	}
	if meta["run_url"] != got.RunURL {
		t.Errorf("meta run_url = %v, want %s", meta["run_url"], got.RunURL)
	}

	// Assertion D: idempotency. A second Dispatch of the same mention ATTACHES
	// (won=false) and does NOT respawn the story.
	spawnCalls := 0
	d.SpawnFn = func(ctx context.Context, route Route, j *jobs.GHJob) (RunResult, error) {
		spawnCalls++
		return RunStorySession(ctx, route, j)
	}
	job2, err := d.Dispatch(ctx, mentions[0], []string{"bug"})
	if err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if spawnCalls != 0 {
		t.Errorf("re-mention respawned the story %d time(s); want 0", spawnCalls)
	}
	if job2.JobID != job.JobID {
		t.Errorf("re-mention minted a new job %q; want %q", job2.JobID, job.JobID)
	}
	if job2.CommentID != job.CommentID {
		t.Errorf("re-mention comment id drift: %q vs %q", job2.CommentID, job.CommentID)
	}
}

// TestDispatch_PRBeat routes a pr-kind mention to the minimal pr-autopilot beat:
// one host.git pr_status read through the real engine + one status comment.
func TestDispatch_PRBeat(t *testing.T) {
	ctx := context.Background()

	prsJSON := `[{"number":7,"title":"@kitsoki review this PR","author":{"login":"bob"},"url":"https://github.com/o/r/pull/7"}]`
	restore := stubGHCli(t, `[]`, prsJSON)
	defer restore()

	items, err := host.ListGitHubInboxItems(ctx, host.GitHubInboxOptions{
		Repo: "o/r", IncludeIssues: true, IncludePRs: true,
	})
	if err != nil {
		t.Fatalf("ListGitHubInboxItems: %v", err)
	}
	mentions := FilterMentions(items, "o/r", DefaultMentionTrigger)
	if len(mentions) != 1 || mentions[0].Item.Kind != "pr" {
		t.Fatalf("want 1 pr mention, got %+v", mentions)
	}

	store := newGHJobStore(t)
	rec := &recordingComments{commentID: "https://github.com/o/r/pull/7#issuecomment-9"}
	d := &Dispatcher{
		Jobs:     store,
		Routes:   DefaultLabelStoryMap(),
		Comments: &CommentStore{Exec: rec.handler, Repo: "o/r"},
		WorkerID: "worker-pr",
		SpawnFn:  RunStorySession,
	}

	job, err := d.Dispatch(ctx, mentions[0], nil)
	if err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}
	if job.State != jobs.GHDone {
		t.Errorf("pr job State = %q, want done", job.State)
	}
	if job.Story != StoryPRBeat {
		t.Errorf("pr job Story = %q, want %q", job.Story, StoryPRBeat)
	}
	rec.mu.Lock()
	n := len(rec.bodies)
	rec.mu.Unlock()
	if n < 1 {
		t.Errorf("pr beat posted no status comment")
	}
}
