package agents

import "fmt"

// BuildSpec is the loader-side declaration of an agent. The internal/agents
// package accepts this shape from internal/app via BuildRegistry so the
// registry package doesn't need to depend on internal/app (and so YAML and
// file-path concerns stay one-way out of the registry).
//
// All fields are expected to be in their final, resolved form: SystemPrompt
// is the literal prompt text (not a path), Tools are fully-qualified
// host.x.y names, and DefaultCwd is already env-expanded.
type BuildSpec struct {
	Name         string
	SystemPrompt string
	Model        string
	Tools        []string
	DefaultCwd   string
}

// BuildRegistry returns a Registry seeded with the builtins (see
// NewBuiltins) and then overridden by the provided specs. Specs whose
// Name matches a builtin REPLACE that builtin for the lifetime of the
// returned registry; new names are added.
//
// Each spec must have a non-empty Name and SystemPrompt; either being
// empty is an error. Tools and DefaultCwd are passed through unchanged.
func BuildRegistry(specs []BuildSpec) (Registry, error) {
	r := NewBuiltins()
	for i, s := range specs {
		if s.Name == "" {
			return nil, fmt.Errorf("agents.BuildRegistry: specs[%d] has empty Name", i)
		}
		if s.SystemPrompt == "" {
			return nil, fmt.Errorf("agents.BuildRegistry: agent %q has empty SystemPrompt", s.Name)
		}
		r.Register(Agent{
			Name:         s.Name,
			SystemPrompt: s.SystemPrompt,
			Model:        s.Model,
			Tools:        append([]string(nil), s.Tools...),
			DefaultCwd:   s.DefaultCwd,
		})
	}
	return r, nil
}
