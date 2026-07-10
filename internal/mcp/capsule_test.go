package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/capsule/control"
	kitsokimcp "kitsoki/internal/mcp"
)

func connectCapsule(ctx context.Context, t *testing.T, srv *kitsokimcp.CapsuleServer) *mcpsdk.ClientSession {
	t.Helper()
	one, two := mcpsdk.NewInMemoryTransports()
	_, err := srv.Connect(ctx, one, nil)
	require.NoError(t, err)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, two, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}
func TestCapsuleMCPUsesOpaqueFreshWorkspaceHandles(t *testing.T) {
	project := t.TempDir()
	spec := filepath.Join(project, "capsules", "clean", "capsule.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(spec), 0o755))
	require.NoError(t, os.WriteFile(spec, []byte("name: clean\nsource:\n  synthetic: true\n  steps:\n    - action: write\n      path: initial.txt\n      content: initial\n    - action: commit\n      message: init\n"), 0o644))
	envSpec := filepath.Join(project, ".kitsoki", "environments", "ci.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(envSpec), 0o755))
	require.NoError(t, os.WriteFile(envSpec, []byte("schema: capsule-environment/v1\nid: ci\nnetwork: none\n"), 0o644))
	root := filepath.Join(project, ".capsules", "workspaces")
	manager := &control.Manager{Definitions: control.FileDefinitionStore{ProjectRoot: project}, Instances: control.FileInstanceStore{Root: root}, Providers: map[string]control.WorkspaceProvider{"synthetic": control.SyntheticProvider{ProjectRoot: project}}, Grant: control.ScopeGrant{ProjectRoot: project, WorkspaceRoots: []string{root}, Definitions: []string{"clean"}, Executors: []string{"synthetic"}, Effects: []string{"exec", "vcs_commit", "local_reconcile"}, Branches: []string{"main"}}}
	srv, err := kitsokimcp.NewCapsuleServer(kitsokimcp.CapsuleConfig{Manager: manager, Owner: "agent", ProjectID: "fixture"})
	require.NoError(t, err)
	ctx := context.Background()
	cs := connectCapsule(ctx, t, srv)
	env, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.env.resolve", Arguments: map[string]any{"id": "ci"}})
	require.NoError(t, err)
	require.False(t, env.IsError, contentText(env))
	require.Contains(t, contentText(env), `"id":"ci"`)
	created, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.workspace.create", Arguments: map[string]any{"id": "run", "definition": "clean"}})
	require.NoError(t, err)
	require.False(t, created.IsError, contentText(created))
	var body struct {
		Workspace control.Handle `json:"workspace"`
	}
	require.NoError(t, json.Unmarshal([]byte(contentText(created)), &body))
	require.NotEmpty(t, body.Workspace.ID)
	written, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.fs.write", Arguments: map[string]any{"workspace": body.Workspace, "path": "agent.txt", "contents": "safe"}})
	require.NoError(t, err)
	require.False(t, written.IsError, contentText(written))
	var changed struct {
		Workspace control.Handle `json:"workspace"`
	}
	require.NoError(t, json.Unmarshal([]byte(contentText(written)), &changed))
	require.Greater(t, changed.Workspace.Generation, body.Workspace.Generation)
	stale, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.fs.read", Arguments: map[string]any{"workspace": body.Workspace, "path": "agent.txt"}})
	require.NoError(t, err)
	require.True(t, stale.IsError, "stale generation must not retain authority")
	read, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.fs.read", Arguments: map[string]any{"workspace": changed.Workspace, "path": "agent.txt"}})
	require.NoError(t, err)
	require.False(t, read.IsError, contentText(read))
	require.Contains(t, contentText(read), "safe")
	require.NotContains(t, contentText(created), project, "machine path leaked through create")
	committed, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.vcs.commit", Arguments: map[string]any{"workspace": changed.Workspace, "message": "agent change"}})
	require.NoError(t, err)
	require.False(t, committed.IsError, contentText(committed))
	var committedHandle struct {
		Workspace control.Handle `json:"workspace"`
	}
	require.NoError(t, json.Unmarshal([]byte(contentText(committed)), &committedHandle))
	deniedPlan, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.sync.plan", Arguments: map[string]any{"workspace": committedHandle.Workspace, "operation": "integrate", "target": "release"}})
	require.NoError(t, err)
	require.True(t, deniedPlan.IsError, "ungranted branch must not be reconciled")
	deniedPublish, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.sync.plan", Arguments: map[string]any{"workspace": committedHandle.Workspace, "operation": "publish", "target": "main"}})
	require.NoError(t, err)
	require.True(t, deniedPublish.IsError, "publish requires an explicit remote_publish grant")
	planned, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.sync.plan", Arguments: map[string]any{"workspace": committedHandle.Workspace, "operation": "integrate", "target": "main"}})
	require.NoError(t, err)
	require.False(t, planned.IsError, contentText(planned))
	var plan struct {
		Plan struct {
			Digest string `json:"digest"`
		} `json:"plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(contentText(planned)), &plan))
	applied, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "capsule.sync.apply", Arguments: map[string]any{"plan_digest": plan.Plan.Digest}})
	require.NoError(t, err)
	require.False(t, applied.IsError, contentText(applied))
	var integrated struct {
		Workspace control.Handle `json:"workspace"`
	}
	require.NoError(t, json.Unmarshal([]byte(contentText(applied)), &integrated))
	require.Greater(t, integrated.Workspace.Generation, committedHandle.Workspace.Generation)
}
