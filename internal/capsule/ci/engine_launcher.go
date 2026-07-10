package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/app"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/harness"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// EngineLauncher drives a self-contained declared CI story through Kitsoki's
// real state machine. The story owns checks, agent review, and parked/human
// paths; this launcher owns only the normalized envelope input and terminal
// typed verdict extraction.
type EngineLauncher struct {
	StoryPath      string
	ConfigureHosts func(*host.Registry) error
}

func (l EngineLauncher) Launch(ctx context.Context, prepared executor.Prepared) (Verdict, error) {
	if l.StoryPath == "" {
		return Verdict{}, fmt.Errorf("capsule ci: story path is required")
	}
	path, err := filepath.Abs(l.StoryPath)
	if err != nil {
		return Verdict{}, err
	}
	def, err := app.Load(path)
	if err != nil {
		return Verdict{}, fmt.Errorf("capsule ci: load story: %w", err)
	}
	m, err := machine.New(def)
	if err != nil {
		return Verdict{}, err
	}
	s, err := store.OpenMemory()
	if err != nil {
		return Verdict{}, err
	}
	defer s.Close()
	reg := host.NewRegistry()
	host.RegisterBuiltins(reg)
	host.RegisterStarlarkBindings(reg, def.StarlarkHostBindings)
	if l.ConfigureHosts != nil {
		if err := l.ConfigureHosts(reg); err != nil {
			return Verdict{}, err
		}
	}
	if err := reg.ValidateAllowList(def.Hosts); err != nil {
		return Verdict{}, err
	}
	orch := orchestrator.New(def, m, s, ciDirectHarness{}, orchestrator.WithHostRegistry(reg))
	out, err := orch.OneShot(ctx, orchestrator.OneShotInput{State: app.StatePath(fmt.Sprint(def.Root)), Intent: "run", World: envelopeWorld(prepared.Envelope)})
	if err != nil {
		return Verdict{}, err
	}
	raw, ok := out.WorldAfter["ci_verdict"]
	if !ok || raw == nil {
		return Verdict{}, fmt.Errorf("capsule ci: story %s did not emit ci_verdict after run (state %s)", filepath.ToSlash(l.StoryPath), out.NextState)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Verdict{}, err
	}
	var verdict Verdict
	if err := json.Unmarshal(encoded, &verdict); err != nil {
		return Verdict{}, fmt.Errorf("capsule ci: parse story verdict: %w", err)
	}
	return verdict, nil
}
func envelopeWorld(e executor.Envelope) map[string]any {
	trigger := make(map[string]any, len(e.Trigger)+1)
	for key, value := range e.Trigger {
		trigger[key] = value
	}
	trigger["envelope_digest"] = e.Digest
	trigger["story_digest"] = e.StoryDigest
	return map[string]any{"ci_job_id": e.JobID, "ci_pipeline": trigger["requested_pipeline"], "ci_trigger": trigger, "ci_source": map[string]any{"digest": e.SourceDigest}, "ci_workspace": map[string]any{"id": e.Instance.ID, "generation": e.Instance.Generation}, "ci_environment": map[string]any{"id": e.Environment.ID, "digest": e.Environment.Digest}, "ci_policy": map[string]any{"network": e.Policy.Network, "external_write": e.Policy.ExternalWrite}}
}

type ciDirectHarness struct{}

func (ciDirectHarness) RunTurn(context.Context, harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, fmt.Errorf("capsule ci: direct story invoked harness unexpectedly")
}
func (ciDirectHarness) Close() error { return nil }
