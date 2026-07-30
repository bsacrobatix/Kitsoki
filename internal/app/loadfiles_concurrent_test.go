package app

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// concurrentClosureYAML builds a minimal single-file closure whose sole
// agent's `cwd:` AND an agent_plugins env entry both reference
// `${KITSOKI_APP_DIR}` — the exact macro LoadFromFiles used to resolve via a
// process-global os.Setenv (see loadfiles.go). cwd: expansion
// (expandMetaCwdWith) and agent_plugins/providers env expansion
// (expandEnvVar) are two SEPARATE code paths that both read AppDef.envLookup;
// exercising both here catches a regression that only de-globalises one of
// them (see loader_agent_plugins.go / loader_providers.go). agentName is
// baked into both the app id and the agent key so two closures built by this
// helper never collide when loaded concurrently.
func concurrentClosureYAML(agentName string) string {
	return fmt.Sprintf(`app:
  id: loadfiles-concurrent-%[1]s
  version: 0.1.0

root: foyer

states:
  foyer:
    view: "Foyer."

agents:
  %[1]s:
    system_prompt: "you are an engineer"
    cwd: "${KITSOKI_APP_DIR}"

agent_plugins:
  agent.claude:
    plugin: builtin.claude_cli
    env:
      WORKDIR: "${KITSOKI_APP_DIR}/tools"
`, agentName)
}

// TestLoadFromFiles_NoEnvMutation is the regression test for the
// KITSOKI_APP_DIR process-global removed from LoadFromFiles: it must resolve
// `${KITSOKI_APP_DIR}` against its OWN materialised temp tree without ever
// calling os.Setenv, in both directions — the var must come back exactly as
// it went in, whether that was "unset" or "set to something unrelated".
func TestLoadFromFiles_NoEnvMutation(t *testing.T) {
	files := map[string][]byte{"app.yaml": []byte(concurrentClosureYAML("engineer"))}

	t.Run("was unset, stays unset", func(t *testing.T) {
		prev, hadPrev := os.LookupEnv("KITSOKI_APP_DIR")
		require.NoError(t, os.Unsetenv("KITSOKI_APP_DIR"))
		t.Cleanup(func() {
			if hadPrev {
				_ = os.Setenv("KITSOKI_APP_DIR", prev)
			}
		})

		def, cleanup, err := LoadFromFiles(files, "app.yaml")
		require.NoError(t, err)
		defer cleanup()

		agent, ok := def.Agents["engineer"]
		require.True(t, ok, "engineer agent must be present")
		require.Equal(t, def.BaseDir, agent.Cwd,
			"agent cwd must resolve to this load's own materialised tree (def.BaseDir)")

		plugin, ok := def.AgentPlugins["agent.claude"]
		require.True(t, ok, "agent.claude plugin decl must be present")
		require.Equal(t, def.BaseDir+"/tools", plugin.Env["WORKDIR"],
			"agent_plugins env ${KITSOKI_APP_DIR} must resolve to this load's own materialised tree (def.BaseDir), not the process env")

		_, isSet := os.LookupEnv("KITSOKI_APP_DIR")
		require.False(t, isSet,
			"LoadFromFiles must not publish KITSOKI_APP_DIR as a process env var")
	})

	t.Run("was set, stays untouched", func(t *testing.T) {
		t.Setenv("KITSOKI_APP_DIR", "/some/unrelated/ambient/path")

		def, cleanup, err := LoadFromFiles(files, "app.yaml")
		require.NoError(t, err)
		defer cleanup()

		agent, ok := def.Agents["engineer"]
		require.True(t, ok, "engineer agent must be present")
		require.Equal(t, def.BaseDir, agent.Cwd,
			"agent cwd must resolve to this load's own materialised tree, not the ambient env var")
		require.NotEqual(t, "/some/unrelated/ambient/path", agent.Cwd,
			"the ambient KITSOKI_APP_DIR must NOT leak into the resolved cwd")

		plugin, ok := def.AgentPlugins["agent.claude"]
		require.True(t, ok, "agent.claude plugin decl must be present")
		require.Equal(t, def.BaseDir+"/tools", plugin.Env["WORKDIR"],
			"agent_plugins env ${KITSOKI_APP_DIR} must resolve to this load's own materialised tree, not the ambient env var")
		require.NotEqual(t, "/some/unrelated/ambient/path/tools", plugin.Env["WORKDIR"],
			"the ambient KITSOKI_APP_DIR must NOT leak into agent_plugins env expansion")

		require.Equal(t, "/some/unrelated/ambient/path", os.Getenv("KITSOKI_APP_DIR"),
			"LoadFromFiles must not overwrite a pre-existing KITSOKI_APP_DIR")
	})
}

// TestLoadFromFiles_ConcurrentRevisions loads two distinct closures from two
// goroutines at once and asserts each resolves `${KITSOKI_APP_DIR}` against
// its OWN temp tree. Before this slice, both loads raced on the same
// process-global env var (os.Setenv in one goroutine could stomp the other's
// value mid-load, or MkdirTemp interleaving could see either goroutine's
// value); a passing run under `go test -race` shows the two loads no longer
// share any mutable state.
func TestLoadFromFiles_ConcurrentRevisions(t *testing.T) {
	filesA := map[string][]byte{"app.yaml": []byte(concurrentClosureYAML("engineer-a"))}
	filesB := map[string][]byte{"app.yaml": []byte(concurrentClosureYAML("engineer-b"))}

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	run := func(files map[string][]byte, agentName string) {
		defer wg.Done()
		def, cleanup, err := LoadFromFiles(files, "app.yaml")
		if err != nil {
			errs <- fmt.Errorf("%s: load: %w", agentName, err)
			return
		}
		defer cleanup()

		agent, ok := def.Agents[agentName]
		if !ok {
			errs <- fmt.Errorf("%s: agent missing from loaded def", agentName)
			return
		}
		if agent.Cwd != def.BaseDir {
			errs <- fmt.Errorf("%s: resolved cwd %q != own BaseDir %q (cross-revision contamination)",
				agentName, agent.Cwd, def.BaseDir)
		}

		plugin, ok := def.AgentPlugins["agent.claude"]
		if !ok {
			errs <- fmt.Errorf("%s: agent.claude plugin decl missing from loaded def", agentName)
			return
		}
		if want := def.BaseDir + "/tools"; plugin.Env["WORKDIR"] != want {
			errs <- fmt.Errorf("%s: agent_plugins env WORKDIR %q != own BaseDir/tools %q (cross-revision contamination)",
				agentName, plugin.Env["WORKDIR"], want)
		}
	}

	wg.Add(2)
	go run(filesA, "engineer-a")
	go run(filesB, "engineer-b")
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}
