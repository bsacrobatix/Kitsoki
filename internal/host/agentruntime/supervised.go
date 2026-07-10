package agentruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Supervised struct{}

// activityBuffer retains command output while non-blockingly notifying the
// waiter that the child made progress. Claude's stream-json is written to
// stdout, so this turns real trace activity into a renewable liveness signal.
type activityBuffer struct {
	bytes.Buffer
	activity chan<- struct{}
}

func (b *activityBuffer) Write(p []byte) (int, error) {
	n, err := b.Buffer.Write(p)
	if n > 0 && b.activity != nil {
		select {
		case b.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

func NewSupervised() *Supervised { return &Supervised{} }

func (s *Supervised) Name() string { return "supervised" }

func (s *Supervised) Probe(context.Context) Capabilities {
	return Capabilities{
		Backend:  s.Name(),
		Strength: StrengthSupervised,
		Features: []string{
			"process_group",
			"timeout_cancel",
			"env_allowlist",
			"temp_home",
			"final_diff",
		},
	}
}

func (s *Supervised) Launch(ctx context.Context, spec LaunchSpec) (*Running, AppliedPolicy, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, AppliedPolicy{}, fmt.Errorf("agentruntime supervised: command is required")
	}
	tempRoot, err := os.MkdirTemp("", "kitsoki-agent-runtime-*")
	if err != nil {
		return nil, AppliedPolicy{}, fmt.Errorf("agentruntime supervised: temp root: %w", err)
	}
	home := filepath.Join(tempRoot, "home")
	cache := filepath.Join(tempRoot, "cache")
	for _, p := range []string{home, cache} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			os.RemoveAll(tempRoot)
			return nil, AppliedPolicy{}, fmt.Errorf("agentruntime supervised: mkdir %s: %w", p, err)
		}
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if spec.Resources.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Resources.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}

	cmd := exec.CommandContext(runCtx, spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdin = spec.Stdin
	cmd.Env = supervisedEnv(spec.Env, home, cache)
	applyProcessGroup(cmd)
	applyResourceLimits(cmd, spec.Resources)

	activity := make(chan struct{}, 1)
	var stdout, stderr activityBuffer
	stdout.activity = activity
	stderr.activity = activity
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(tempRoot)
		return nil, AppliedPolicy{}, fmt.Errorf("agentruntime supervised: start: %w", err)
	}

	policy := AppliedPolicy{
		Backend:      s.Name(),
		Strength:     StrengthSupervised,
		MinStrength:  spec.EffectiveMin(),
		Repo:         spec.EffectiveRepo(),
		RW:           append([]string(nil), spec.RW...),
		Hidden:       append([]string(nil), spec.Hidden...),
		Network:      spec.EffectiveNetwork(),
		Degrade:      spec.EffectiveDegrade(),
		TempHome:     home,
		TempCacheDir: cache,
		Degraded:     supervisedDegradations(spec),
	}

	wait := func(waitCtx context.Context) (Result, error) {
		defer cancel()
		defer os.RemoveAll(tempRoot)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var waitErr error
		killed := false
		var activityTimer *time.Timer
		var activityC <-chan time.Time
		if spec.Resources.ActivityTimeout > 0 {
			activityTimer = time.NewTimer(spec.Resources.ActivityTimeout)
			activityC = activityTimer.C
			defer activityTimer.Stop()
		}
		for {
			select {
			case waitErr = <-done:
				goto finished
			case <-activity:
				if activityTimer != nil {
					if !activityTimer.Stop() {
						select {
						case <-activityTimer.C:
						default:
						}
					}
					activityTimer.Reset(spec.Resources.ActivityTimeout)
				}
			case <-activityC:
				// stdout and the timer can become ready together. Prefer the
				// observed output: it was produced before the liveness decision
				// and must renew the deadline rather than losing a race to it.
				select {
				case <-activity:
					activityTimer.Reset(spec.Resources.ActivityTimeout)
					continue
				default:
				}
				killed = true
				killProcessGroup(cmd)
				waitErr = <-done
				goto finished
			case <-waitCtx.Done():
				killed = true
				killProcessGroup(cmd)
				waitErr = <-done
				goto finished
			case <-runCtx.Done():
				killed = true
				killProcessGroup(cmd)
				waitErr = <-done
				goto finished
			}
		}
	finished:
		exitCode := 0
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				return Result{}, waitErr
			}
		}
		diff := ""
		if strings.TrimSpace(spec.RepoRoot) != "" {
			diff = captureGitDiff(waitCtx, spec.RepoRoot)
		}
		return Result{
			Stdout:    stdout.String(),
			Stderr:    stderr.String(),
			ExitCode:  exitCode,
			Killed:    killed,
			Duration:  time.Since(start),
			FinalDiff: diff,
		}, nil
	}
	return NewRunning(policy, wait), policy, nil
}

func supervisedDegradations(spec LaunchSpec) []string {
	var out []string
	if spec.EffectiveMin().Satisfies(StrengthFSConfined) {
		out = append(out, "supervised backend does not satisfy filesystem confinement")
	}
	if spec.EffectiveRepo() == RepoReadOnly || len(spec.RW) > 0 || len(spec.Hidden) > 0 {
		out = append(out, "filesystem policy recorded but not enforced by supervised backend")
	}
	if spec.EffectiveNetwork() != NetworkInherit {
		out = append(out, "network policy recorded but not enforced by supervised backend")
	}
	return out
}

func supervisedEnv(base []string, home, cache string) []string {
	keep := map[string]bool{
		"PATH":               true,
		"TMPDIR":             true,
		"LANG":               true,
		"LC_ALL":             true,
		"KITSOKI_SESSION_ID": true,
	}
	out := make([]string, 0, len(keep)+8)
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if ok && (keep[k] || allowedProviderEnv(k)) {
			out = append(out, kv)
		}
	}
	out = append(out,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+cache,
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
	)
	return out
}

func allowedProviderEnv(k string) bool {
	prefixes := []string{
		"ANTHROPIC_",
		"OPENAI_",
		"CODEX_",
		"KITSOKI_",
		"GOOGLE_",
		"GEMINI_",
		"XAI_",
		"ZAI_",
		"GLM_",
		"HF_",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func captureGitDiff(ctx context.Context, repo string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "diff", "--")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return out.String()
}
