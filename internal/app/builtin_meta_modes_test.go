package app

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInjectBuiltinMetaModes_FillsBugWhenAbsent asserts the injection
// step adds the `bug` builtin when the app didn't declare one.
func TestInjectBuiltinMetaModes_FillsBugWhenAbsent(t *testing.T) {
	def := &AppDef{}
	injectBuiltinMetaModes(def)

	bug, ok := def.MetaModes["bug"]
	require.True(t, ok, "bug builtin must be injected")
	require.Equal(t, "bug-reporter", bug.Agent)
	require.Equal(t, "bug", bug.Trigger)
	require.Equal(t, "onpath", bug.ExitIntentOrDefault())
}

// TestInjectBuiltinMetaModes_AppOverrideWins asserts that an app-declared
// mode with the same name as a builtin is preserved verbatim — the
// builtin does not silently replace it.
func TestInjectBuiltinMetaModes_AppOverrideWins(t *testing.T) {
	custom := &MetaModeDef{
		Trigger: "report",
		Agent:   "my-custom-agent",
	}
	def := &AppDef{
		MetaModes: map[string]*MetaModeDef{
			"bug": custom,
		},
	}
	injectBuiltinMetaModes(def)

	got := def.MetaModes["bug"]
	require.Same(t, custom, got, "app-declared `bug` must survive injection unchanged")
	require.Equal(t, "my-custom-agent", got.Agent)
}

// TestInjectBuiltinMetaModes_SelfRequiresEnvVar asserts the `self`
// builtin is only injected when KITSOKI_REPO is set. Test runs both
// branches by toggling the env var around the call.
func TestInjectBuiltinMetaModes_SelfRequiresEnvVar(t *testing.T) {
	// Save and restore the env var so the test doesn't leak.
	original, hadOriginal := os.LookupEnv("KITSOKI_REPO")
	t.Cleanup(func() {
		if hadOriginal {
			_ = os.Setenv("KITSOKI_REPO", original)
		} else {
			_ = os.Unsetenv("KITSOKI_REPO")
		}
	})

	// Branch 1: KITSOKI_REPO unset — `self` is omitted.
	_ = os.Unsetenv("KITSOKI_REPO")
	def := &AppDef{}
	injectBuiltinMetaModes(def)
	_, hasSelf := def.MetaModes["self"]
	require.False(t, hasSelf, "self must NOT be injected when KITSOKI_REPO is unset")

	// Branch 2: KITSOKI_REPO set — `self` is present with the expected cwd.
	_ = os.Setenv("KITSOKI_REPO", "/tmp/fake-repo")
	def = &AppDef{}
	injectBuiltinMetaModes(def)
	self, ok := def.MetaModes["self"]
	require.True(t, ok, "self MUST be injected when KITSOKI_REPO is set")
	require.Equal(t, "kitsoki-engineer", self.Agent)
	require.Equal(t, "${KITSOKI_REPO}", self.Cwd, "cwd stays in unexpanded form; loader's validateMetaModes does the expansion")
}

// TestInjectBuiltinMetaModes_NilDef is a defensive no-crash check.
func TestInjectBuiltinMetaModes_NilDef(t *testing.T) {
	injectBuiltinMetaModes(nil) // must not panic
}
