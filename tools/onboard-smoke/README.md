# onboard-smoke — reproducible project-onboarding E2E

A gated end-to-end test that proves **project onboarding produces a fully
working kitsoki environment** against a real, pinned open-source repository —
the way a binary-only user (no kitsoki checkout) would experience it.

## What it does

1. Clones a **pinned SHA** of [`sindresorhus/yocto-queue`](https://github.com/sindresorhus/yocto-queue)
   (tiny, MIT, Node) into a temp dir — byte-reproducible (the SHA is the
   dereferenced `v1.1.1` tag commit, immune to later tag movement).
2. Drives onboarding **headless** against the binary's **embedded** dev-story
   (`kitsoki session … --app @kitsoki/dev-story`) — no local kitsoki repo, no
   `--kitsoki-repo`, no network beyond the clone.
3. Asserts the onboarded repo is a working environment:
   - `.kitsoki.yaml`, `stories/yocto-queue-dev/app.yaml` (the dev-story instance)
   - `.mcp.json` registering the kitsoki studio MCP server
   - `.claude/skills/<name>` relative symlinks into `.agents/skills/<name>`
   - `.claude/agents/kitsoki-mcp-driver.md`
   - the generated instance **loads** (a fresh session against it creates cleanly)

## Run it

```sh
make onboard-smoke
# or, against an already-installed binary:
go test -tags onboardsmoke -run TestOnboardPinnedRepo -count=1 -v ./tools/onboard-smoke/
```

It is **excluded from `make test`** by the `onboardsmoke` build tag — it needs
network, git, and `kitsoki` on PATH. It skips cleanly if `kitsoki`/`git` are
absent.

## Updating the pin

Edit `repoURL` / `repoSHA` in `onboard_smoke_test.go`. Resolve a tag's commit
SHA with:

```sh
git ls-remote --tags https://github.com/sindresorhus/yocto-queue.git | grep '\^{}'
```

Use the `refs/tags/<v>^{}` SHA (the dereferenced commit), not the tag-object SHA.
