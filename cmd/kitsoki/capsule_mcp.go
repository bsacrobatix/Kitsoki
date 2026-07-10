package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/storylauncher"
	kitsokimcp "kitsoki/internal/mcp"
)

// capsuleMCPCommand is intentionally separate from the broad Studio MCP
// surface. An agent launched with this command receives only project-scoped
// Capsule handles, never the server checkout or arbitrary host tools.
func capsuleMCPCommand() *cobra.Command {
	var project, pipeline, executor, owner string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start a project-scoped Capsule MCP server",
		Long: `Start a least-authority MCP server for one project's managed Capsule workspaces.

The startup scope is immutable: tool calls can narrow it but cannot add a
project, workspace root, definition, executor, command, remote, or credential.
This is the coding-agent MCP surface; it is intentionally separate from the
general-purpose Studio MCP server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, projectID, err := newCapsuleManager(project, executor)
			if err != nil {
				return err
			}
			if strings.TrimSpace(pipeline) == "" {
				pipeline = "default"
			}
			_ = pipeline // CI registration consumes this selected pipeline in the next slice.
			server, err := kitsokimcp.NewCapsuleServer(kitsokimcp.CapsuleConfig{Manager: manager, Owner: owner, ProjectID: projectID, CILauncher: func(path string) ci.Launcher { return storylauncher.Launcher{StoryPath: path} }})
			if err != nil {
				return err
			}
			return server.Run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "onboarded project root")
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "declared pipeline name (reserved for Capsule CI registration)")
	cmd.Flags().StringVar(&executor, "executor", "synthetic", "allowed executor provider (synthetic is currently implemented)")
	cmd.Flags().StringVar(&owner, "owner", "mcp-agent", "immutable lease owner recorded on created workspaces")
	return cmd
}

func newCapsuleManager(project, executor string) (*control.Manager, string, error) {
	root, err := filepath.Abs(project)
	if err != nil {
		return nil, "", err
	}
	defs := control.FileDefinitionStore{ProjectRoot: root}
	list, err := defs.List(nil)
	if err != nil {
		return nil, "", err
	}
	if len(list) == 0 {
		return nil, "", fmt.Errorf("capsule mcp: no definitions found under %s/.kitsoki/capsules or %s/capsules", root, root)
	}
	if executor != "" && executor != "local" && executor != "synthetic" {
		return nil, "", fmt.Errorf("capsule mcp: executor %q is not configured", executor)
	}
	definitionIDs := make([]string, 0, len(list))
	providers := map[string]control.WorkspaceProvider{"synthetic": control.SyntheticProvider{ProjectRoot: root}}
	allowedProviders := []string{"synthetic"}
	git := control.GitSourceProvider{ProjectRoot: root}
	for _, def := range list {
		definitionIDs = append(definitionIDs, def.ID)
		switch def.Source.Kind {
		case control.SourceSelf:
			providers["self"] = git
			providers["git"] = git
			allowedProviders = append(allowedProviders, "self")
		case control.SourcePinned:
			providers["pinned"] = git
			providers["git"] = git
			allowedProviders = append(allowedProviders, "pinned")
		}
	}
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	manager := &control.Manager{
		Definitions: defs,
		Instances:   control.FileInstanceStore{Root: workspaceRoot},
		Providers:   providers,
		Grant: control.ScopeGrant{
			ProjectRoot: root, WorkspaceRoots: []string{workspaceRoot},
			Definitions: definitionIDs, Executors: allowedProviders,
			Effects: []string{"exec", "vcs_commit", "local_reconcile", "env_write"},
		},
	}
	return manager, filepath.Base(root), nil
}
