// Package project assembles the generic project-scoped Capsule manager shared
// by CLI, MCP, and story host adapters.
package project

import (
	"fmt"
	"path/filepath"

	"kitsoki/internal/capsule/control"
)

func Open(root string, branches []string) (*control.Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	definitions := control.FileDefinitionStore{ProjectRoot: abs}
	list, err := definitions.List(nil)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("capsule project: no definitions found")
	}
	roots := []string{filepath.Join(abs, ".capsules", "workspaces")}
	providers := map[string]control.WorkspaceProvider{"synthetic": control.SyntheticProvider{ProjectRoot: abs}}
	executors := []string{"synthetic"}
	git := control.GitSourceProvider{ProjectRoot: abs}
	development := control.DevWorkspaceScriptProvider{ProjectRoot: abs}
	ids := make([]string, 0, len(list))
	for _, definition := range list {
		ids = append(ids, definition.ID)
		switch definition.Source.Kind {
		case control.SourceSelf:
			providers["self"], providers["git"] = git, git
			executors = append(executors, "self")
		case control.SourcePinned:
			providers["pinned"], providers["git"] = git, git
			executors = append(executors, "pinned")
		case control.SourceDevWorkspaceScript:
			providers[string(control.SourceDevWorkspaceScript)] = development
			executors = append(executors, string(control.SourceDevWorkspaceScript))
			if relative := definition.Source.Development.Root; relative != "" {
				candidate := filepath.Join(abs, relative)
				if !contains(roots, candidate) {
					roots = append(roots, candidate)
				}
			}
		}
	}
	return &control.Manager{Definitions: definitions, Instances: control.FileInstanceStore{Root: roots[0]}, Providers: providers, Grant: control.ScopeGrant{ProjectRoot: abs, WorkspaceRoots: roots, Definitions: ids, Executors: executors, Effects: []string{"exec", "vcs_commit", "local_reconcile", "env_write"}, Branches: branches}}, nil
}

func contains(paths []string, candidate string) bool {
	for _, path := range paths {
		if filepath.Clean(path) == filepath.Clean(candidate) {
			return true
		}
	}
	return false
}
