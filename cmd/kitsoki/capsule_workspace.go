package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"kitsoki/internal/capsule/control"
)

// capsuleWorkspaceCmd is the operator/automation CLI counterpart to the
// handle-scoped MCP lifecycle. It retains current script compatibility while
// letting any onboarded project use the native manager directly.
func capsuleWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "workspace", Short: "Create and manage native Capsule workspaces"}
	cmd.AddCommand(capsuleWorkspaceCreateCmd(), capsuleWorkspaceListCmd(), capsuleWorkspaceStatusCmd(), capsuleWorkspaceCommitCmd(), capsuleWorkspaceIntegrateCmd(), capsuleWorkspaceCloseCmd())
	return cmd
}
func capsuleWorkspaceManager(project string) (*control.Manager, error) {
	m, _, err := newCapsuleManager(project, "local", []string{"*"})
	return m, err
}
func capsuleWorkspaceCreateCmd() *cobra.Command {
	var project, id, definition, owner string
	var jsonOut bool
	cmd := &cobra.Command{Use: "create", Short: "Create or reacquire a managed Capsule workspace", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		h, err := m.Create(cmd.Context(), control.CreateRequest{ID: id, DefinitionID: definition, Owner: owner})
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, h, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&definition, "definition", "", "capsule definition id")
	cmd.Flags().StringVar(&owner, "owner", "cli", "lease owner")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("definition")
	return cmd
}
func capsuleWorkspaceListCmd() *cobra.Command {
	var project string
	var jsonOut bool
	cmd := &cobra.Command{Use: "list", Short: "List managed Capsule workspaces", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		all, err := m.List(cmd.Context())
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, map[string]any{"workspaces": all}, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}
func capsuleWorkspaceStatusCmd() *cobra.Command {
	var project, id string
	var jsonOut bool
	cmd := &cobra.Command{Use: "status", Short: "Show a managed Capsule workspace", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		in, err := m.Instances.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, in, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
func capsuleWorkspaceCommitCmd() *cobra.Command {
	var project, id, message string
	var jsonOut bool
	cmd := &cobra.Command{Use: "commit", Short: "Commit all local workspace changes", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		in, err := m.Instances.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		h, err := m.CommitVCS(cmd.Context(), control.Handle{ID: in.ID, Generation: in.Generation}, message)
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, h, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&message, "message", "", "commit message")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("message")
	return cmd
}

func capsuleWorkspaceIntegrateCmd() *cobra.Command {
	var project, id, gate, owner string
	var teardown, jsonOut bool
	cmd := &cobra.Command{Use: "integrate", Short: "Integrate a provider-managed workspace through its declared protected lifecycle", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		in, err := m.Instances.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		h, err := m.Integrate(cmd.Context(), control.Handle{ID: in.ID, Generation: in.Generation}, gate)
		if err != nil {
			return err
		}
		if teardown {
			if err := m.Close(cmd.Context(), h, owner); err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, map[string]any{"integrated": id, "closed": true}, jsonOut)
		}
		return capsuleWorkspaceWrite(cmd, h, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&gate, "gate", "", "focused validation command declared by the development adapter")
	cmd.Flags().StringVar(&owner, "owner", "cli", "lease owner required when tearing down")
	cmd.Flags().BoolVar(&teardown, "teardown", false, "close the workspace after successful integration")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
func capsuleWorkspaceCloseCmd() *cobra.Command {
	var project, id, owner string
	var jsonOut bool
	cmd := &cobra.Command{Use: "close", Short: "Close a managed Capsule workspace", RunE: func(cmd *cobra.Command, args []string) error {
		m, err := capsuleWorkspaceManager(project)
		if err != nil {
			return err
		}
		in, err := m.Instances.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		if err := m.Close(cmd.Context(), control.Handle{ID: in.ID, Generation: in.Generation}, owner); err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, map[string]any{"closed": id}, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&id, "id", "", "workspace id")
	cmd.Flags().StringVar(&owner, "owner", "cli", "lease owner")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
func capsuleWorkspaceWrite(cmd *cobra.Command, v any, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
	}
	fmt.Fprintln(cmd.OutOrStdout(), v)
	return nil
}
