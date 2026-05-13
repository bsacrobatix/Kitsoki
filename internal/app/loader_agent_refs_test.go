package app

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAgentRefs_MetaMode_Builtin asserts that a meta_modes[*].agent
// pointing at a builtin (story-author) loads cleanly without an explicit
// agents: declaration.
func TestAgentRefs_MetaMode_Builtin(t *testing.T) {
	yaml := `app:
  id: ref-builtin
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
meta_modes:
  story:
    trigger: meta
    agent: story-author
`
	_, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
}

// TestAgentRefs_MetaMode_Declared asserts that a meta_modes[*].agent
// pointing at an agent declared in the same AppDef resolves.
func TestAgentRefs_MetaMode_Declared(t *testing.T) {
	yaml := `app:
  id: ref-declared
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
agents:
  my-agent:
    system_prompt: "fixture"
meta_modes:
  story:
    trigger: meta
    agent: my-agent
`
	_, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
}

// TestAgentRefs_MetaMode_Unknown asserts that an unknown agent name on
// meta_modes[*].agent fails load with an error mentioning both the
// offending name and the known-agents list.
func TestAgentRefs_MetaMode_Unknown(t *testing.T) {
	yaml := `app:
  id: ref-unknown
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
agents:
  alpha:
    system_prompt: "a"
  beta:
    system_prompt: "b"
meta_modes:
  story:
    trigger: meta
    agent: ghost
`
	_, err := LoadBytes([]byte(yaml))
	require.Error(t, err)
	require.True(t, containsSubstring(err, `agent reference "ghost"`),
		"error must include the offending name; got: %v", err)
	require.True(t, containsSubstring(err, "meta_modes.story.agent"),
		"error must include the site; got: %v", err)
	require.True(t, containsSubstring(err, "known agents:"),
		"error must include known-agents list prefix; got: %v", err)
	// The known list is sorted; with builtins (story-author) plus
	// declared (alpha, beta), it must contain each name.
	for _, want := range []string{"alpha", "beta", "story-author"} {
		require.True(t, containsSubstring(err, want),
			"known-agents list must contain %q; got: %v", want, err)
	}
}

// TestAgentRefs_OffPath_Unknown asserts that an unknown off_path.agent
// produces the same shape of error as the meta-mode case.
func TestAgentRefs_OffPath_Unknown(t *testing.T) {
	yaml := `app:
  id: ref-offpath
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
off_path:
  trigger: help
  agent: phantom
`
	_, err := LoadBytes([]byte(yaml))
	require.Error(t, err)
	require.True(t, containsSubstring(err, `agent reference "phantom"`),
		"error must include the offending name; got: %v", err)
	require.True(t, containsSubstring(err, "off_path.agent"),
		"error must include the off_path site; got: %v", err)
	require.True(t, containsSubstring(err, "known agents:"),
		"error must include known-agents list; got: %v", err)
}

// TestAgentRefs_OffPath_Declared asserts that off_path.agent resolves
// against an agent declared in the same AppDef.
func TestAgentRefs_OffPath_Declared(t *testing.T) {
	yaml := `app:
  id: ref-offpath-ok
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
agents:
  helper:
    system_prompt: "ok"
off_path:
  trigger: help
  agent: helper
`
	_, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
}

// TestAgentRefs_OffPath_Builtin asserts that off_path.agent can point at
// a builtin without declaring it.
func TestAgentRefs_OffPath_Builtin(t *testing.T) {
	yaml := `app:
  id: ref-offpath-builtin
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
off_path:
  trigger: help
  agent: story-author
`
	_, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
}

// TestAgentRefs_IncludesMergeFirst asserts that the cross-reference
// validator sees agents declared in INCLUDED files, not just the main
// file. This exercises the WS-A6 merge order: includes are resolved
// before validation runs.
func TestAgentRefs_IncludesMergeFirst(t *testing.T) {
	dir := t.TempDir()
	mainYAML := `app:
  id: ref-include
  version: 0.1.0
include: [extras/*.yaml]
root: foyer
states:
  foyer:
    view: "Foyer."
meta_modes:
  bug:
    trigger: report-bug
    agent: bug-reporter
`
	extra := `agents:
  bug-reporter:
    system_prompt: "fixture bug-reporter from include"
`
	require.NoError(t, os.WriteFile(dir+"/main.yaml", []byte(mainYAML), 0644))
	require.NoError(t, os.MkdirAll(dir+"/extras", 0755))
	require.NoError(t, os.WriteFile(dir+"/extras/agents.yaml", []byte(extra), 0644))

	// The bug-reporter agent is declared only in the include; the main
	// file references it from meta_modes. If validation ran before
	// merging, this would fail. It must succeed.
	_, err := Load(dir + "/main.yaml")
	require.NoError(t, err)
}
