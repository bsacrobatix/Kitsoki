package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
)

const Schema = "capsule-ci/v1"
const VerdictSchema = "capsule-ci-verdict/v1"

type Config struct {
	Schema             string              `yaml:"schema" json:"schema"`
	ProjectProfile     string              `yaml:"project_profile,omitempty" json:"project_profile,omitempty"`
	DefaultEnvironment string              `yaml:"default_environment,omitempty" json:"default_environment,omitempty"`
	Pipelines          map[string]Pipeline `yaml:"pipelines" json:"pipelines"`
	Remotes            map[string]Remote   `yaml:"remotes,omitempty" json:"remotes,omitempty"`
}
type Pipeline struct {
	Story       string         `yaml:"story" json:"story"`
	Triggers    []string       `yaml:"triggers" json:"triggers"`
	Environment string         `yaml:"environment,omitempty" json:"environment,omitempty"`
	Executor    string         `yaml:"executor,omitempty" json:"executor,omitempty"`
	Mode        string         `yaml:"mode,omitempty" json:"mode,omitempty"`
	Required    bool           `yaml:"required,omitempty" json:"required,omitempty"`
	Permissions Permissions    `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Agents      Agents         `yaml:"agents,omitempty" json:"agents,omitempty"`
	Result      ResultContract `yaml:"result" json:"result"`
}
type Permissions struct {
	Network       string `yaml:"network,omitempty" json:"network,omitempty"`
	ExternalWrite string `yaml:"external_write,omitempty" json:"external_write,omitempty"`
}
type Agents struct {
	Policy        string   `yaml:"policy,omitempty" json:"policy,omitempty"`
	Profiles      []string `yaml:"profiles,omitempty" json:"profiles,omitempty"`
	MaxCostUSD    float64  `yaml:"max_cost_usd,omitempty" json:"max_cost_usd,omitempty"`
	OnUnavailable string   `yaml:"on_unavailable,omitempty" json:"on_unavailable,omitempty"`
}
type ResultContract struct {
	Schema    string   `yaml:"schema,omitempty" json:"schema,omitempty"`
	PassExits []string `yaml:"pass_exits,omitempty" json:"pass_exits,omitempty"`
	FailExits []string `yaml:"fail_exits,omitempty" json:"fail_exits,omitempty"`
	ParkExits []string `yaml:"park_exits,omitempty" json:"park_exits,omitempty"`
}
type Remote struct {
	Endpoint      string `yaml:"endpoint" json:"endpoint"`
	CredentialEnv string `yaml:"credential_env,omitempty" json:"credential_env,omitempty"`
}
type Trigger struct {
	Kind              string   `json:"kind"`
	Provider          string   `json:"provider,omitempty"`
	EventID           string   `json:"event_id,omitempty"`
	Actor             string   `json:"actor,omitempty"`
	Ref               string   `json:"ref,omitempty"`
	BaseRef           string   `json:"base_ref,omitempty"`
	HeadSHA           string   `json:"head_sha,omitempty"`
	ChangedPaths      []string `json:"changed_paths,omitempty"`
	RequestedPipeline string   `json:"requested_pipeline,omitempty"`
}
type Check struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Outcome     string   `json:"outcome"`
	Evidence    []string `json:"evidence,omitempty"`
	DecisionRef string   `json:"decision_ref,omitempty"`
}
type Verdict struct {
	Schema            string  `json:"schema"`
	Pipeline          string  `json:"pipeline"`
	Outcome           string  `json:"outcome"`
	Summary           string  `json:"summary,omitempty"`
	Checks            []Check `json:"checks"`
	PromotionEligible bool    `json:"promotion_eligible"`
	SourceDigest      string  `json:"source_digest,omitempty"`
	StoryDigest       string  `json:"story_digest,omitempty"`
	EnvironmentDigest string  `json:"environment_digest,omitempty"`
	EnvelopeDigest    string  `json:"envelope_digest,omitempty"`
}

func Load(project string) (Config, error) {
	path := filepath.Join(project, ".kitsoki", "ci.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("capsule ci: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("capsule ci: parse %s: %w", path, err)
	}
	if err := Validate(project, cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
func Validate(project string, cfg Config) error {
	if cfg.Schema != Schema {
		return fmt.Errorf("capsule ci: schema %q, want %q", cfg.Schema, Schema)
	}
	if len(cfg.Pipelines) == 0 {
		return fmt.Errorf("capsule ci: pipelines are required")
	}
	for name, remote := range cfg.Remotes {
		if strings.TrimSpace(name) == "" || isBuiltinExecutor(name) {
			return fmt.Errorf("capsule ci remote %q: invalid executor name", name)
		}
		u, err := url.Parse(remote.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("capsule ci remote %q: endpoint must be https", name)
		}
		if remote.CredentialEnv != "" && !validEnvName(remote.CredentialEnv) {
			return fmt.Errorf("capsule ci remote %q: invalid credential env", name)
		}
	}
	for name, p := range cfg.Pipelines {
		if strings.TrimSpace(p.Story) == "" || filepath.IsAbs(p.Story) || strings.HasPrefix(filepath.Clean(p.Story), "..") {
			return fmt.Errorf("capsule ci pipeline %q: story must be project-relative", name)
		}
		if _, err := os.Stat(filepath.Join(project, p.Story)); err != nil {
			return fmt.Errorf("capsule ci pipeline %q: story: %w", name, err)
		}
		env := p.Environment
		if env == "" {
			env = cfg.DefaultEnvironment
		}
		if env == "" {
			return fmt.Errorf("capsule ci pipeline %q: environment is required", name)
		}
		if _, err := environment.Load(project, env); err != nil {
			return fmt.Errorf("capsule ci pipeline %q: %w", name, err)
		}
		if p.Mode != "" && p.Mode != "one-shot" && p.Mode != "staged" {
			return fmt.Errorf("capsule ci pipeline %q: invalid mode", name)
		}
		if p.Permissions.Network != "" && p.Permissions.Network != "none" && p.Permissions.Network != "replay" && p.Permissions.Network != "live" {
			return fmt.Errorf("capsule ci pipeline %q: invalid network", name)
		}
		if p.Agents.Policy == "allow" && (len(p.Agents.Profiles) == 0 || p.Agents.MaxCostUSD <= 0 || p.Agents.OnUnavailable == "") {
			return fmt.Errorf("capsule ci pipeline %q: allowed agents require profiles, budget, and fallback", name)
		}
		if p.Executor != "" && !isBuiltinExecutor(p.Executor) {
			if _, ok := cfg.Remotes[p.Executor]; !ok {
				return fmt.Errorf("capsule ci pipeline %q: executor %q is not configured", name, p.Executor)
			}
		}
	}
	return nil
}
func ValidateVerdict(v Verdict, expected executor.Envelope, contract ResultContract) error {
	if v.Schema != VerdictSchema {
		return fmt.Errorf("capsule ci: verdict schema %q", v.Schema)
	}
	if v.Pipeline == "" || v.Outcome == "" {
		return fmt.Errorf("capsule ci: verdict pipeline and outcome are required")
	}
	switch v.Outcome {
	case "passed", "failed", "needs_input", "cancelled", "infra_failed":
	default:
		return fmt.Errorf("capsule ci: invalid verdict outcome %q", v.Outcome)
	}
	seen := map[string]bool{}
	for _, check := range v.Checks {
		if check.ID == "" || seen[check.ID] {
			return fmt.Errorf("capsule ci: checks require unique ids")
		}
		seen[check.ID] = true
		if check.Outcome == "passed" && len(check.Evidence) == 0 && check.DecisionRef == "" {
			return fmt.Errorf("capsule ci: passed check %q has no evidence", check.ID)
		}
	}
	if v.SourceDigest != expected.SourceDigest || v.StoryDigest != expected.StoryDigest || v.EnvironmentDigest != expected.Environment.Digest || v.EnvelopeDigest != expected.Digest {
		return fmt.Errorf("capsule ci: verdict digest mismatch")
	}
	derived := v.Outcome == "passed"
	for _, c := range v.Checks {
		if c.Outcome != "passed" {
			derived = false
		}
	}
	if v.PromotionEligible != derived {
		return fmt.Errorf("capsule ci: promotion eligibility is derived, not caller-controlled")
	}
	return nil
}

// Launcher is a story adapter. Production adapters start the selected Kitsoki
// story and return its terminal typed artifact; tests use a no-LLM fake.
type Launcher interface {
	Launch(context.Context, executor.Prepared) (Verdict, error)
}

// ExecutorSelector chooses a named checked-in pipeline placement. The selector
// is injected at the front door so stories cannot swap providers or acquire
// remote authority while running.
type ExecutorSelector interface {
	Select(context.Context, string) (executor.Provider, error)
}

type ExecutorSelectorFunc func(context.Context, string) (executor.Provider, error)

func (f ExecutorSelectorFunc) Select(ctx context.Context, name string) (executor.Provider, error) {
	return f(ctx, name)
}

type Service struct {
	ProjectRoot string
	Jobs        artifactjob.Store
	Env         environment.Resolver
	Provider    executor.Provider
	Executors   ExecutorSelector
	Launcher    Launcher
}
type RunRequest struct {
	Pipeline         string
	Workspace        control.Handle
	DefinitionDigest string
	SourceDigest     string
	StoryDigest      string
	Trigger          Trigger
}
type RunResult struct {
	Job       artifactjob.Job   `json:"job"`
	Envelope  executor.Envelope `json:"envelope"`
	Verdict   Verdict           `json:"verdict"`
	Execution executor.Result   `json:"execution"`
}

func (s Service) Plan(ctx context.Context, req RunRequest) (Pipeline, executor.Envelope, error) {
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return Pipeline{}, executor.Envelope{}, err
	}
	s.ProjectRoot = root
	cfg, err := Load(s.ProjectRoot)
	if err != nil {
		return Pipeline{}, executor.Envelope{}, err
	}
	p, ok := cfg.Pipelines[req.Pipeline]
	if !ok {
		return Pipeline{}, executor.Envelope{}, fmt.Errorf("capsule ci: pipeline %q not found", req.Pipeline)
	}
	if req.Trigger.Kind == "" {
		req.Trigger.Kind = "local"
	}
	if !contains(p.Triggers, req.Trigger.Kind) {
		return Pipeline{}, executor.Envelope{}, fmt.Errorf("capsule ci: trigger %q not allowed", req.Trigger.Kind)
	}
	envID := p.Environment
	if envID == "" {
		envID = cfg.DefaultEnvironment
	}
	s.Env.ProjectRoot = s.ProjectRoot
	lock, err := s.Env.Resolve(ctx, envID)
	if err != nil {
		return Pipeline{}, executor.Envelope{}, err
	}
	e, err := executor.Seal(executor.Envelope{JobID: "pending", ProjectID: filepath.Base(s.ProjectRoot), DefinitionDigest: req.DefinitionDigest, Instance: req.Workspace, SourceDigest: req.SourceDigest, StoryPath: p.Story, StoryDigest: req.StoryDigest, Environment: lock, Trigger: triggerMap(req.Trigger), Policy: executor.Policy{Network: defaultNetwork(p.Permissions.Network), MinimumSandbox: lock.Sandbox, ExternalWrite: defaultExternal(p.Permissions.ExternalWrite)}})
	return p, e, err
}
func (s Service) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if s.Jobs == nil || (s.Provider == nil && s.Executors == nil) {
		return RunResult{}, fmt.Errorf("capsule ci: jobs and provider are required")
	}
	p, envelope, err := s.Plan(ctx, req)
	if err != nil {
		return RunResult{}, err
	}
	job, err := s.Jobs.Register(ctx, artifactjob.RegisterRequest{AppID: "capsule-ci", Story: envelope.StoryPath, Origin: artifactjob.Origin{Kind: req.Trigger.Kind, Ref: req.Trigger.Ref}, WorkspaceInstanceID: artifactjob.InstanceID(req.Workspace.ID), Owner: "capsule-ci"})
	if err != nil {
		return RunResult{}, err
	}
	envelope.JobID = string(job.ID)
	envelope, err = executor.Seal(envelope)
	if err != nil {
		return RunResult{}, err
	}
	provider := s.Provider
	if s.Executors != nil {
		provider, err = s.Executors.Select(ctx, p.Executor)
		if err != nil {
			return RunResult{}, err
		}
	}
	if provider == nil {
		return RunResult{}, fmt.Errorf("capsule ci: no provider for executor %q", p.Executor)
	}
	prepared, err := provider.Prepare(ctx, envelope)
	if err != nil {
		return RunResult{}, err
	}
	var verdict Verdict
	execution, runErr := provider.Run(ctx, prepared, func(ctx context.Context, prepared executor.Prepared) (executor.Result, error) {
		if s.Launcher == nil {
			return executor.Result{}, fmt.Errorf("capsule ci: no local launcher is configured")
		}
		v, e := s.Launcher.Launch(ctx, prepared)
		if e != nil {
			return executor.Result{}, e
		}
		if e = ValidateVerdict(v, prepared.Envelope, p.Result); e != nil {
			return executor.Result{}, e
		}
		raw, e := json.Marshal(v)
		if e != nil {
			return executor.Result{}, e
		}
		verdict = v
		return executor.Result{VerdictArtifact: "verdict:" + hashVerdict(v), VerdictJSON: raw}, nil
	}, nil)
	if runErr == nil && len(execution.VerdictJSON) > 0 {
		if err := json.Unmarshal(execution.VerdictJSON, &verdict); err != nil {
			runErr = fmt.Errorf("capsule ci: parse executor verdict: %w", err)
		}
	}
	if runErr == nil {
		if err := ValidateVerdict(verdict, prepared.Envelope, p.Result); err != nil {
			runErr = err
		}
	}
	status := artifactjob.StatusDone
	if runErr != nil || verdict.Outcome == "infra_failed" {
		status = artifactjob.StatusFailed
	}
	if verdict.Outcome == "needs_input" {
		status = artifactjob.StatusAwaitingInput
	}
	summary := verdict.Summary
	_, _ = s.Jobs.Update(ctx, job.ID, artifactjob.Update{Status: &status, Summary: &summary, TerminalArtifactHandle: &execution.VerdictArtifact})
	if runErr != nil {
		return RunResult{Job: job, Envelope: envelope, Execution: execution}, runErr
	}
	job, _ = s.Jobs.Get(ctx, job.ID)
	return RunResult{Job: job, Envelope: envelope, Verdict: verdict, Execution: execution}, nil
}
func contains(in []string, want string) bool {
	for _, v := range in {
		if v == want {
			return true
		}
	}
	return false
}
func defaultNetwork(v string) string {
	if v == "" {
		return "none"
	}
	return v
}
func defaultExternal(v string) string {
	if v == "" {
		return "deny"
	}
	return v
}
func triggerMap(t Trigger) map[string]any {
	raw, _ := json.Marshal(t)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func hashVerdict(v Verdict) string {
	v.PromotionEligible = false
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func isBuiltinExecutor(name string) bool {
	switch name {
	case "", "host", "local", "remote-fake":
		return true
	default:
		return false
	}
}
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || ('A' <= r && r <= 'Z') || (i > 0 && '0' <= r && r <= '9') {
			continue
		}
		return false
	}
	return true
}

var _ = sort.Strings
