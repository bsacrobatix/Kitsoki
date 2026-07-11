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
	trace := capsuleWorkspaceTrace(op, repo, "", idOwner(args))
	if repoErr != "" {
		trace["error"] = repoErr
		trace["hint"] = "pass repo explicitly or run from inside the target checkout"
		return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace: " + repoErr}, nil
	}
	trace["repo"] = repo
	manager, err := project.Open(repo, []string{"*"})
	if err != nil {
		trace["error"] = err.Error()
		trace["hint"] = "ensure the project has at least one .kitsoki/capsules/<definition>.yaml file"
		return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace: " + err.Error()}, nil
	}
	id := strings.TrimSpace(worktreeStringArg(args, "id"))
	owner := strings.TrimSpace(worktreeStringArg(args, "owner"))
	if owner == "" {
		owner = "host"
	}
	trace["id"] = id
	trace["owner"] = owner
	switch op {
	case "create":
		definition := strings.TrimSpace(worktreeStringArg(args, "definition"))
		if definition == "" {
			definition = "development"
			trace["definition_defaulted"] = true
		}
		trace["definition"] = definition
		if id == "" {
			trace["error"] = "id is required"
			trace["hint"] = "pass a stable workspace id; generated dev-story instances usually use the session or ticket id"
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.create: id is required"}, nil
		}
		h, err := manager.Create(ctx, control.CreateRequest{ID: id, DefinitionID: definition, Owner: owner})
		if err != nil {
			trace["error"] = err.Error()
			trace["hint"] = "check .kitsoki/capsules/" + definition + ".yaml, the source provider, and .capsules/workspaces permissions"
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.create: " + err.Error()}, nil
		}
		data, err := capsuleWorkspaceStatusData(ctx, manager, h.ID, trace)
		if err != nil {
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.create: " + err.Error()}, nil
		}
		data["created"] = true
		return Result{Data: data}, nil
	case "get", "status", "sync":
		if id == "" {
			trace["error"] = "id is required"
			trace["hint"] = "bind and reuse the id returned by create"
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace." + op + ": id is required"}, nil
		}
		data, err := capsuleWorkspaceStatusData(ctx, manager, id, trace)
		if err != nil {
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace." + op + ": " + err.Error()}, nil
		}
		if op == "sync" {
			data["log"] = "capsule workspace ready; no git sync was performed"
		}
		return Result{Data: data}, nil
	case "commit":
		message := worktreeStringArg(args, "message")
		in, err := manager.Instances.Get(ctx, id)
		if id == "" || err != nil {
			trace["error"] = "id is required and must exist"
			trace["hint"] = "create the capsule workspace before committing it"
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.commit: id is required and must exist"}, nil
		}
		trace["definition"] = in.DefinitionID
		trace["generation"] = in.Generation
		trace["state"] = string(in.State)
		h, err := manager.CommitVCS(ctx, control.Handle{ID: in.ID, Generation: in.Generation}, message)
		if err != nil {
			trace["error"] = err.Error()
			trace["hint"] = "inspect dirty status and ensure there are staged or unstaged changes to commit"
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.commit: " + err.Error()}, nil
		}
		data, err := capsuleWorkspaceStatusData(ctx, manager, h.ID, trace)
		if err != nil {
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.commit: " + err.Error()}, nil
		}
		data["committed"] = true
		return Result{Data: data}, nil
	case "close":
		in, err := manager.Instances.Get(ctx, id)
		if id == "" || err != nil {
			trace["error"] = "id is required and must exist"
			trace["hint"] = "bind and reuse the id returned by create"
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.close: id is required and must exist"}, nil
		}
		trace["definition"] = in.DefinitionID
		trace["generation"] = in.Generation
		trace["state"] = string(in.State)
		if err := manager.Close(ctx, control.Handle{ID: in.ID, Generation: in.Generation}, owner); err != nil {
			trace["error"] = err.Error()
			trace["hint"] = "check the workspace lease owner and use the same owner that created it"
			return Result{Data: capsuleWorkspaceData(trace), Error: "capsule workspace.close: " + err.Error()}, nil
		}
		trace["closed"] = true
		return Result{Data: capsuleWorkspaceData(trace)}, nil
	default:
		trace["error"] = "unknown op"
		trace["hint"] = "valid ops: create, get, status, sync, commit, close"
		return Result{Data: capsuleWorkspaceData(trace), Error: fmt.Sprintf("host.capsule_workspace: unknown op %q", op)}, nil
	}
}

func idOwner(args map[string]any) string {
	if owner := strings.TrimSpace(worktreeStringArg(args, "owner")); owner != "" {
		return owner
	}
	return "host"
}

func capsuleWorkspaceTrace(op, repo, definition, owner string) map[string]any {
	return map[string]any{
		"handler":    "host.capsule_workspace",
		"op":         op,
		"repo":       repo,
		"definition": definition,
		"owner":      owner,
	}
}

func capsuleWorkspaceData(trace map[string]any) map[string]any {
	data := map[string]any{"ok": trace["error"] == nil, "diagnostics": trace}
	for _, key := range []string{"id", "generation", "path", "branch", "state", "head", "dirty", "definition", "provider", "source_ref", "closed"} {
		if value, ok := trace[key]; ok {
			data[key] = value
		}
	}
	return data
}

func capsuleWorkspaceStatusData(ctx context.Context, manager *control.Manager, id string, trace map[string]any) (map[string]any, error) {
	in, err := manager.Instances.Get(ctx, id)
	if err != nil {
		trace["error"] = err.Error()
		trace["hint"] = "create the capsule workspace or check whether it was already closed"
		return nil, err
	}
	h := control.Handle{ID: in.ID, Generation: in.Generation}
	path, err := manager.WorkspacePath(ctx, h)
	if err != nil {
		trace["error"] = err.Error()
		trace["hint"] = "check the workspace root in the capsule definition and .capsules instance metadata"
		return nil, err
	}
	trace["id"] = in.ID
	trace["generation"] = in.Generation
	trace["path"] = path
	trace["branch"] = in.Branch
	trace["state"] = string(in.State)
	trace["definition"] = in.DefinitionID
	trace["provider"] = in.Provider
	trace["source_ref"] = in.SourceRef
	trace["head"] = in.Head
	status, err := manager.StatusVCS(ctx, h)
	if err != nil {
		trace["vcs_status_error"] = err.Error()
	} else {
		trace["branch"] = status.Branch
		trace["head"] = status.Head
		trace["dirty"] = status.Dirty
		trace["porcelain"] = strings.TrimSpace(status.Porcelain)
	}
	return capsuleWorkspaceData(trace), nil
}
