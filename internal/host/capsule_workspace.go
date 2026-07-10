package host

import (
	"context"
	"fmt"
	"strings"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/project"
)

// CapsuleWorkspaceHandler is the explicit Capsule-backed workspace host
// surface. The historical git_worktree handler remains a compatibility alias
// until stories migrate their branch-argument contract to checked-in Capsule
// definitions.
func CapsuleWorkspaceHandler(ctx context.Context, args map[string]any) (Result, error) {
	op := strings.TrimSpace(worktreeStringArg(args, "op"))
	repo, repoErr := resolveWorktreeRepo(ctx, worktreeStringArg(args, "repo"))
	if repoErr != "" {
		return Result{Error: "capsule workspace: " + repoErr}, nil
	}
	manager, err := project.Open(repo, []string{"*"})
	if err != nil {
		return Result{Error: "capsule workspace: " + err.Error()}, nil
	}
	id := strings.TrimSpace(worktreeStringArg(args, "id"))
	owner := strings.TrimSpace(worktreeStringArg(args, "owner"))
	if owner == "" {
		owner = "host"
	}
	switch op {
	case "create":
		definition := strings.TrimSpace(worktreeStringArg(args, "definition"))
		if id == "" || definition == "" {
			return Result{Error: "capsule workspace.create: id and definition are required"}, nil
		}
		h, err := manager.Create(ctx, control.CreateRequest{ID: id, DefinitionID: definition, Owner: owner})
		if err != nil {
			return Result{Error: "capsule workspace.create: " + err.Error()}, nil
		}
		path, err := manager.WorkspacePath(ctx, h)
		if err != nil {
			return Result{Error: "capsule workspace.create: " + err.Error()}, nil
		}
		return Result{Data: map[string]any{"ok": true, "id": h.ID, "generation": h.Generation, "path": path}}, nil
	case "get":
		if id == "" {
			return Result{Error: "capsule workspace.get: id is required"}, nil
		}
		in, err := manager.Instances.Get(ctx, id)
		if err != nil {
			return Result{Error: "capsule workspace.get: " + err.Error()}, nil
		}
		path, err := manager.WorkspacePath(ctx, control.Handle{ID: in.ID, Generation: in.Generation})
		if err != nil {
			return Result{Error: "capsule workspace.get: " + err.Error()}, nil
		}
		return Result{Data: map[string]any{"id": in.ID, "generation": in.Generation, "path": path, "branch": in.Branch, "state": in.State}}, nil
	case "commit":
		message := worktreeStringArg(args, "message")
		in, err := manager.Instances.Get(ctx, id)
		if id == "" || err != nil {
			return Result{Error: "capsule workspace.commit: id is required and must exist"}, nil
		}
		h, err := manager.CommitVCS(ctx, control.Handle{ID: in.ID, Generation: in.Generation}, message)
		if err != nil {
			return Result{Error: "capsule workspace.commit: " + err.Error()}, nil
		}
		return Result{Data: map[string]any{"ok": true, "id": h.ID, "generation": h.Generation}}, nil
	case "close":
		in, err := manager.Instances.Get(ctx, id)
		if id == "" || err != nil {
			return Result{Error: "capsule workspace.close: id is required and must exist"}, nil
		}
		if err := manager.Close(ctx, control.Handle{ID: in.ID, Generation: in.Generation}, owner); err != nil {
			return Result{Error: "capsule workspace.close: " + err.Error()}, nil
		}
		return Result{Data: map[string]any{"ok": true, "id": id}}, nil
	default:
		return Result{Error: fmt.Sprintf("host.capsule_workspace: unknown op %q", op)}, nil
	}
}
