package queue

import (
	"context"
	"fmt"
	"strings"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/storydigest"
)

// ExecutorGate runs the project's declared Capsule CI pipeline against a
// speculative queue workspace through the existing internal/capsule/ci +
// internal/capsule/executor machinery — exactly what `kitsoki capsule ci run`
// uses — instead of a local `sh -c` command like ShellGate. Gate work is
// dispatched to the named executor's configured transport (direct PUT or
// source_bucket; see internal/capsule/ci.ConfiguredExecutors.Select), so it
// moves off the orchestrator host running `kitsoki queue worker` onto
// whatever worker that executor names, including a remote/ephemeral one.
// ExecutorGate never invents its own transport or story-launch logic; it only
// seals a RunRequest and calls ci.Service.Run.
//
// The pipeline catalog itself (executor endpoints, credentials, environment
// locks) is deliberately read from ProjectRoot — the trusted managed project
// — never from the speculative candidate workspace: an unreviewed candidate
// must not be able to redirect its own gate to a different executor, or
// change gate identity, by editing .kitsoki/ci.yaml in its own commit. Only
// the source under test and its story content (hashed by storydigest.Compute)
// come from the candidate's exact committed workspace tree.
type ExecutorGate struct {
	ProjectRoot string
	Executor    string
	Pipeline    string
	// Selector overrides executor catalog resolution for tests. Nil uses
	// ci.NewConfiguredExecutors(cfg) with source bundling wired to
	// spec.WorkspacePath, matching `capsule ci run`'s own construction.
	Selector ci.ExecutorSelector
}

// executorGateDefinitionDigest is a fixed non-empty placeholder for
// executor.Envelope.DefinitionDigest. A queue speculative workspace (created
// by scripts/dev-workspace.sh) is not a capsule created from a tracked
// definition registry the way `kitsoki capsule create` workspaces are, so
// there is no real definition digest to bind here; Seal only requires the
// field to be non-empty.
const executorGateDefinitionDigest = "queue/executor-gate/v1: no tracked capsule definition for a queue speculative workspace"

func (g ExecutorGate) pipelineName() string {
	if strings.TrimSpace(g.Pipeline) == "" {
		return "change"
	}
	return strings.TrimSpace(g.Pipeline)
}

func (g ExecutorGate) Run(ctx context.Context, spec Speculation) (GateResult, error) {
	if strings.TrimSpace(spec.WorkspacePath) == "" {
		return GateResult{}, fmt.Errorf("queue: executor gate requires a speculative workspace")
	}
	if strings.TrimSpace(g.Executor) == "" {
		return GateResult{}, Harness(fmt.Errorf("queue: executor gate requires an executor name"))
	}
	root := strings.TrimSpace(g.ProjectRoot)
	if root == "" {
		return GateResult{}, Harness(fmt.Errorf("queue: executor gate requires a project root"))
	}
	pipelineName := g.pipelineName()

	before, err := gitOutput(ctx, spec.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return GateResult{}, err
	}
	if spec.SHA != "" && before != spec.SHA {
		return GateResult{}, fmt.Errorf("queue: executor gate workspace HEAD %s does not match speculative sha %s", before, spec.SHA)
	}
	if dirty, err := executorGateDirtyLine(ctx, spec.WorkspacePath); err != nil {
		return GateResult{}, err
	} else if dirty != "" {
		return GateResult{}, fmt.Errorf("queue: executor gate requires a clean speculative workspace: %s", dirty)
	}

	// Every check from here through selector.Select is a setup/config
	// problem: an operator pointed the queue worker at a pipeline, executor,
	// or trigger vocabulary the project's own trusted .kitsoki/ci.yaml does
	// not support. That is never a signal about this candidate and never
	// transient, so it parks immediately (HarnessError) instead of burning
	// either retry budget.
	cfg, err := ci.Load(root)
	if err != nil {
		return GateResult{}, Harness(fmt.Errorf("queue: executor gate: load capsule ci config: %w", err))
	}
	p, ok := cfg.Pipelines[pipelineName]
	if !ok {
		return GateResult{}, Harness(fmt.Errorf("queue: executor gate: pipeline %q is not declared in %s/.kitsoki/ci.yaml", pipelineName, root))
	}
	if !triggerAllowed(p.Triggers, "local") {
		return GateResult{}, Harness(fmt.Errorf("queue: executor gate: pipeline %q does not allow a local trigger", pipelineName))
	}
	selector := g.Selector
	if selector == nil {
		configured := ci.NewConfiguredExecutors(cfg)
		configured.ProjectRoot = root
		configured.Source = executor.SourceBundlerFunc(func(ctx context.Context, envelope executor.Envelope) (executor.SourceBundle, error) {
			return executor.GitBundle(ctx, spec.WorkspacePath, envelope.SourceDigest, 0)
		})
		selector = configured
	}
	if _, err := selector.Select(ctx, g.Executor); err != nil {
		return GateResult{}, Harness(fmt.Errorf("queue: executor gate: resolve executor %q: %w", g.Executor, err))
	}

	// A missing/unreadable story in the candidate's own tree is a fact about
	// this candidate, not the harness or the transport; it fails the gate
	// like any other broken candidate rather than parking or retrying.
	story, err := storydigest.Compute(spec.WorkspacePath, p.Story)
	if err != nil {
		return GateResult{}, fmt.Errorf("queue: executor gate: compute story digest: %w", err)
	}

	service := ci.Service{
		ProjectRoot: root,
		Jobs:        artifactjob.NewMemoryStore(),
		Env:         environment.Resolver{ProjectRoot: root, Probe: environment.HostProbe()},
		Executors:   selector,
	}
	result, runErr := service.Run(ctx, ci.RunRequest{
		Pipeline:         pipelineName,
		Workspace:        executorGateWorkspaceHandle(spec),
		DefinitionDigest: executorGateDefinitionDigest,
		SourceDigest:     before,
		StoryDigest:      story.Digest,
		Trigger:          ci.Trigger{Kind: "local", RequestedPipeline: pipelineName},
		ExecutorOverride: g.Executor,
	})

	evidence := executorGateEvidence(pipelineName, g.Executor, result)

	if after, headErr := gitOutput(ctx, spec.WorkspacePath, "rev-parse", "HEAD"); headErr == nil && after != before {
		return GateResult{Passed: false, Evidence: evidence}, fmt.Errorf("queue: executor gate moved speculative HEAD (%s -> %s)", before, after)
	}

	if runErr != nil {
		// ci.Service.Run only returns an error when no validated verdict was
		// produced at all: a transport failure, an execution that never
		// reached a terminal completed state, a malformed/undeclared
		// verdict, or a hygiene-tool failure. None of these are an
		// assessment of the candidate itself — that is exactly what a
		// "failed" verdict outcome (handled below, without an error) is for.
		// This is the harness failing to reach or trust a verdict, so it
		// gets the lenient environmental retry budget rather than the
		// candidate's bounded product-failure attempts.
		return GateResult{Passed: false, Evidence: evidence}, Environmental(runErr)
	}

	switch result.Verdict.Outcome {
	case "passed":
		return GateResult{
			Passed:      true,
			Evidence:    evidence,
			Log:         result.Verdict.Summary,
			GateVersion: fmt.Sprintf("executor-gate/v1:%s:%s", g.Executor, pipelineName),
		}, nil
	case "failed":
		// The pipeline genuinely ran against this candidate's exact tree and
		// reported it red: a real product failure, consuming the bounded
		// attempt budget like any other gate result.
		return GateResult{Passed: false, Evidence: evidence, Log: result.Verdict.Summary}, fmt.Errorf("queue: executor gate: pipeline %q reported outcome %q: %s", pipelineName, result.Verdict.Outcome, first(result.Verdict.Summary, "no summary"))
	default:
		// infra_failed / cancelled / needs_input / any future result-contract
		// outcome: the pipeline did not produce a genuine pass-or-fail
		// assessment of this candidate's tree, so this does not consume the
		// candidate's bounded product-failure attempt budget either.
		return GateResult{Passed: false, Evidence: evidence, Log: result.Verdict.Summary}, Environmental(fmt.Errorf("queue: executor gate: pipeline %q reported outcome %q: %s", pipelineName, result.Verdict.Outcome, first(result.Verdict.Summary, "no summary")))
	}
}

// executorGateWorkspaceHandle synthesizes a control.Handle identifying this
// dispatch. A queue speculative workspace is not registered in a
// control.Manager's instance store (it is created by
// scripts/dev-workspace.sh, a separate lifecycle — see ProtectedIntegration
// and StagingIntegration), so there is no real Instance/Generation to look
// up; executor.Seal only requires a non-empty id and non-zero generation.
func executorGateWorkspaceHandle(spec Speculation) control.Handle {
	id := strings.TrimSpace(spec.WorkspaceID)
	if id == "" {
		id = "executor-gate-workspace"
	}
	return control.Handle{ID: id, Generation: 1}
}

// executorGateDirtyLine mirrors ShellGate's dirty-workspace refusal: the same
// managed-workspace sentinel files are excluded, since they are expected to
// be present and untracked in every speculative workspace.
func executorGateDirtyLine(ctx context.Context, workspace string) (string, error) {
	status, err := gitOutput(ctx, workspace, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(status, "\n") {
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ".kitsoki-capsule") || strings.HasSuffix(line, ".kitsoki-clone") || strings.HasSuffix(line, "capsule-manifest.json") || strings.HasSuffix(line, ".kitsoki-dev-workspace.json") || strings.HasSuffix(line, ".kitsoki-owner") {
			continue
		}
		return line, nil
	}
	return "", nil
}

func triggerAllowed(triggers []string, kind string) bool {
	for _, t := range triggers {
		if t == kind {
			return true
		}
	}
	return false
}

// executorGateEvidence is the bounded, human-auditable trail for a dispatch:
// the pipeline/executor identity, the durable job and execution ids (so a
// diagnose/status lookup can find the exact remote run), the sealed envelope
// digest, a bounded verdict summary, and each check's outcome.
func executorGateEvidence(pipeline, executorName string, result ci.RunResult) []string {
	lines := []string{fmt.Sprintf(
		"queue:executor-gate pipeline=%s executor=%s outcome=%s job=%s execution=%s envelope=%s",
		pipeline, executorName, first(result.Verdict.Outcome, "unknown"), string(result.Job.ID), result.Execution.ExecutionID, result.Envelope.Digest,
	)}
	if summary := executorGateBounded(result.Verdict.Summary, 2048); summary != "" {
		lines = append(lines, "queue:executor-gate:summary:"+summary)
	}
	for _, check := range result.Verdict.Checks {
		lines = append(lines, fmt.Sprintf("queue:executor-gate:check %s=%s", check.ID, check.Outcome))
	}
	return lines
}

func executorGateBounded(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) > max {
		return text[:max] + "…"
	}
	return text
}
