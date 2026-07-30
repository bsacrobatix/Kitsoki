package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CapsuleCICommandRunner is injected so project-wrapper flow tests never fork
// real commands or spend on an LLM. Production deliberately uses the
// project-owned command string through a shell: project profiles are the same
// trust boundary as a Makefile or package script, and quoting must retain its
// normal meaning.
type CapsuleCICommandRunner interface {
	Run(context.Context, string, string) (string, int, error)
}

type CapsuleCICommandRunnerFunc func(context.Context, string, string) (string, int, error)

func (f CapsuleCICommandRunnerFunc) Run(ctx context.Context, workdir, command string) (string, int, error) {
	return f(ctx, workdir, command)
}

// CapsuleCIEvidenceDestination is a controller-owned destination for the
// bounded project-check artifact.  Leaving it empty preserves the historical
// workspace-local .artifacts path.  Exact-source CI uses a project-owned
// destination because its source checkout is intentionally removed after the
// result and receipt are persisted.
//
// Root and ReferencePrefix are supplied by the launcher/controller, never by
// the story's host-call arguments.  That keeps an untrusted checked-in story
// from selecting an arbitrary controller filesystem path for retained output.
type CapsuleCIEvidenceDestination struct {
	Root            string
	ReferencePrefix string
}

type shellCapsuleCICommandRunner struct{}

func (shellCapsuleCICommandRunner) Run(ctx context.Context, workdir, command string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0, nil
	}
	// CommandContext commonly returns an ExitError after it kills the child.
	// Check the context first so a declared deadline or caller cancellation is
	// never misreported as an ordinary project exit failure.
	if ctx.Err() != nil {
		return string(out), -1, ctx.Err()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode(), nil
	}
	return string(out), -1, err
}

// NewCapsuleCIProjectChecksHandler returns the deterministic host used by the
// generated project CI wrapper. It reads the checked-in project profile, runs
// its test/build commands, persists bounded local evidence, and constructs the
// typed verdict. The story remains the CI pipeline: this host is one ordinary
// deterministic fact producer, not a second step-DAG runtime.
func NewCapsuleCIProjectChecksHandler(runner CapsuleCICommandRunner) Handler {
	return NewCapsuleCIProjectChecksHandlerWithEvidenceDestination(runner, CapsuleCIEvidenceDestination{})
}

// NewCapsuleCIProjectChecksHandlerWithEvidenceDestination constructs the
// project-check host with a trusted, optional retained evidence destination.
// It is intended for controller-owned transient source checkouts; ordinary
// Capsule CI retains the default workspace-local behavior.
func NewCapsuleCIProjectChecksHandlerWithEvidenceDestination(runner CapsuleCICommandRunner, destination CapsuleCIEvidenceDestination) Handler {
	if runner == nil {
		runner = shellCapsuleCICommandRunner{}
	}
	return func(ctx context.Context, args map[string]any) (Result, error) {
		workdir, err := filepath.Abs(capsuleCIStringArg(args, "workdir", "."))
		if err != nil {
			return Result{}, err
		}
		profilePath := filepath.Join(workdir, ".kitsoki", "project-profile.yaml")
		raw, err := os.ReadFile(profilePath)
		if err != nil {
			return Result{Error: fmt.Sprintf("host.capsule_ci.project_checks: read project profile: %v", err), FailureKind: FailureFatal}, nil
		}
		var profile map[string]any
		if err := yaml.Unmarshal(raw, &profile); err != nil {
			return Result{Error: fmt.Sprintf("host.capsule_ci.project_checks: parse project profile: %v", err), FailureKind: FailureFatal}, nil
		}
		commands, _ := profile["commands"].(map[string]any)
		if commands == nil {
			commands = map[string]any{}
		}
		pipeline := capsuleCIStringArg(args, "pipeline", "")
		jobID := safeArtifactID(capsuleCIStringArg(args, "job_id", "run"))
		commandTimeout, err := capsuleCICommandTimeout(args)
		if err != nil {
			return Result{Error: fmt.Sprintf("host.capsule_ci.project_checks: command_timeout: %v", err), FailureKind: FailureFatal}, nil
		}
		evidencePath, evidenceRef, err := capsuleCIEvidencePath(workdir, jobID, destination)
		if err != nil {
			return Result{Error: fmt.Sprintf("host.capsule_ci.project_checks: evidence destination: %v", err), FailureKind: FailureFatal}, nil
		}
		commandSpecs := capsuleCICommandSpecs(commands, pipeline)
		checks := make([]map[string]any, 0, len(commandSpecs))
		commandEvidence := make([]map[string]any, 0, len(commandSpecs))
		allPassed := true
		for _, check := range commandSpecs {
			command := strings.TrimSpace(fmt.Sprint(commands[check.key]))
			if command == "" || command == "<nil>" {
				continue
			}
			started := time.Now().UTC()
			runCtx := ctx
			cancel := func() {}
			if commandTimeout > 0 {
				runCtx, cancel = context.WithTimeout(ctx, commandTimeout)
			}
			log, exitCode, runErr := runner.Run(runCtx, workdir, command)
			cancel()
			outcome := "passed"
			if runErr != nil || exitCode != 0 {
				outcome = "failed"
				allPassed = false
			}
			checks = append(checks, map[string]any{"id": check.id, "kind": "deterministic", "outcome": outcome, "evidence": []string{evidenceRef + "#" + check.id}})
			entry := map[string]any{"id": check.id, "command": command, "exit_code": exitCode, "outcome": outcome, "started_at": started, "finished_at": time.Now().UTC(), "log": boundedCheckLog(log)}
			if runErr != nil {
				entry["error"] = runErr.Error()
				entry["error_kind"] = capsuleCICommandErrorKind(runErr)
			}
			commandEvidence = append(commandEvidence, entry)
		}
		outcome := "passed"
		summary := "All declared project test and build commands passed."
		promotionEligible := allPassed
		if len(checks) == 0 {
			outcome = "needs_input"
			summary = "Project profile declares no test or build command."
			promotionEligible = false
		} else if !allPassed {
			outcome = "failed"
			summary = "One or more declared project commands failed."
		}
		artifact := map[string]any{"schema": "capsule-ci-project-checks/v1", "job_id": capsuleCIStringArg(args, "job_id", ""), "pipeline": pipeline, "profile": filepath.ToSlash(filepath.Join(".kitsoki", "project-profile.yaml")), "checks": commandEvidence, "outcome": outcome}
		if err := writeCapsuleCIEvidence(evidencePath, artifact); err != nil {
			return Result{}, fmt.Errorf("host.capsule_ci.project_checks: write evidence: %w", err)
		}
		verdict := map[string]any{
			"schema":             "capsule-ci-verdict/v1",
			"pipeline":           pipeline,
			"outcome":            outcome,
			"summary":            summary,
			"checks":             checks,
			"promotion_eligible": promotionEligible,
			"source_digest":      capsuleCIStringArg(args, "source_digest", ""),
			"story_digest":       capsuleCIStringArg(args, "story_digest", ""),
			"environment_digest": capsuleCIStringArg(args, "environment_digest", ""),
			"envelope_digest":    capsuleCIStringArg(args, "envelope_digest", ""),
		}
		return Result{Data: map[string]any{"ok": allPassed && len(checks) > 0, "checks": checks, "evidence": evidenceRef, "verdict": verdict}}, nil
	}
}

type capsuleCICommandSpec struct {
	id  string
	key string
}

// capsuleCICommandSpecs makes the selected Capsule pipeline operational rather
// than a verdict label. A project may define commands.<pipeline> as its exact
// deterministic gate. Legacy change pipelines retain the test/build pair;
// missing full or release gates fail closed rather than silently weakening.
func capsuleCICommandSpecs(commands map[string]any, pipeline string) []capsuleCICommandSpec {
	pipeline = strings.TrimSpace(pipeline)
	if pipeline != "" {
		command := strings.TrimSpace(fmt.Sprint(commands[pipeline]))
		if command != "" && command != "<nil>" {
			return []capsuleCICommandSpec{{id: pipeline, key: pipeline}}
		}
		if pipeline != "change" {
			return nil
		}
	}
	return []capsuleCICommandSpec{{id: "tests", key: "test"}, {id: "build", key: "build"}}
}

func capsuleCIEvidencePath(workdir, jobID string, destination CapsuleCIEvidenceDestination) (string, string, error) {
	if destination.Root == "" && destination.ReferencePrefix == "" {
		rel := filepath.ToSlash(filepath.Join(".artifacts", "capsule-ci", "checks", jobID+".json"))
		return filepath.Join(workdir, filepath.FromSlash(rel)), "file:" + rel, nil
	}
	if destination.Root == "" || destination.ReferencePrefix == "" {
		return "", "", fmt.Errorf("root and reference prefix must be supplied together")
	}
	root, err := filepath.Abs(destination.Root)
	if err != nil {
		return "", "", err
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(destination.ReferencePrefix), "/")
	if !strings.HasPrefix(prefix, "file:") || strings.TrimPrefix(prefix, "file:") == "" || filepath.IsAbs(strings.TrimPrefix(prefix, "file:")) {
		return "", "", fmt.Errorf("reference prefix must be a non-absolute file: path")
	}
	return filepath.Join(root, jobID+".json"), prefix + "/" + jobID + ".json", nil
}

func capsuleCICommandTimeout(args map[string]any) (time.Duration, error) {
	raw := strings.TrimSpace(capsuleCIStringArg(args, "command_timeout", ""))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("must be a positive duration")
	}
	return d, nil
}

func capsuleCICommandErrorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "runner"
	}
}

var CapsuleCIProjectChecksHandler = NewCapsuleCIProjectChecksHandler(nil)

func capsuleCIStringArg(args map[string]any, key, fallback string) string {
	value := strings.TrimSpace(fmt.Sprint(args[key]))
	if value == "" || value == "<nil>" {
		return fallback
	}
	return value
}

func safeArtifactID(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}

func boundedCheckLog(value string) string {
	const max = 1 << 20
	if len(value) > max {
		return value[len(value)-max:]
	}
	return value
}

func writeCapsuleCIEvidence(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}
