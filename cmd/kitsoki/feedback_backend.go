package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/effect"
	"kitsoki/internal/reviewedfeedback"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
	"kitsoki/internal/webconfig"
)

// ConfigureFeedbackBackends resolves reviewed_feedback bindings against the
// discovered application catalogue. Call it only after Rescan and EnableDaemon.
func (r *SessionRegistry) ConfigureFeedbackBackends(root string) error {
	if len(r.cfg.ReviewedFeedback) == 0 {
		return nil
	}
	if r.daemonJobs == nil || r.feedbackDispatches == nil {
		return fmt.Errorf("configure reviewed feedback: daemon persistence is unavailable")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("configure reviewed feedback: resolve server root: %w", err)
	}
	home, _ := os.UserHomeDir()

	r.mu.Lock()
	stories := append([]webconfig.StoryMeta(nil), r.stories...)
	r.mu.Unlock()

	sourceIDs := make([]string, 0, len(r.cfg.ReviewedFeedback))
	for sourceID := range r.cfg.ReviewedFeedback {
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.Strings(sourceIDs)
	for _, sourceID := range sourceIDs {
		configured := r.cfg.ReviewedFeedback[sourceID]
		if _, err := uniqueFeedbackApplication(stories, sourceID, "source"); err != nil {
			return err
		}
		target, err := uniqueFeedbackApplication(
			stories, configured.TargetApplication, "target",
		)
		if err != nil {
			return err
		}
		if err := validateFeedbackTarget(target.Def, configured); err != nil {
			return fmt.Errorf("configure reviewed feedback %q: %w", sourceID, err)
		}
		binding := reviewedfeedback.Binding{
			SourceApplication: sourceID,
			TargetApplication: configured.TargetApplication,
			TargetHandler:     configured.TargetHandler,
			TargetAction:      configured.TargetAction,
		}
		backend := &reviewedfeedback.Backend{
			Binding: binding,
			Ledger: reviewedfeedback.JSONLLedger{
				Path: filepath.Join(root, ".artifacts", "feedback", "feedback.jsonl"),
				Home: home,
			},
			Locators: reviewedfeedback.ManagedLocatorResolver{
				WorkspaceRoot: filepath.Join(root, ".capsules", "workspaces"),
				ArtifactRoot:  filepath.Join(root, ".artifacts"),
			},
			Store: r.feedbackDispatches,
			Dispatcher: feedbackRegistryDispatcher{
				registry: r, target: target, binding: binding,
			},
		}
		if err := r.RegisterFeedbackBackend(sourceID, backend); err != nil {
			return fmt.Errorf("configure reviewed feedback %q: %w", sourceID, err)
		}
	}
	return nil
}

func uniqueFeedbackApplication(
	stories []webconfig.StoryMeta,
	applicationID, role string,
) (webconfig.StoryMeta, error) {
	var matches []webconfig.StoryMeta
	for _, story := range stories {
		if story.Def != nil && story.Def.App.ID == applicationID {
			matches = append(matches, story)
		}
	}
	if len(matches) != 1 {
		return webconfig.StoryMeta{}, fmt.Errorf(
			"configure reviewed feedback: %s application %q resolved to %d catalogue entries; exactly one is required",
			role, applicationID, len(matches),
		)
	}
	return matches[0], nil
}

func validateFeedbackTarget(
	def *app.AppDef,
	configured webconfig.ReviewedFeedbackBinding,
) error {
	if def == nil || def.Exports == nil ||
		def.Exports.Handlers[configured.TargetHandler] == nil {
		return fmt.Errorf("target handler %q is not exported", configured.TargetHandler)
	}
	handler := def.Exports.Handlers[configured.TargetHandler]
	if handler.Session != string(appplatform.SessionRequired) {
		return fmt.Errorf("target handler %q must require a session", configured.TargetHandler)
	}
	if handler.Effect != effect.Write && handler.Effect != effect.External {
		return fmt.Errorf("target handler %q must declare a write or external effect", configured.TargetHandler)
	}
	contract, _ := app.EffectiveApplication(def)
	if contract == nil || contract.Actions[configured.TargetAction] == nil {
		return fmt.Errorf("target action %q is not declared", configured.TargetAction)
	}
	if contract.Actions[configured.TargetAction].Handler != configured.TargetHandler {
		return fmt.Errorf(
			"target action %q does not bind exported handler %q",
			configured.TargetAction, configured.TargetHandler,
		)
	}
	return nil
}

type feedbackRegistryDispatcher struct {
	registry *SessionRegistry
	target   webconfig.StoryMeta
	binding  reviewedfeedback.Binding
}

type feedbackApplicationInput struct {
	Schema            string                    `json:"schema"`
	SourceApplication string                    `json:"source_application"`
	DispatchID        string                    `json:"dispatch_id"`
	IdempotencyKey    string                    `json:"idempotency_key"`
	Report            feedbackApplicationReport `json:"report"`
	Resume            feedbackApplicationResume `json:"resume"`
}

type feedbackApplicationReport struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Producer   string `json:"producer"`
	Revision   string `json:"revision"`
	ReceiptRef string `json:"receipt_ref"`
	ReviewedAt string `json:"reviewed_at"`
	Frame      uint64 `json:"frame_revision"`
}

type feedbackApplicationResume struct {
	Mode           string `json:"mode"`
	WorkspacePath  string `json:"workspace_path,omitempty"`
	RetryBriefPath string `json:"retry_brief_path,omitempty"`
}

func (d feedbackRegistryDispatcher) Dispatch(
	ctx context.Context,
	plan reviewedfeedback.DispatchPlan,
) (reviewedfeedback.DispatchResult, error) {
	if d.registry == nil || plan.SourceApplication != d.binding.SourceApplication ||
		plan.TargetApplication != d.binding.TargetApplication ||
		plan.TargetHandler != d.binding.TargetHandler ||
		plan.TargetAction != d.binding.TargetAction {
		return reviewedfeedback.DispatchResult{}, fmt.Errorf("reviewed feedback dispatcher binding mismatch")
	}
	entry, err := d.ensureSession(ctx, plan)
	if err != nil {
		return reviewedfeedback.DispatchResult{}, err
	}
	service, err := server.NewSessionApplicationService(entry, "")
	if err != nil {
		return reviewedfeedback.DispatchResult{}, fmt.Errorf("open reviewed feedback target application: %w", err)
	}
	// Feedback dispatch is a headless internal call. Its public result is the
	// canonical receipt, so do not compile or attach a presentation frame.
	service.Frames = nil
	input, err := json.Marshal(feedbackApplicationInput{
		Schema:            "kitsoki/reviewed-feedback-application-input/v1",
		SourceApplication: plan.SourceApplication,
		DispatchID:        plan.DispatchID,
		IdempotencyKey:    plan.IdempotencyKey,
		Report: feedbackApplicationReport{
			Ref:        plan.Report.Projection.Ref,
			Kind:       plan.Report.Projection.Kind,
			Title:      plan.Report.Projection.Title,
			Summary:    plan.Report.Projection.Summary,
			Producer:   plan.Report.Projection.Producer,
			Revision:   plan.Report.Projection.Revision,
			ReceiptRef: plan.Report.Projection.ReceiptRef,
			ReviewedAt: plan.Report.Projection.ReviewedAt.UTC().Format(time.RFC3339Nano),
			Frame:      plan.Report.Frame,
		},
		Resume: feedbackApplicationResume{
			Mode: plan.ResumeMode, WorkspacePath: plan.WorkspacePath,
			RetryBriefPath: plan.RetryBriefPath,
		},
	})
	if err != nil {
		return reviewedfeedback.DispatchResult{}, fmt.Errorf("encode reviewed feedback target input: %w", err)
	}
	outcome, err := service.Call(ctx, appplatform.TransportEvent, appplatform.CallRequest{
		Handler: plan.TargetHandler, Input: input, SessionID: plan.JobID,
		Actor: plan.ServerActor, IdempotencyKey: plan.IdempotencyKey,
	})
	if err != nil {
		d.markFailed(plan.JobID)
		return reviewedfeedback.DispatchResult{}, fmt.Errorf("invoke reviewed feedback target application: %w", err)
	}
	d.registry.syncDaemonJob(plan.JobID)
	return reviewedfeedback.DispatchResult{
		JobID: plan.JobID, Receipts: []appplatform.Receipt{outcome.Receipt},
	}, nil
}

func (d feedbackRegistryDispatcher) ensureSession(
	ctx context.Context,
	plan reviewedfeedback.DispatchPlan,
) (server.Entry, error) {
	existing, err := d.registry.daemonJobs.Get(ctx, artifactjob.JobID(plan.JobID))
	switch {
	case err == nil:
		if err := validateFeedbackJob(
			existing, plan.JobID, plan.DispatchID, d.target.Path, d.binding,
		); err != nil {
			return server.Entry{}, err
		}
	case errors.Is(err, artifactjob.ErrNotFound):
	default:
		return server.Entry{}, fmt.Errorf("read reviewed feedback artifact job: %w", err)
	}

	id, err := d.registry.AttachExternal(ctx, d.target.Path, "daemon:"+plan.JobID)
	if err != nil {
		return server.Entry{}, fmt.Errorf("attach reviewed feedback target session: %w", err)
	}
	if id != plan.JobID {
		return server.Entry{}, fmt.Errorf("reviewed feedback target returned unstable job identity")
	}
	entry, ok := d.registry.Get(id)
	if !ok {
		return server.Entry{}, fmt.Errorf("reviewed feedback target session is unavailable")
	}
	d.registry.mu.Lock()
	live := d.registry.sessions[id]
	d.registry.mu.Unlock()
	if live == nil || live.source == nil {
		return server.Entry{}, fmt.Errorf("reviewed feedback target session has no durable identity")
	}
	if existing.ID != "" && existing.SessionID != live.sid {
		return server.Entry{}, fmt.Errorf("reviewed feedback artifact job session identity changed")
	}
	if existing.ID == "" {
		snapshot, snapshotErr := live.source.Snapshot()
		if snapshotErr != nil {
			return server.Entry{}, fmt.Errorf("inspect reviewed feedback target session: %w", snapshotErr)
		}
		tracePath := store.DefaultTracePath(
			d.binding.TargetApplication, "daemon", plan.JobID,
		)
		existing, err = d.registry.daemonJobs.Register(ctx, artifactjob.RegisterRequest{
			ID: artifactjob.JobID(plan.JobID), SessionID: live.sid,
			AppID: d.binding.TargetApplication, Story: d.target.Path,
			Origin: artifactjob.Origin{
				Kind: "feedback", Ref: "feedback:" + d.binding.SourceApplication + "/" + plan.DispatchID,
			},
			Status: artifactjob.StatusRunning, RunURL: "/s/" + plan.JobID,
			TracePath: tracePath, Summary: plan.Report.Projection.Title,
			Phase:      snapshot.Session.CurrentState,
			Visibility: artifactjob.VisibilityLocal, Owner: reviewedfeedback.ServerActor,
		})
		if err != nil {
			return server.Entry{}, fmt.Errorf("register reviewed feedback artifact job: %w", err)
		}
		if err := validateFeedbackJob(
			existing, plan.JobID, plan.DispatchID, d.target.Path, d.binding,
		); err != nil {
			return server.Entry{}, err
		}
	} else {
		status := artifactjob.StatusRunning
		reason := ""
		if _, err := d.registry.daemonJobs.Update(
			ctx, artifactjob.JobID(plan.JobID),
			artifactjob.Update{Status: &status, InterruptedReason: &reason},
		); err != nil {
			return server.Entry{}, fmt.Errorf("resume reviewed feedback artifact job: %w", err)
		}
	}
	return entry, nil
}

func validateFeedbackJob(
	job artifactjob.Job,
	jobID, dispatchID string,
	storyPath string,
	binding reviewedfeedback.Binding,
) error {
	if job.ID != artifactjob.JobID(jobID) ||
		job.AppID != binding.TargetApplication ||
		job.Story != storyPath || job.Origin.Kind != "feedback" ||
		job.Origin.Ref != "feedback:"+binding.SourceApplication+"/"+dispatchID ||
		job.Owner != reviewedfeedback.ServerActor {
		return fmt.Errorf("reviewed feedback artifact job identity does not match its configured target")
	}
	return nil
}

func (d feedbackRegistryDispatcher) markFailed(jobID string) {
	status := artifactjob.StatusFailed
	_, _ = d.registry.daemonJobs.Update(
		context.Background(), artifactjob.JobID(jobID),
		artifactjob.Update{Status: &status},
	)
}
