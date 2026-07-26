package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"kitsoki/internal/applicationartifact"
	"kitsoki/internal/applicationbuild"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
	"kitsoki/internal/storydemo"
	"kitsoki/internal/webconfig"
)

// ExecuteApplicationArtifact implements storydemo.ApplicationArtifactExecutor
// only for a daemon-enabled registry with an exact caller-owned config binding.
func (r *SessionRegistry) ExecuteApplicationArtifact(
	ctx context.Context,
	request storydemo.ApplicationArtifactRequest,
) (storydemo.ApplicationArtifactResult, error) {
	r.applicationArtifactMu.Lock()
	defer r.applicationArtifactMu.Unlock()

	if r.daemonJobs == nil {
		return storydemo.ApplicationArtifactResult{}, fmt.Errorf(
			"application artifact execution is unavailable outside daemon mode",
		)
	}
	config, ok := r.cfg.StoryApplicationArtifacts[request.CallerApplicationID]
	if !ok {
		return storydemo.ApplicationArtifactResult{}, fmt.Errorf(
			"application artifact execution is not configured for caller %q",
			request.CallerApplicationID,
		)
	}
	binding, err := configuredArtifactBinding(config, request.Operation)
	if err != nil {
		return storydemo.ApplicationArtifactResult{}, err
	}
	storyPath, err := r.registeredApplicationPath(binding.ApplicationID)
	if err != nil {
		return storydemo.ApplicationArtifactResult{}, err
	}
	input, err := json.Marshal(request.Input)
	if err != nil {
		return storydemo.ApplicationArtifactResult{}, fmt.Errorf("encode bounded application artifact input: %w", err)
	}
	jobID := artifactApplicationJobID(request, binding, input)
	originRef := "application-artifact:" + jobID
	job, err := r.daemonJobs.Get(ctx, artifactjob.JobID(jobID))
	switch {
	case errors.Is(err, artifactjob.ErrNotFound):
		job, err = r.daemonJobs.Register(ctx, artifactjob.RegisterRequest{
			ID:         artifactjob.JobID(jobID),
			AppID:      binding.ApplicationID,
			Story:      storyPath,
			Origin:     artifactjob.Origin{Kind: "application-artifact", Ref: originRef},
			Status:     artifactjob.StatusRunning,
			RunURL:     "/s/" + jobID,
			Summary:    "Story Application artifact execution",
			Phase:      request.Operation,
			Visibility: artifactjob.VisibilityLocal,
			Owner:      "application-artifact:" + request.CallerApplicationID,
		})
		if err != nil {
			return storydemo.ApplicationArtifactResult{}, fmt.Errorf("register application artifact job: %w", err)
		}
	case err != nil:
		return storydemo.ApplicationArtifactResult{}, fmt.Errorf("load application artifact job: %w", err)
	default:
		if job.AppID != binding.ApplicationID ||
			job.Story != storyPath ||
			job.Origin.Kind != "application-artifact" ||
			job.Origin.Ref != originRef {
			return storydemo.ApplicationArtifactResult{}, fmt.Errorf(
				"application artifact job identity conflicts with the configured application scope",
			)
		}
		running := artifactjob.StatusRunning
		phase := request.Operation
		reason := ""
		if _, err := r.daemonJobs.Update(ctx, job.ID, artifactjob.Update{
			Status: &running, Phase: &phase, InterruptedReason: &reason,
		}); err != nil {
			return storydemo.ApplicationArtifactResult{}, fmt.Errorf("resume application artifact job: %w", err)
		}
	}

	fail := func(cause error) (storydemo.ApplicationArtifactResult, error) {
		failed := artifactjob.StatusFailed
		summary := "Story Application artifact execution failed"
		_, _ = r.daemonJobs.Update(context.Background(), artifactjob.JobID(jobID), artifactjob.Update{
			Status: &failed, Summary: &summary,
		})
		return storydemo.ApplicationArtifactResult{}, cause
	}

	routeID, err := r.AttachExternal(ctx, storyPath, "daemon:"+jobID)
	if err != nil {
		return fail(fmt.Errorf("attach durable application artifact session: %w", err))
	}
	if routeID != jobID {
		return fail(fmt.Errorf("application artifact session identity is unstable"))
	}
	r.mu.Lock()
	entry := r.sessions[routeID]
	r.mu.Unlock()
	if entry == nil || entry.source == nil || entry.source.AppDef() == nil {
		return fail(fmt.Errorf("registered application artifact session is unavailable"))
	}
	tracePath := store.DefaultTracePath(binding.ApplicationID, "daemon", jobID)
	if _, err := r.daemonJobs.BindRun(
		ctx,
		artifactjob.JobID(jobID),
		string(entry.sid),
		"/s/"+jobID,
		tracePath,
	); err != nil {
		return fail(fmt.Errorf("bind durable application artifact run: %w", err))
	}
	applicationService, err := server.NewSessionApplicationService(
		server.Entry{Source: entry.source, Driver: entry.driver},
		"",
	)
	if err != nil {
		return fail(fmt.Errorf("construct registered application service: %w", err))
	}
	executed, err := (applicationartifact.Service{
		Definition:  entry.source.AppDef(),
		Application: applicationService,
		SessionID:   string(entry.sid),
		Actor:       "application-artifact:" + request.CallerApplicationID,
		Binding:     binding,
	}).Execute(ctx, applicationartifact.Request{
		CallerApplicationID: request.CallerApplicationID,
		Operation:           request.Operation,
		Input:               input,
	})
	if err != nil {
		return fail(err)
	}
	result := storydemo.ApplicationArtifactResult{
		JobID: jobID, SessionID: string(entry.sid),
		ReceiptIDs: append([]string(nil), executed.ReceiptIDs...),
		Primary:    executed.Primary,
	}
	for _, phase := range binding.Phases {
		for _, output := range phase.ArtifactOutputs {
			result.Artifacts = append(result.Artifacts, executed.Artifacts[output])
		}
	}
	if binding.Bundle {
		bundle, bundleErr := applicationbuild.Latest(r.applicationBundleRoot, binding.ApplicationID)
		if bundleErr != nil || bundle.Manifest.ApplicationID != binding.ApplicationID {
			return fail(fmt.Errorf("verified application bundle is unavailable"))
		}
		digest := strings.TrimPrefix(bundle.Manifest.Digest, "sha256:")
		decoded, decodeErr := hex.DecodeString(digest)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return fail(fmt.Errorf("verified application bundle identity is invalid"))
		}
		result.Bundle = "application-bundle:" + digest
		result.Artifacts = append(result.Artifacts, result.Bundle)
	}
	if result.Primary == "" || len(result.Artifacts) == 0 {
		return fail(fmt.Errorf("application artifact producer returned no terminal handle"))
	}
	terminal := result.Primary
	if result.Bundle != "" {
		terminal = result.Bundle
	}
	done := artifactjob.StatusDone
	summary := "Story Application artifact execution completed"
	if _, err := r.daemonJobs.Update(ctx, artifactjob.JobID(jobID), artifactjob.Update{
		Status: &done, Summary: &summary, TerminalArtifactHandle: &terminal,
	}); err != nil {
		return fail(fmt.Errorf("persist terminal application artifact job: %w", err))
	}
	return result, nil
}

func configuredArtifactBinding(
	config webconfig.StoryApplicationArtifactConfig,
	operation string,
) (applicationartifact.Binding, error) {
	if operation == "create_mockup" {
		if config.CreateMockup == nil {
			return applicationartifact.Binding{}, fmt.Errorf("create_mockup application artifact binding is unavailable")
		}
		return *config.CreateMockup, nil
	}
	const prefix = "materialize."
	if !strings.HasPrefix(operation, prefix) {
		return applicationartifact.Binding{}, fmt.Errorf("unknown application artifact operation %q", operation)
	}
	binding, ok := config.Materialize[strings.TrimPrefix(operation, prefix)]
	if !ok {
		return applicationartifact.Binding{}, fmt.Errorf("application artifact operation %q is not configured", operation)
	}
	return binding, nil
}

func (r *SessionRegistry) registeredApplicationPath(applicationID string) (string, error) {
	r.mu.Lock()
	var matches []string
	for _, story := range r.stories {
		if story.Def != nil && story.Def.App.ID == applicationID {
			matches = append(matches, story.Path)
		}
	}
	r.mu.Unlock()
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("registered application %q not found", applicationID)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("registered application %q is ambiguous (%d exact matches)", applicationID, len(matches))
	}
}

func artifactApplicationJobID(
	request storydemo.ApplicationArtifactRequest,
	binding applicationartifact.Binding,
	input []byte,
) string {
	bindingRaw, _ := json.Marshal(binding)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"story-application-artifact-job/v1",
		request.CallerApplicationID,
		request.Operation,
		string(bindingRaw),
		string(input),
	}, "\x00")))
	return "application-artifact-" + hex.EncodeToString(sum[:16])
}

var _ storydemo.ApplicationArtifactExecutor = (*SessionRegistry)(nil)
