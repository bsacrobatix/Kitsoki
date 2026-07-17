// agent_mode_test.go — CLI + registry wiring tests for the `agent:<name>`
// virtual story path (agent mode): the unified `agent list` catalog, the
// loadAppWithEnv head-check, session create/continue over the scheme (each
// invocation re-resolves + re-synthesizes the root, proving resume works),
// and the registry's loadStory head-check + ListAgents catalog.
//
// No LLM anywhere: the project fixture agent is read-only (conversational
// off-ramp shape, no launch-policy preflight, no schema materialization) and
// the diverted converse turn runs the stubbed fake-agent.sh binary.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/agentroot"
	"kitsoki/internal/baseskills"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

// withProjectAgent isolates the process in a temp project that defines one
// read-only TOML agent named "demo" under .kitsoki/agents. HOME is redirected
// to a private dir so a developer's real ~/.codex/agents (and the default
// trace/db homes) never leak in, and the embedded agent library seam is
// stubbed to "not staged" so the catalog is deterministic (compiled-in
// builtins remain visible by design). Returns the resolved working dir.
func withProjectAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)

	agentsDir := filepath.Join(dir, ".kitsoki", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	const demoTOML = `name = "demo"
description = "Demo read-only agent"
developer_instructions = "You answer questions about the project."
sandbox_mode = "read-only"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "demo.toml"), []byte(demoTOML), 0o600))

	prev := materializeBuiltInAgentLibrary
	materializeBuiltInAgentLibrary = func(context.Context) (string, error) { return "", baseskills.ErrNotStaged }
	t.Cleanup(func() { materializeBuiltInAgentLibrary = prev })

	t.Chdir(dir)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	return cwd
}

// runAgentModeCLI executes an isolated root carrying the agent-mode-relevant
// commands (mirrors runKitsoki, which does not mount agentCmd).
func runAgentModeCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "kitsoki"}
	root.AddCommand(agentCmd())
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	root.SetContext(context.Background())
	err := root.Execute()
	return out.String(), err
}

// TestAgentList_JSON proves `agent list --json` emits the merged catalog:
// the project TOML agent (source project, effect read) alongside the
// compiled-in builtins, sorted by name.
func TestAgentList_JSON(t *testing.T) {
	withProjectAgent(t)

	stdout, err := runAgentModeCLI(t, "agent", "list", "--json")
	require.NoError(t, err)

	type catalogRow struct {
		Name        string   `json:"name"`
		Source      string   `json:"source"`
		Description string   `json:"description"`
		Effect      string   `json:"effect"`
		Shadows     []string `json:"shadows"`
	}
	var rows []catalogRow
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows))
	require.NotEmpty(t, rows)

	var demo *catalogRow
	builtinSeen := false
	lastName := ""
	for i := range rows {
		require.LessOrEqual(t, lastName, rows[i].Name, "catalog must be sorted by name")
		lastName = rows[i].Name
		if rows[i].Name == "demo" {
			demo = &rows[i]
		}
		if rows[i].Source == "builtin" {
			builtinSeen = true
		}
	}
	require.NotNil(t, demo, "project agent must appear in the catalog: %s", stdout)
	assert.Equal(t, "project", demo.Source)
	assert.Equal(t, "read", demo.Effect)
	assert.Equal(t, "Demo read-only agent", demo.Description)
	assert.True(t, builtinSeen, "builtin registry agents must appear in the merged catalog")

	// Table mode smoke: header + the project row.
	table, err := runAgentModeCLI(t, "agent", "list")
	require.NoError(t, err)
	assert.Contains(t, table, "NAME")
	assert.Contains(t, table, "demo")
}

// TestAgentRun_ArgumentValidation covers `agent run`'s guard rails: the
// delegating sugar must demand a name before it ever reaches the TUI run
// path, and must reject names the agent: scheme could not address.
func TestAgentRun_ArgumentValidation(t *testing.T) {
	_, err := runAgentModeCLI(t, "agent", "run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent name required")

	_, err = runAgentModeCLI(t, "agent", "run", "--continue")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent name required")

	_, err = runAgentModeCLI(t, "agent", "run", "bad/name")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid agent name")
}

// TestAgentRun_RunFlagPassthrough proves `kitsoki agent run <name>` is real
// sugar for `kitsoki run agent:<name>`: the delegation re-dispatches through
// a full newRootCmd() tree, so the root's PERSISTENT flags (--kitsoki-repo,
// --staged, --semantic-routing) parse instead of dying in a detached run
// parser, and a conventional leading `--` separator is accepted. A bogus
// agent name pins the expected failure point: agent resolution ("not
// found"), never flag parsing or the name-required guard.
func TestAgentRun_RunFlagPassthrough(t *testing.T) {
	withProjectAgent(t)
	// The root's PersistentPreRunE exports these; scope the mutation to this
	// test so the process env is restored afterwards.
	t.Setenv("KITSOKI_REPO", "")
	t.Setenv("KITSOKI_KIT_STAGED", "")

	repoOverride := t.TempDir()
	for _, args := range [][]string{
		{"agent", "run", "no-such-agent-xyz", "--semantic-routing"},
		{"agent", "run", "no-such-agent-xyz", "--kitsoki-repo", repoOverride},
		{"agent", "run", "no-such-agent-xyz", "--staged"},
		{"agent", "run", "--", "no-such-agent-xyz"},
	} {
		_, err := runAgentModeCLI(t, args...)
		require.Error(t, err, "args: %v", args)
		assert.NotContains(t, err.Error(), "unknown flag", "args: %v", args)
		assert.NotContains(t, err.Error(), "agent name required", "args: %v", args)
		assert.Contains(t, err.Error(), "not found", "args: %v", args)
	}
}

// TestResolveDefaultFlowsGlob_AgentSchemeRequiresFlows pins `kitsoki test
// flows agent:<name>` without --flows to a fatal error instead of silently
// globbing whatever ./flows/*.yaml the cwd happens to contain
// (docs/guide/agents/agent-mode.md documents --flows as required for the
// scheme). Ordinary file paths keep the derived default glob.
func TestResolveDefaultFlowsGlob_AgentSchemeRequiresFlows(t *testing.T) {
	_, err := resolveDefaultFlowsGlob("agent:demo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--flows")

	glob, err := resolveDefaultFlowsGlob(filepath.Join("testdata", "apps", "cloak", "app.yaml"))
	require.NoError(t, err)
	assert.NotEmpty(t, glob)
}

// TestLoadAppWithEnv_AgentScheme proves the loader head-check: an
// `agent:<name>` arg synthesizes the one-room root in memory (no file on
// disk), publishes the stable KITSOKI_APP_DIR (the working dir), and an
// unknown name fails with the catalog named — same UX as a bad story path.
func TestLoadAppWithEnv_AgentScheme(t *testing.T) {
	cwd := withProjectAgent(t)

	def, err := loadAppWithEnv("agent:demo")
	require.NoError(t, err)
	assert.Equal(t, "agent:demo", def.App.ID)
	assert.NotEmpty(t, def.App.Version, "version must carry the definition content hash")
	require.NotNil(t, def.States[agentroot.RoomName], "synthesized root must carry the agent room")
	assert.Equal(t, cwd, os.Getenv(host.AppDirEnv), "agent scheme must publish the working dir as the stable app dir")

	_, err = loadAppWithEnv("agent:definitely-not-a-known-agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found", "unknown agent must fail with the catalog UX")
}

// TestSessionCreateContinue_AgentScheme is the resume round-trip: create a
// session bound to agent:demo, then show and continue it via SEPARATE CLI
// invocations — each one re-resolves the definition and re-synthesizes the
// root from the scheme, exactly what a process restart does. The continue
// turn submits the synthesized agent_discuss capture intent, which the
// orchestrator diverts to the off-ramp conversation lane; the agent
// subprocess is stubbed with fake-agent.sh (no LLM).
func TestSessionCreateContinue_AgentScheme(t *testing.T) {
	fakeAgent, err := filepath.Abs(filepath.Join("..", "..", "internal", "host", "testdata", "fake-agent.sh"))
	require.NoError(t, err)
	withProjectAgent(t)
	t.Setenv(host.AgentBinEnv, fakeAgent)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	stdout, err := runKitsoki(t, "session", "create",
		"--app", "agent:demo",
		"--db", dbPath,
		"--key", "test:AGENT-1",
	)
	require.NoError(t, err)
	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &created))
	assert.Equal(t, "agent:demo", created["app_id"], "the app_id IS the agent binding")
	require.NotEmpty(t, created["session_id"])

	// Reopen 1: show re-synthesizes the root and reads the persisted session.
	stdout, err = runKitsoki(t, "session", "show",
		"--app", "agent:demo",
		"--db", dbPath,
		"--key", "test:AGENT-1",
	)
	require.NoError(t, err)
	var shown map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &shown))
	assert.Equal(t, agentroot.RoomName, shown["state"], "fresh agent session rests in the agent room")

	// Reopen 2: continue re-synthesizes again and runs one diverted
	// conversation turn against the stubbed agent binary.
	stdout, err = runKitsoki(t, "session", "continue",
		"--app", "agent:demo",
		"--db", dbPath,
		"--key", "test:AGENT-1",
		"--intent", agentroot.RoomName+"_discuss",
		"--slots", `{"message":"hello there"}`,
	)
	require.NoError(t, err)
	var outcome map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &outcome))
	assert.Equal(t, "offpath", outcome["mode"], "conversation lane must divert, not transition: %v", outcome)
	assert.Equal(t, agentroot.RoomName, outcome["new_state"], "conversational turn must not move the session")
	view, _ := outcome["view"].(string)
	assert.Contains(t, view, "hello there", "the stubbed agent must answer the diverted free text")
}

// TestTestFlows_AgentScheme proves design §1's flow surface: `kitsoki test
// flows agent:<name> --flows <fixture.yaml>` loads the synthesized root
// through the testrunner's agent-scheme head check and runs the fixture
// against it. Only the passing path runs through the CLI — testFlowsCmd
// os.Exits on failure, so the harness-level negative paths are pinned by
// internal/testrunner's TestRunFlows_AgentScheme instead.
func TestTestFlows_AgentScheme(t *testing.T) {
	dir := withProjectAgent(t)

	flowsDir := filepath.Join(dir, "flows")
	require.NoError(t, os.MkdirAll(flowsDir, 0o755))
	const discussFlow = `test_kind: flow
app: agent:demo
initial_state: agent
initial_world: {}

turns:
  - intent:
      name: agent_discuss
      slots:
        message: "what can you tell me about this project?"
    expect_state: agent
    expect_world_unchanged: true

expect_no_errors: true
`
	flowPath := filepath.Join(flowsDir, "agent_discuss.yaml")
	require.NoError(t, os.WriteFile(flowPath, []byte(discussFlow), 0o600))

	root := &cobra.Command{Use: "kitsoki"}
	root.AddCommand(testCmd())
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"test", "flows", "agent:demo", "--flows", flowPath})
	root.SetContext(context.Background())
	require.NoError(t, root.Execute(), "stderr: %s", errBuf.String())
}

// TestRegistryNewSession_AgentScheme proves the web/daemon path: the
// registry's loadStory head-check synthesizes the agent root for
// `runstatus.session.new {story_path: "agent:demo"}` with zero RPC contract
// change, and ListAgents serves the runstatus.agents.list catalog.
func TestRegistryNewSession_AgentScheme(t *testing.T) {
	withProjectAgent(t)
	reg := NewRegistry(webconfig.WebConfig{}, []string{"stories"}, deterministicBase(t))
	t.Cleanup(reg.Close)

	ctx := context.Background()
	sid, err := reg.NewSession(ctx, "agent:demo")
	require.NoError(t, err)
	require.NotEmpty(t, sid)

	entry, ok := reg.Get(sid)
	require.True(t, ok, "the returned id must resolve via Get")
	snap, err := entry.Source.Snapshot()
	require.NoError(t, err)
	assert.Equal(t, "agent:demo", snap.App.App.ID)
	assert.Equal(t, agentroot.RoomName, snap.Session.CurrentState)

	// Staleness must treat the synthesized root like the implicit root: no
	// on-disk file to diff, never stale.
	stale, _, err := reg.Staleness(ctx, sid)
	require.NoError(t, err)
	assert.False(t, stale, "synthesized agent roots have no on-disk bytes to drift")

	agents, err := reg.ListAgents()
	require.NoError(t, err)
	found := false
	for _, a := range agents {
		if a.Name == "demo" {
			found = true
			assert.Equal(t, "project", a.Source)
			assert.Equal(t, "read", a.Effect)
			assert.Equal(t, "agent:demo", a.StoryPath, "catalog rows carry the ready-to-use story path")
		}
	}
	assert.True(t, found, "ListAgents must include the project agent: %+v", agents)
}

// TestRegistryAgentScheme_UsesInjectedLaunchPolicy proves the web/daemon
// synthesis-time write/external preflight is gated by the registry's
// INJECTED policy (runtimeBase.AgentLaunchPolicy — resolved once at server
// startup from the `kitsoki web --config` file), NOT by whatever .kitsoki.yaml
// happens to sit in the daemon's cwd. Both directions are pinned: a denying
// cwd file must not block a session the injected policy allows, and an
// injected denial must gate a write agent even when the cwd file would allow
// it.
func TestRegistryAgentScheme_UsesInjectedLaunchPolicy(t *testing.T) {
	dir := withProjectAgent(t)

	// A project TOML with no sandbox_mode resolves to the full write surface
	// — exactly the effect class the preflight gates (read agents are exempt).
	const writerTOML = `name = "writer"
description = "Demo write agent"
developer_instructions = "You edit files in the project."
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki", "agents", "writer.toml"), []byte(writerTOML), 0o600))
	elsewhere := filepath.Join(dir, "elsewhere")
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))

	// writeCwdPolicy plants a cwd .kitsoki.yaml whose agent_launch_policy
	// only allows sessions under allowedRoot. The registry must never read it.
	writeCwdPolicy := func(t *testing.T, allowedRoot string) {
		t.Helper()
		cfg := "agent_launch_policy:\n  enabled: true\n  allowed_roots:\n    - " + allowedRoot + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki.yaml"), []byte(cfg), 0o600))
	}

	t.Run("denying cwd config is ignored when the injected policy allows", func(t *testing.T) {
		writeCwdPolicy(t, elsewhere)                                         // would deny a session in dir
		reg := NewRegistry(webconfig.WebConfig{}, nil, deterministicBase(t)) // zero policy: allow
		t.Cleanup(reg.Close)
		sid, err := reg.NewSession(context.Background(), "agent:writer")
		require.NoError(t, err, "the cwd .kitsoki.yaml must not gate the server's preflight")
		require.NotEmpty(t, sid)
	})

	t.Run("injected denial gates the write agent despite an allowing cwd config", func(t *testing.T) {
		writeCwdPolicy(t, dir) // would allow a session in dir
		base := deterministicBase(t)
		base.AgentLaunchPolicy = host.AgentLaunchPolicy{Enabled: true, AllowedRoots: []string{elsewhere}}
		reg := NewRegistry(webconfig.WebConfig{}, nil, base)
		t.Cleanup(reg.Close)
		_, err := reg.NewSession(context.Background(), "agent:writer")
		require.Error(t, err, "the injected policy must run the synthesis-time preflight")
		assert.Contains(t, err.Error(), "agent mode preflight")
	})
}

// TestLoadAgentSchemeApp_ProtectedRootAutoCapsule proves the protected-root
// auto-capsule exception end to end through loadAgentSchemeAppWithPolicy (the
// shared CLI + web/daemon registry path): a write agent started from a
// protected checkout materializes a managed capsule workspace (stable
// agent-mode-<name> id, agent-mode owner) and the synthesized root's workdir
// — the dir every dispatch reads — is the capsule, not the protected root.
func TestLoadAgentSchemeApp_ProtectedRootAutoCapsule(t *testing.T) {
	dir := withProjectAgent(t)
	const writerTOML = `name = "writer"
description = "Demo write agent"
developer_instructions = "You edit files in the project."
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki", "agents", "writer.toml"), []byte(writerTOML), 0o600))

	capsuleDir := t.TempDir()
	var gotRoot, gotID, gotOwner string
	prev := createProtectedRootAgentModeCapsule
	createProtectedRootAgentModeCapsule = func(_ context.Context, projectRoot, id, owner string) (control.Instance, error) {
		gotRoot, gotID, gotOwner = projectRoot, id, owner
		return control.Instance{ID: id, Path: capsuleDir, Branch: "agent/" + id}, nil
	}
	t.Cleanup(func() { createProtectedRootAgentModeCapsule = prev })

	policy := host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{dir}}
	def, err := loadAgentSchemeAppWithPolicy("writer", policy)
	require.NoError(t, err, "a protected-root write agent must auto-provision, not deny")

	require.Equal(t, "agent-mode-writer", gotID)
	require.Equal(t, agentModeCapsuleOwner, gotOwner)
	// The policy hands the provisioner the RESOLVED protected root (macOS
	// /var → /private/var), so compare symlink-resolved paths.
	resolvedDir, rerr := filepath.EvalSymlinks(dir)
	require.NoError(t, rerr)
	require.Equal(t, resolvedDir, filepath.Clean(gotRoot))

	workdir, ok := def.World["workdir"]
	require.True(t, ok, "write agents synthesize a workdir world var")
	resolvedCapsule, rerr := filepath.EvalSymlinks(capsuleDir)
	require.NoError(t, rerr)
	gotWorkdir, _ := workdir.Default.(string)
	resolvedWorkdir, rerr := filepath.EvalSymlinks(gotWorkdir)
	require.NoError(t, rerr)
	assert.Equal(t, resolvedCapsule, resolvedWorkdir, "dispatch workdir must be the capsule")
	assert.Equal(t, gotWorkdir, os.Getenv(host.AppDirEnv), "KITSOKI_APP_DIR must publish the capsule dir")
}

// TestLoadAgentSchemeApp_ProtectedRootProvisioningFailureDenies pins the
// degraded path: when capsule materialization fails, the operator gets the
// standard auditable denial with the provisioning failure appended — never a
// silent allow.
func TestLoadAgentSchemeApp_ProtectedRootProvisioningFailureDenies(t *testing.T) {
	dir := withProjectAgent(t)
	const writerTOML = `name = "writer"
description = "Demo write agent"
developer_instructions = "You edit files in the project."
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki", "agents", "writer.toml"), []byte(writerTOML), 0o600))

	prev := createProtectedRootAgentModeCapsule
	createProtectedRootAgentModeCapsule = func(context.Context, string, string, string) (control.Instance, error) {
		return control.Instance{}, errors.New("no development capsule definition")
	}
	t.Cleanup(func() { createProtectedRootAgentModeCapsule = prev })

	policy := host.AgentLaunchPolicy{Enabled: true, ProtectedRoots: []string{dir}}
	_, err := loadAgentSchemeAppWithPolicy("writer", policy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent mode preflight")
	assert.Contains(t, err.Error(), "auto-capsule provisioning")
	assert.Contains(t, err.Error(), "no development capsule definition")
}
