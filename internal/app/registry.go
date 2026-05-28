package app

import "kitsoki/internal/agents"

// AgentSpecs converts def.Agents into the loader-side BuildSpec values
// consumed by agents.BuildRegistry. The conversion assumes the loader has
// already resolved system_prompt_path into SystemPrompt, env-expanded Cwd,
// and normalised Tools to fully-qualified form (host.x.y).
//
// A nil or empty Agents map returns nil so callers can pass the result
// straight to agents.BuildRegistry, which treats nil as "builtins only".
func (def *AppDef) AgentSpecs() []agents.BuildSpec {
	if def == nil || len(def.Agents) == 0 {
		return nil
	}
	specs := make([]agents.BuildSpec, 0, len(def.Agents))
	for _, name := range sortedKeys(def.Agents) {
		d := def.Agents[name]
		if d == nil {
			continue
		}
		specs = append(specs, agents.BuildSpec{
			Name:         name,
			SystemPrompt: d.SystemPrompt,
			Model:        d.Model,
			Tools:        append([]string(nil), d.Tools...),
			DefaultCwd:   d.Cwd,
		})
	}
	return specs
}
