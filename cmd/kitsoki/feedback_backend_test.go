package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/host"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/reviewedfeedback"
	"kitsoki/internal/testrunner"
	"kitsoki/internal/webconfig"
)

func TestConfiguredFeedbackBackendDispatchesReviewedReportThroughApplicationService(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "private-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	storiesRoot, sourcePath, unregisteredPath := writeFeedbackApplicationStories(t, root)
	writeFeedbackApplicationLedger(t, root, home)
	dbPath := filepath.Join(root, ".kitsoki", "sessions.db")
	cfg := webconfig.WebConfig{
		ReviewedFeedback: map[string]webconfig.ReviewedFeedbackBinding{
			"source.app": {
				TargetApplication: "target.app",
				TargetHandler:     "target.app.feedback.apply",
				TargetAction:      "target.app.feedback.apply.action",
			},
		},
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(cfg, []string{storiesRoot}, runtimeBase{
		DBPath: dbPath, ExecMode: orchestrator.ExecStaged,
		Flow: &testrunner.FlowFixture{}, DefaultActor: "operator",
	})
	t.Cleanup(registry.Close)
	if err := registry.EnableDaemon(dbPath); err != nil {
		t.Fatalf("enable daemon: %v", err)
	}
	if _, err := registry.Rescan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if err := registry.ConfigureFeedbackBackends(root); err != nil {
		t.Fatalf("configure feedback: %v", err)
	}

	sourceSession, err := registry.NewSession(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("create source session: %v", err)
	}
	sourceHosts, ok := registry.ApplicationHostRegistry(sourceSession)
	if !ok {
		t.Fatal("source host registry unavailable")
	}
	ctx := host.WithActor(context.Background(), "private-operator")
	listed, err := sourceHosts.Invoke(ctx, "host.feedback.list_reviewed", map[string]any{
		"scope": "current", "limit": 10,
	})
	if err != nil || listed.Error != "" {
		t.Fatalf("list reviewed = %#v, %v", listed, err)
	}
	listedWire, err := json.Marshal(listed.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{home, "private-operator", feedbackTestSecret} {
		if strings.Contains(string(listedWire), private) {
			t.Fatalf("reviewed list leaked %q: %s", private, listedWire)
		}
	}
	if strings.Contains(string(listedWire), "feedback/unreviewed") ||
		strings.Contains(string(listedWire), "other.app") {
		t.Fatalf("reviewed list widened scope: %s", listedWire)
	}

	args := map[string]any{
		"report_ref":       "feedback/reviewed",
		"dispatch_id":      "dispatch-1",
		"resume_mode":      "fresh",
		"resume_workspace": "",
		"retry_brief":      "",
	}
	first, err := sourceHosts.Invoke(ctx, "host.feedback.dispatch", args)
	if err != nil || first.Error != "" {
		t.Fatalf("dispatch = %#v, %v", first, err)
	}
	second, err := sourceHosts.Invoke(ctx, "host.feedback.dispatch", args)
	if err != nil || second.Error != "" {
		t.Fatalf("repeat dispatch = %#v, %v", second, err)
	}
	firstWire, _ := json.Marshal(first.Data)
	secondWire, _ := json.Marshal(second.Data)
	if string(firstWire) != string(secondWire) {
		t.Fatalf("deduplicated response changed:\nfirst: %s\nsecond: %s", firstWire, secondWire)
	}
	for _, private := range []string{
		root, home, "private-operator", feedbackTestSecret, "Reviewed note",
	} {
		if strings.Contains(string(firstWire), private) {
			t.Fatalf("dispatch response leaked %q: %s", private, firstWire)
		}
	}
	jobID, ok := first.Data["job_id"].(string)
	if !ok || !strings.HasPrefix(jobID, "feedback-") {
		t.Fatalf("opaque job id = %#v", first.Data["job_id"])
	}
	receipts, ok := first.Data["receipts"].([]appplatform.Receipt)
	if !ok || len(receipts) != 1 {
		t.Fatalf("receipts = %#v", first.Data["receipts"])
	}
	if receipts[0].HandlerID != "target.app.feedback.apply" ||
		receipts[0].Actor != reviewedfeedback.ServerActor ||
		receipts[0].SessionID != jobID ||
		receipts[0].Transport != appplatform.TransportEvent {
		t.Fatalf("canonical receipt = %#v", receipts[0])
	}
	finalized, err := appplatform.FinalizeReceipt(receipts[0])
	if err != nil || finalized.ID != receipts[0].ID {
		t.Fatalf("receipt verification = %#v, %v", finalized, err)
	}
	backend, ok := registry.feedbackBackends["source.app"].(*reviewedfeedback.Backend)
	if !ok {
		t.Fatalf("configured backend = %T", registry.feedbackBackends["source.app"])
	}
	report, err := backend.Ledger.ResolveReviewed(
		context.Background(),
		host.FeedbackScope{
			ApplicationID: "source.app", Owner: "owner", Revision: "1.0.0",
		},
		"feedback/reviewed",
	)
	if err != nil {
		t.Fatalf("resolve reviewed report: %v", err)
	}
	replayed, err := backend.Dispatcher.Dispatch(
		context.Background(),
		reviewedfeedback.DispatchPlan{
			JobID: jobID, DispatchID: "dispatch-1",
			SourceApplication: "source.app", TargetApplication: "target.app",
			TargetHandler: "target.app.feedback.apply",
			TargetAction:  "target.app.feedback.apply.action",
			Report:        report, ResumeMode: "fresh",
			ServerActor:    reviewedfeedback.ServerActor,
			IdempotencyKey: receipts[0].IdempotencyKey,
		},
	)
	if err != nil || len(replayed.Receipts) != 1 ||
		!replayed.Receipts[0].Replayed ||
		replayed.Receipts[0].ReplayOf != receipts[0].ID {
		t.Fatalf("application replay = %#v, %v", replayed, err)
	}
	job, err := registry.daemonJobs.Get(context.Background(), artifactjob.JobID(jobID))
	if err != nil {
		t.Fatalf("read artifact job: %v", err)
	}
	if job.Origin.Kind != "feedback" || job.AppID != "target.app" ||
		job.Owner != reviewedfeedback.ServerActor {
		t.Fatalf("artifact job = %#v", job)
	}
	target, ok := registry.Get(jobID)
	if !ok {
		t.Fatal("target application session is not routable by opaque job id")
	}
	snapshot, err := target.Source.Snapshot()
	if err != nil || snapshot.Session.Turn != 1 {
		t.Fatalf("target snapshot = %#v, %v", snapshot, err)
	}

	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, "workspace-1")
	artifactRoot := filepath.Join(root, ".artifacts")
	retryBrief := filepath.Join(artifactRoot, "retry", "brief.md")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(retryBrief), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retryBrief, []byte("reviewed retry"), 0o600); err != nil {
		t.Fatal(err)
	}
	managed, err := sourceHosts.Invoke(ctx, "host.feedback.dispatch", map[string]any{
		"report_ref":       "feedback/reviewed",
		"dispatch_id":      "dispatch-managed",
		"resume_mode":      "reused-workspace",
		"resume_workspace": "workspace-1",
		"retry_brief":      "retry/brief.md",
	})
	if err != nil || managed.Error != "" {
		t.Fatalf("managed dispatch = %#v, %v", managed, err)
	}
	managedWire, _ := json.Marshal(managed.Data)
	if strings.Contains(string(managedWire), workspace) ||
		strings.Contains(string(managedWire), retryBrief) {
		t.Fatalf("managed dispatch leaked server paths: %s", managedWire)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspaceRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err = sourceHosts.Invoke(ctx, "host.feedback.dispatch", map[string]any{
		"report_ref":       "feedback/reviewed",
		"dispatch_id":      "dispatch-symlink",
		"resume_mode":      "reused-workspace",
		"resume_workspace": "escape",
		"retry_brief":      "",
	})
	if err == nil || strings.Contains(err.Error(), outside) {
		t.Fatalf("symlink locator error = %v", err)
	}

	absolute := filepath.Join(root, ".artifacts", "private-retry.md")
	_, err = sourceHosts.Invoke(ctx, "host.feedback.dispatch", map[string]any{
		"report_ref":       "feedback/reviewed",
		"dispatch_id":      "dispatch-unsafe",
		"resume_mode":      "fresh",
		"resume_workspace": "",
		"retry_brief":      absolute,
	})
	if err == nil || strings.Contains(err.Error(), absolute) {
		t.Fatalf("unsafe locator error = %v", err)
	}

	unregisteredSession, err := registry.NewSession(context.Background(), unregisteredPath)
	if err != nil {
		t.Fatalf("create unregistered session: %v", err)
	}
	unregisteredHosts, ok := registry.ApplicationHostRegistry(unregisteredSession)
	if !ok {
		t.Fatal("unregistered host registry unavailable")
	}
	sentinel, err := unregisteredHosts.Invoke(
		ctx, "host.feedback.list_reviewed",
		map[string]any{"scope": "current", "limit": 1},
	)
	if err != nil || !strings.Contains(sentinel.Error, "unavailable") {
		t.Fatalf("unregistered feedback result = %#v, %v", sentinel, err)
	}
}

func TestFeedbackDaemonRestartInterruptsWithoutClaimingContinuation(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	storiesRoot, sourcePath, _ := writeFeedbackApplicationStories(t, root)
	writeFeedbackApplicationLedger(t, root, home)
	dbPath := filepath.Join(root, ".kitsoki", "sessions.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := feedbackTestConfig()
	base := runtimeBase{
		DBPath: dbPath, ExecMode: orchestrator.ExecStaged,
		Flow: &testrunner.FlowFixture{}, DefaultActor: "operator",
	}
	registry1 := NewRegistry(cfg, []string{storiesRoot}, base)
	if err := registry1.EnableDaemon(dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := registry1.Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := registry1.ConfigureFeedbackBackends(root); err != nil {
		t.Fatal(err)
	}
	sourceSession, err := registry1.NewSession(context.Background(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceHosts, ok := registry1.ApplicationHostRegistry(sourceSession)
	if !ok {
		t.Fatal("source host registry unavailable")
	}
	result, err := sourceHosts.Invoke(
		host.WithActor(context.Background(), "operator"),
		"host.feedback.dispatch",
		map[string]any{
			"report_ref":       "feedback/reviewed",
			"dispatch_id":      "dispatch-completed",
			"resume_mode":      "fresh",
			"resume_workspace": "",
			"retry_brief":      "",
		},
	)
	if err != nil || result.Error != "" {
		t.Fatalf("dispatch before restart = %#v, %v", result, err)
	}
	jobID := result.Data["job_id"].(string)
	pending := reviewedfeedback.DispatchState{
		ScopeID: "source.app", DispatchID: "dispatch-pending",
		RequestDigest: "pending-digest", JobID: "feedback-pending",
		Status: reviewedfeedback.DispatchPending,
	}
	state, err := registry1.feedbackDispatches.Claim(context.Background(), pending)
	if err != nil || state.Attempt != 1 {
		t.Fatalf("pending dispatch = %#v, %v", state, err)
	}
	done := artifactjob.StatusDone
	if _, err := registry1.daemonJobs.Update(
		context.Background(), artifactjob.JobID(sourceSession),
		artifactjob.Update{Status: &done},
	); err != nil {
		t.Fatal(err)
	}
	registry1.Close()

	registry2 := NewRegistry(cfg, []string{storiesRoot}, base)
	t.Cleanup(registry2.Close)
	if err := registry2.EnableDaemon(dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := registry2.Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := registry2.ConfigureFeedbackBackends(root); err != nil {
		t.Fatal(err)
	}
	restored, err := registry2.RestoreDaemonJobs(context.Background())
	if err != nil || restored != 1 {
		t.Fatalf("restore feedback jobs = %d, %v", restored, err)
	}
	if _, ok := registry2.Get(jobID); !ok {
		t.Fatal("feedback session was not reattached")
	}
	job, err := registry2.daemonJobs.Get(
		context.Background(), artifactjob.JobID(jobID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != artifactjob.StatusInterrupted ||
		job.InterruptedReason != "daemon_restarted" {
		t.Fatalf("restored artifact job = %#v", job)
	}
	retried, err := registry2.feedbackDispatches.Claim(
		context.Background(), pending,
	)
	if err != nil || retried.Status != reviewedfeedback.DispatchPending ||
		retried.Attempt != 2 {
		t.Fatalf("explicit retry = %#v, %v", retried, err)
	}
}

func TestConfigureFeedbackBackendsRejectsAmbiguousOrMismatchedTargets(t *testing.T) {
	target := feedbackTargetAppDefinition(t)
	tests := []struct {
		name    string
		stories []webconfig.StoryMeta
		binding webconfig.ReviewedFeedbackBinding
		want    string
	}{
		{
			name:    "missing source",
			stories: []webconfig.StoryMeta{{Path: "target/app.yaml", Def: target}},
			binding: validFeedbackBinding(),
			want:    "source application",
		},
		{
			name: "ambiguous target",
			stories: []webconfig.StoryMeta{
				{Path: "source/app.yaml", Def: feedbackSourceAppDefinition(t, "source.app")},
				{Path: "target-a/app.yaml", Def: target},
				{Path: "target-b/app.yaml", Def: target},
			},
			binding: validFeedbackBinding(),
			want:    "resolved to 2",
		},
		{
			name: "action mismatch",
			stories: []webconfig.StoryMeta{
				{Path: "source/app.yaml", Def: feedbackSourceAppDefinition(t, "source.app")},
				{Path: "target/app.yaml", Def: target},
			},
			binding: webconfig.ReviewedFeedbackBinding{
				TargetApplication: "target.app",
				TargetHandler:     "target.app.feedback.apply",
				TargetAction:      "target.app.feedback.other.action",
			},
			want: "not declared",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(webconfig.WebConfig{
				ReviewedFeedback: map[string]webconfig.ReviewedFeedbackBinding{
					"source.app": test.binding,
				},
			}, nil, runtimeBase{})
			registry.daemonJobs = artifactjob.NewMemoryStore()
			registry.feedbackDispatches = &feedbackDispatchStoreStub{}
			registry.stories = test.stories
			err := registry.ConfigureFeedbackBackends(t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

type feedbackDispatchStoreStub struct{}

func (*feedbackDispatchStoreStub) Claim(
	context.Context,
	reviewedfeedback.DispatchState,
) (reviewedfeedback.DispatchState, error) {
	return reviewedfeedback.DispatchState{}, nil
}
func (*feedbackDispatchStoreStub) Complete(
	context.Context, string, string, string, []appplatform.Receipt,
) error {
	return nil
}
func (*feedbackDispatchStoreStub) Interrupt(
	context.Context, string, string, string, string,
) error {
	return nil
}
func (*feedbackDispatchStoreStub) InterruptPending(context.Context, string) (int64, error) {
	return 0, nil
}

func writeFeedbackApplicationStories(
	t *testing.T,
	root string,
) (storiesRoot, sourcePath, unregisteredPath string) {
	t.Helper()
	storiesRoot = filepath.Join(root, "stories")
	sourcePath = writeFeedbackStory(
		t, storiesRoot, "source", feedbackSourceStoryYAML("source.app"),
	)
	unregisteredPath = writeFeedbackStory(
		t, storiesRoot, "unregistered", feedbackSourceStoryYAML("other.app"),
	)
	targetDir := filepath.Join(storiesRoot, "target")
	if err := os.MkdirAll(filepath.Join(targetDir, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(targetDir, "app.yaml"),
		[]byte(feedbackTargetStoryYAML),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	inputSchema := `{
  "type":"object",
  "required":["schema","source_application","dispatch_id","idempotency_key","report","resume"],
  "properties":{
    "schema":{"const":"kitsoki/reviewed-feedback-application-input/v1"},
    "source_application":{"const":"source.app"},
    "dispatch_id":{"type":"string"},
    "idempotency_key":{"type":"string"},
    "report":{"type":"object"},
    "resume":{"type":"object"}
  },
  "additionalProperties":false
}`
	if err := os.WriteFile(
		filepath.Join(targetDir, "schemas", "input.json"),
		[]byte(inputSchema),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(targetDir, "schemas", "output.json"),
		[]byte(`{"type":"object"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return storiesRoot, sourcePath, unregisteredPath
}

func writeFeedbackStory(t *testing.T, root, name, source string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func feedbackSourceStoryYAML(id string) string {
	return `app:
  id: ` + id + `
  version: 1.0.0
  title: Source
  author: owner
root: idle
intents:
  look: {title: Look}
states:
  idle:
    view: Source
    on:
      look: [{target: idle}]
`
}

const feedbackTargetStoryYAML = `app:
  id: target.app
  version: 1.0.0
  title: Target
  author: owner
world: {}
root: ready
intents:
  apply:
    title: Apply
    description: Apply one reviewed report.
    slots:
      schema: {type: string, required: true}
      source_application: {type: string, required: true}
      dispatch_id: {type: string, required: true}
      idempotency_key: {type: string, required: true}
      report: {type: object, required: true}
      resume: {type: object, required: true}
states:
  ready:
    view: Ready
    on:
      apply: [{target: ready}]
application:
  schema: application/v1
  name: Feedback target
  description: Apply reviewed application feedback.
  semantic_ref: target.app.application
  shell: {entry: home}
  pages:
    home:
      name: Home
      description: Show feedback application state.
      semantic_ref: target.app.page.home
      regions:
        main:
          name: Main
          description: Offer the reviewed feedback action.
          semantic_ref: target.app.region.main
          items:
            - card:
                id: feedback
                name: Feedback
                description: Apply reviewed feedback.
                semantic_ref: target.app.card.feedback
                actions: [target.app.feedback.apply.action]
  actions:
    target.app.feedback.apply.action:
      name: Apply
      description: Apply one reviewed feedback report.
      semantic_ref: target.app.action.feedback.apply
      handler: target.app.feedback.apply
exports:
  handlers:
    target.app.feedback.apply:
      name: Apply
      description: Apply one reviewed feedback report.
      semantic_ref: target.app.handler.feedback.apply
      input_schema: schemas/input.json
      output_schema: schemas/output.json
      session: required
      effect: write
      routing_mode: exact
      outcomes: [ok]
      dispatch: {intent: apply, state: ready, slots_from: input}
      expose: [jsonrpc]
      idempotency: {key: input.idempotency_key, scope: application}
`

const feedbackTestSecret = "ghp_123456789012345678901234567890123456"

func writeFeedbackApplicationLedger(t *testing.T, root, home string) {
	t.Helper()
	dir := filepath.Join(root, ".artifacts", "feedback")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	records := []map[string]any{
		{
			"schema":         applicationfeedback.ReportSchema,
			"idempotencyKey": "feedback/reviewed",
			"app":            "source.app", "producer": "feedback-intake", "kind": "bug",
			"userText": "Reviewed note at " + home + " with " + feedbackTestSecret,
			"reviewed": true,
			"application": map[string]any{
				"schema":         applicationfeedback.AttachmentSchema,
				"application_id": "source.app", "frame_revision": 7,
			},
			"receivedAt": "2026-07-26T03:04:05Z",
		},
		{
			"schema":         applicationfeedback.ReportSchema,
			"idempotencyKey": "feedback/unreviewed",
			"app":            "source.app", "producer": "feedback-intake", "kind": "bug",
			"userText": "draft", "reviewed": false,
			"application": map[string]any{
				"schema":         applicationfeedback.AttachmentSchema,
				"application_id": "source.app", "frame_revision": 8,
			},
			"receivedAt": "2026-07-26T03:04:06Z",
		},
		{
			"schema":         applicationfeedback.ReportSchema,
			"idempotencyKey": "feedback/other",
			"app":            "other.app", "producer": "feedback-intake", "kind": "bug",
			"userText": "other", "reviewed": true,
			"application": map[string]any{
				"schema":         applicationfeedback.AttachmentSchema,
				"application_id": "other.app", "frame_revision": 9,
			},
			"receivedAt": "2026-07-26T03:04:07Z",
		},
	}
	var body []byte
	for _, record := range records {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, raw...)
		body = append(body, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "feedback.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func feedbackTargetAppDefinition(t *testing.T) *app.AppDef {
	t.Helper()
	def, err := app.LoadBytes([]byte(feedbackTargetStoryYAML))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func feedbackSourceAppDefinition(t *testing.T, id string) *app.AppDef {
	t.Helper()
	def, err := app.LoadBytes([]byte(feedbackSourceStoryYAML(id)))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func validFeedbackBinding() webconfig.ReviewedFeedbackBinding {
	return webconfig.ReviewedFeedbackBinding{
		TargetApplication: "target.app",
		TargetHandler:     "target.app.feedback.apply",
		TargetAction:      "target.app.feedback.apply.action",
	}
}

func feedbackTestConfig() webconfig.WebConfig {
	return webconfig.WebConfig{
		ReviewedFeedback: map[string]webconfig.ReviewedFeedbackBinding{
			"source.app": validFeedbackBinding(),
		},
	}
}
