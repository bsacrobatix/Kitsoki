package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapsuleCIQuickGatePreparesAgentAssetsInCleanClone(t *testing.T) {
	repo := t.TempDir()
	bin := filepath.Join(repo, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(repo, "calls.log")
	writeExecutable := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("git", `
if [ "$*" = "rev-parse --show-toplevel" ]; then
	printf '%s\n' "$KITSOKI_QUICK_TEST_ROOT"
	exit 0
fi
if [ "$*" = "diff --check" ]; then
	exit 0
fi
exit 91
`)
	writeExecutable("make", `
printf 'make %s\n' "$*" >>"$KITSOKI_QUICK_TEST_LOG"
test "$*" = "embed-stories embed-skills"
touch "$KITSOKI_QUICK_TEST_ROOT/.agent-assets-ready"
`)
	writeExecutable("go", `
printf 'go %s\n' "$*" >>"$KITSOKI_QUICK_TEST_LOG"
case "$*" in
  *" ./cmd/kitsoki")
    test -f "$KITSOKI_QUICK_TEST_ROOT/.agent-assets-ready"
    ;;
esac
`)

	projectRoot := filepath.Clean(filepath.Join("..", ".."))
	gate, err := filepath.Abs(filepath.Join(projectRoot, "scripts", "capsule-ci-quick-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", gate)
	command.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"KITSOKI_QUICK_TEST_ROOT="+repo,
		"KITSOKI_QUICK_TEST_LOG="+logPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clean-clone quick gate: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	makeAt := strings.Index(calls, "make embed-stories embed-skills")
	cmdTestsAt := strings.Index(calls, "go test -short -count=1")
	if makeAt < 0 || cmdTestsAt < 0 || makeAt > cmdTestsAt {
		t.Fatalf("agent assets were not prepared before cmd tests:\n%s", calls)
	}
}
