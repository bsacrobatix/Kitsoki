package host_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"hally/internal/host"
)

// fakeOracleBin returns the path to testdata/fake-oracle.sh.
func fakeOracleBin(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "testdata", "fake-oracle.sh")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fake-oracle.sh not found at %s: %v", path, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("fake-oracle.sh is not executable")
	}
	return path
}

// TestOracleAsk_GeneratesSessionID calls the handler with no session_id and
// verifies the handler creates a UUID, invokes the fake binary, and returns
// both the answer and the generated session_id.
func TestOracleAsk_GeneratesSessionID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oracle.sh requires bash")
	}
	t.Setenv(host.OracleBinEnv, fakeOracleBin(t))

	res, err := host.OracleAskHandler(context.Background(), map[string]any{
		"question": "how does X work",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}

	sid, _ := res.Data["session_id"].(string)
	if sid == "" {
		t.Fatal("expected session_id to be generated")
	}
	uuidRE := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidRE.MatchString(sid) {
		t.Fatalf("session_id %q is not a v4 UUID", sid)
	}

	answer, _ := res.Data["answer"].(string)
	if !strings.Contains(answer, "how does X work") {
		t.Fatalf("answer does not echo the question: %q", answer)
	}
	if !strings.Contains(answer, sid) {
		t.Fatalf("answer does not contain the generated session_id: %q (sid=%s)", answer, sid)
	}
}

// TestOracleAsk_PreservesSessionID verifies that when a session_id is passed
// in, it is forwarded to the binary unchanged and returned in the result.
func TestOracleAsk_PreservesSessionID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oracle.sh requires bash")
	}
	t.Setenv(host.OracleBinEnv, fakeOracleBin(t))

	const existingSID = "11111111-2222-4333-8444-555555555555"
	res, err := host.OracleAskHandler(context.Background(), map[string]any{
		"question":   "second turn",
		"session_id": existingSID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}
	if sid, _ := res.Data["session_id"].(string); sid != existingSID {
		t.Fatalf("session_id not preserved: got %q want %q", sid, existingSID)
	}
	if ans, _ := res.Data["answer"].(string); !strings.Contains(ans, existingSID) {
		t.Fatalf("fake binary did not receive existing session_id: %q", ans)
	}
}

// TestOracleAsk_MissingQuestion asserts that an empty question returns an
// application-level error (Result.Error), not a Go error.
func TestOracleAsk_MissingQuestion(t *testing.T) {
	res, err := host.OracleAskHandler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected Result.Error for missing question")
	}
}

// TestOracleAsk_BinaryMissing asserts that when the claude binary is not
// available, the handler returns Result.Error with a helpful message and
// still echoes the (possibly generated) session_id so the caller can retry.
func TestOracleAsk_BinaryMissing(t *testing.T) {
	t.Setenv(host.OracleBinEnv, "/definitely/does/not/exist/claude")

	res, err := host.OracleAskHandler(context.Background(), map[string]any{
		"question": "anything",
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected Result.Error when binary is missing")
	}
	if sid, _ := res.Data["session_id"].(string); sid == "" {
		t.Fatal("expected a session_id to be echoed even on failure so caller can retry")
	}
}

// TestOracleAsk_RegisteredAsBuiltin verifies the handler is wired into the
// default Registry via RegisterBuiltins.
func TestOracleAsk_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	if _, ok := r.Get("host.oracle.ask"); !ok {
		t.Fatal("host.oracle.ask was not registered by RegisterBuiltins")
	}
}
