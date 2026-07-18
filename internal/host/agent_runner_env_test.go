package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnvWithKitsokiBinOnPath_PrependsKitsokiDir verifies the helper
// prepends os.Executable()'s directory onto PATH so an agent invoking
// `kitsoki bug create …` via Bash can actually reach the binary,
// regardless of whether kitsoki was launched via `go run`, `go
// install`, or a packaged binary.
func TestEnvWithKitsokiBinOnPath_PrependsKitsokiDir(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)
	wantDir := filepath.Dir(self)

	got := envWithKitsokiBinOnPath([]string{"PATH=/usr/local/bin:/usr/bin", "HOME=/home/x"})
	pathLine := findPathLine(t, got)
	require.True(t, strings.HasPrefix(pathLine, "PATH="+wantDir+string(os.PathListSeparator)),
		"PATH must lead with kitsoki's own dir; got %q", pathLine)
	require.True(t, strings.Contains(pathLine, "/usr/local/bin"),
		"existing PATH entries must survive; got %q", pathLine)
	require.Contains(t, got, "HOME=/home/x", "unrelated env vars pass through unchanged")
}

// TestEnvWithKitsokiBinOnPath_Idempotent ensures repeated calls don't
// stack the kitsoki dir on the front of PATH N times.
func TestEnvWithKitsokiBinOnPath_Idempotent(t *testing.T) {
	once := envWithKitsokiBinOnPath([]string{"PATH=/usr/local/bin"})
	twice := envWithKitsokiBinOnPath(once)
	require.Equal(t, findPathLine(t, once), findPathLine(t, twice),
		"second pass must not re-prepend; PATH lines should match exactly")
}

// TestEnvWithKitsokiBinOnPath_NoPATH adds a PATH entry when the input
// env has none. This is a safety net for tests / harnesses that strip
// the environment before exec.
func TestEnvWithKitsokiBinOnPath_NoPATH(t *testing.T) {
	self, _ := os.Executable()
	wantDir := filepath.Dir(self)
	got := envWithKitsokiBinOnPath([]string{"HOME=/home/x"})
	require.Contains(t, got, "PATH="+wantDir,
		"a PATH entry must be synthesised when the input env has none")
}

// findPathLine is a tiny helper that extracts the PATH= line out of a
// KEY=value-style env slice. Fails the test if there isn't exactly one.
func findPathLine(t *testing.T, env []string) string {
	t.Helper()
	var matches []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			matches = append(matches, kv)
		}
	}
	require.Len(t, matches, 1, "expected exactly one PATH entry, got %v", matches)
	return matches[0]
}

// TestEnvWithShimPassThrough_MarksApprovedLaunch pins the launch-policy shim
// contract for kitsoki-managed backend forks: the subprocess env must carry
// KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE=1 so a PATH-resolved .kitsoki/bin shim
// passes through to the real binary instead of re-gating the dispatch as a
// raw operator launch (protected-root read-only denial).
func TestEnvWithShimPassThrough_MarksApprovedLaunch(t *testing.T) {
	got := envWithShimPassThrough([]string{"PATH=/usr/bin", "HOME=/home/x"})
	require.Contains(t, got, agentLaunchShimActiveEnv+"=1")
	require.Contains(t, got, "HOME=/home/x")
}

// TestEnvWithShimPassThrough_ResetsInheritedDepth pins the depth reset: a
// kitsoki session that was itself started through the shims inherits a
// positive depth, and forwarding it would trip the shim's recursion guard
// when more than one repo's shim dir sits on PATH. Every kitsoki-managed
// fork starts a fresh approved chain at depth 1, with exactly one marker
// entry in the env.
func TestEnvWithShimPassThrough_ResetsInheritedDepth(t *testing.T) {
	for _, inherited := range []string{"0", "2", "3"} {
		got := envWithShimPassThrough([]string{
			"PATH=/usr/bin",
			agentLaunchShimActiveEnv + "=" + inherited,
		})
		count := 0
		for _, kv := range got {
			if strings.HasPrefix(kv, agentLaunchShimActiveEnv+"=") {
				count++
				require.Equal(t, agentLaunchShimActiveEnv+"=1", kv,
					"inherited depth %s must be reset to 1", inherited)
			}
		}
		require.Equal(t, 1, count, "exactly one marker entry (inherited %s)", inherited)
	}
}
