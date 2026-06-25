//go:build qsbakeoff

// Package qsbakeoff is a gated, reproducible end-to-end test of the EXTERNAL
// query-string bake-off scaffold — the "should I use kitsoki for my project?"
// case. It is excluded from `make test` by the `qsbakeoff` build tag; it needs
// network (clone + npm install), git, node/npm, and the `kitsoki` binary. Run:
//
//	make qs-bakeoff   # or:
//	go test -tags qsbakeoff -run TestQueryStringBakeoff -count=1 -v ./tools/bugfix-bakeoff/external/
//
// What it proves DETERMINISTICALLY (no LLM, no cost):
//  1. A binary-only user can ONBOARD a real, mature third-party JS repo
//     (sindresorhus/query-string, 274 commits / 90 releases) via the embedded
//     dev-story — config + instance + studio MCP + skill/agent toolkit land.
//  2. For each of the 3 pinned bugs, the hidden-oracle good/bad detector
//     (score_qs.sh) is correctly ARMED: RED at the bug's baseline, and GREEN
//     once the real fix's source is applied. That validates the whole bake-off
//     (the fixtures are genuine RED->GREEN, the scorer is honest) before any
//     cost-bearing LLM cell is ever run.
//
// The cost-bearing LLM cells (kitsoki bugfix pipeline vs single-prompt, scored
// with the SAME score_qs.sh) stay operator-run — see the case study.
package qsbakeoff

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const repoURL = "https://github.com/sindresorhus/query-string.git"

type bug struct {
	id       string
	baseline string // fix_sha^ — bug present
	fix      string // the fix commit; only its base.js is applied for the GREEN check
}

// Pinned fixtures — see query-string/manifest.yaml. Each baseline is the fix
// commit's first parent, so the checkout is byte-reproducible.
var bugs = []bug{
	{"qs1", "2e1f45aafb71ef247572b10d9d37dce67cd825ac", "ec67feafcef38759e5ec76f7bc69aa835bc05b9c"},
	{"qs2", "88e1e36dd3bccf178dfaf54f44293e790d8bbd9d", "3e6188231268dca3bb514451839b263a70538e7b"},
	{"qs3", "4287e770ed3e6133217a86ac4ed3cee24cde0622", "19c43d4e19ed5f362ee22644b0cced6cabb4dda8"},
}

func TestQueryStringBakeoff(t *testing.T) {
	kitsoki, err := exec.LookPath("kitsoki")
	if err != nil {
		t.Skip("kitsoki not on PATH — run `make install` first")
	}
	for _, tool := range []string{"git", "npm", "node"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	scorer := scorerPath(t)

	work := t.TempDir()
	repo := filepath.Join(work, "query-string")

	// 1. One clone; fetch every baseline + fix commit we need (depth 1 each).
	run(t, work, "git", "init", "-q", repo)
	run(t, repo, "git", "remote", "add", "origin", repoURL)
	for _, b := range bugs {
		run(t, repo, "git", "fetch", "-q", "--depth", "1", "origin", b.baseline)
		run(t, repo, "git", "fetch", "-q", "--depth", "1", "origin", b.fix)
	}

	// Build node_modules ONCE (slowest step ~18s) and reuse across every tree.
	run(t, repo, "git", "checkout", "-q", bugs[0].baseline)
	run(t, repo, "npm", "install", "--no-audit", "--no-fund", "--silent")
	nodeModules := filepath.Join(repo, "node_modules")
	t.Setenv("QS_NODE_MODULES", nodeModules)

	// 2. Onboard the mature JS repo via the EMBEDDED dev-story (binary-only).
	t.Run("onboard", func(t *testing.T) {
		onboard := filepath.Join(work, "onboard")
		export(t, repo, bugs[0].baseline, onboard)
		db := filepath.Join(work, "onboard.db")
		const app = "@kitsoki/dev-story"
		sess := func(args ...string) {
			base := []string{"session", "continue", "--app", app, "--db", db, "--key", "local:qs"}
			run(t, onboard, kitsoki, append(base, args...)...)
		}
		run(t, onboard, kitsoki, "session", "create", "--app", app, "--db", db, "--key", "local:qs")
		sess("--intent", "work", "--slots", `{"request":"onboard `+onboard+`"}`)
		sess("--intent", "init_discovered")
		sess("--intent", "confirm_init")
		sess("--intent", "init_applied")

		mustExist(t, onboard, ".kitsoki.yaml")
		mustExist(t, onboard, ".mcp.json")
		mustExist(t, onboard, filepath.Join("stories", "query-string-dev", "app.yaml"))
		mustExist(t, onboard, filepath.Join(".claude", "skills", "kitsoki-story-authoring"))
		mustExist(t, onboard, filepath.Join(".claude", "agents", "kitsoki-mcp-driver.md"))
		t.Logf("onboarded mature JS repo query-string@%s -> working kitsoki env", bugs[0].baseline[:12])
	})

	// 3. Per bug: the oracle is RED at baseline and GREEN once the real fix's
	//    source (base.js only — mirroring a candidate's source edit) is applied.
	for _, b := range bugs {
		b := b
		t.Run(b.id, func(t *testing.T) {
			// RED: baseline source only — the bug is present.
			red := filepath.Join(work, b.id+"-red")
			export(t, repo, b.baseline, red)
			if score(t, scorer, b.id, red) {
				t.Fatalf("%s: oracle GREEN at baseline — fixture not armed (expected RED)", b.id)
			}

			// GREEN: baseline + the real fix's base.js (source-only correct fix).
			green := filepath.Join(work, b.id+"-green")
			export(t, repo, b.baseline, green)
			run(t, repo, "git", "--work-tree="+green, "checkout", b.fix, "--", "base.js")
			if !score(t, scorer, b.id, green) {
				t.Fatalf("%s: oracle RED after the real fix — scorer or fixture is wrong (expected GREEN)", b.id)
			}
			t.Logf("%s: oracle armed (RED@baseline) and rewarded (GREEN@real-fix)", b.id)
		})
	}
}

// score runs the deterministic detector; true == GREEN (good fix), false == RED.
func score(t *testing.T, scorer, bugID, tree string) bool {
	t.Helper()
	cmd := exec.Command("bash", scorer, "--bug", bugID, "--tree", tree)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	t.Logf("score %s:\n%s", bugID, out)
	return err == nil // score_qs.sh exits 0 iff the oracle passed
}

func scorerPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(wd, "score_qs.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("score_qs.sh not found at %s: %v", p, err)
	}
	return p
}

// export materializes the repo tree at sha into a fresh dir (no .git history).
func export(t *testing.T, repo, sha, dest string) {
	t.Helper()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	// git archive <sha> | tar -x -C dest
	archive := exec.Command("git", "-C", repo, "archive", sha)
	untar := exec.Command("tar", "-x", "-C", dest)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	untar.Stdin = pipe
	if err := untar.Start(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Run(); err != nil {
		t.Fatalf("git archive %s: %v", sha, err)
	}
	if err := untar.Wait(); err != nil {
		t.Fatalf("untar %s: %v", sha, err)
	}
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func mustExist(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Fatalf("expected %s to exist: %v", rel, err)
	}
}
