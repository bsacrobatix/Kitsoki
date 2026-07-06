# Hermetic capsules: time-capsule environments for git/dev testing

**Status:** Draft v1. Nothing implemented yet.
**Kind:** epic (runtime + tools + testing; no TUI surface)
**Date:** 2026-07-06

## Why

dev-story, bugfix, PRD generation, product-journey, arena, and swarm all
exercise mechanical git/development behavior — branching, worktrees, commits,
PRs, RED/GREEN gates, repo scanning. Today each layer fakes or rebuilds its
target repo differently, and none of them can say *"this test ran against
exactly this repo state in exactly this environment, and anyone can rebuild
that state bit-for-bit"*:

- **Unit tests hand-roll fixtures.** There is no shared git-fixture package;
  every test re-invents `git init` under `t.TempDir()`
  (`internal/mcp/studio/vcs_tools_test.go:31`,
  `internal/orchestrator/dogfood_smoke_test.go:45`,
  `internal/orchestrator/gitops_rebase_resolve_test.go:11`). Rich states
  (mid-rebase conflict, stale worktree, dirty index) are bespoke and
  non-reusable.
- **Targets are named but not pinned.** Arena's `Target`
  (`tools/arena/arena/model.py:32`) and the 10-OSS matrix
  (`tools/product-journey/github-targets.json`) carry `repo` but **no
  commit/ref and no environment declaration**. product-journey clones into
  `tempfile.mkdtemp` and discards (`tools/product-journey/run.py:289`) — a
  re-run months later sees a different repo.
- **Environment is "a broad Docker image," not a capture.** Reproducibility
  today = commit SHA + the multi-toolchain
  `tools/bugfix-bakeoff/external/docker/Dockerfile.repo-runtime` + language
  cache dirs. No lockfile/toolchain-version manifest is captured anywhere
  (the query-string manifest literally notes `install: npm install # no
  lockfile committed`). When upstream npm/pip resolution drifts, the "same"
  baseline stops reproducing the original bug environment.
- **The discipline exists but is trapped in one tool.** bugfix-bakeoff already
  proves the model — `baseline_sha = fix_sha^`, RED-at-baseline /
  GREEN-at-fix verification (`tools/bugfix-bakeoff/external/bench.py:17`),
  per-cell detached hermetic worktrees
  (`tools/bugfix-bakeoff/external/drive_cell.sh:202-213`) — but it's welded to
  the bug-fix bake-off. PRD generation, onboarding journeys, git-flow flow
  tests, and swarm runs can't consume it.
- **Users can't do it in their repo.** Everything above is Kitsoki-internal
  tooling. A user who wants to test *their* project's bugfix/PRD flows against
  a frozen known state has nothing to `init`.

`docs/proposals/repo-history-training-loop.md` names generalizing exactly this
method as its substrate. This proposal builds that substrate as a first-class,
user-installable primitive.

## Prior art (external tools — what to adopt, learn from, or skip)

Researched 2026-07-06. Short version: **no off-the-shelf tool covers the whole
capsule** (declarative spec + synthetic git states + user-installable + arena/
flow/unit-test integration), but every sub-problem has a mature reference
implementation, and two external *formats* are worth adopting rather than
inventing rivals to.

### Task-capsule harnesses (the closest overall analogs)

- **[SWE-bench harness](https://www.swebench.com/SWE-bench/reference/harness/)**
  — the industry-standard "pinned repo@commit + env + hidden test oracle in
  Docker" evaluator. Its
  [three-layer image scheme](https://www.swebench.com/SWE-bench/guides/docker_setup/)
  (base → ~60 shared environment images → per-instance images) is the proven
  answer to our large-repo/cache economics question (Open Q3): most of the
  bytes are shared layers, only the instance layer is per-capsule. Validation
  discipline: 99.78% of tasks resolve with the ground-truth patch —
  "ground-truth must pass" is a standing capsule probe we should copy. Its
  known weakness validates our env-capture stance: SWE-bench Dockerfiles
  generally **don't** pin apt/PyPI versions, so images drift at rebuild time
  ([Epoch AI's registry](https://github.com/epoch-research/SWE-bench) exists
  precisely to freeze built images). **Verdict: learn (image layering,
  ground-truth probe); don't adopt — Python-eval-corpus-shaped, not a
  user-installable primitive.**
- **[Terminal-Bench / Harbor](https://github.com/harbor-framework/harbor)** —
  the closest *structural* analog to capsule+scenario: a task = a plain
  directory (`task.toml` + `instruction.md` + `environment/Dockerfile` +
  hidden `tests/test.sh` + oracle `solution/solve.sh`), graded on
  **environment end-state, not agent transcript** — exactly our git-flow
  oracle stance. Reward comes from an explicit file write
  (`/logs/verifier/reward.txt|json`), never exit codes — the exact lesson our
  bakeoff learned the hard way (score-verdict-as-exit-code hang). Crucially,
  **Harbor itself never calls an LLM**: agents are black boxes behind a
  ~50-line `BaseAgent`/`BaseInstalledAgent` Python shim, so a no-LLM
  cassette-replay kitsoki agent is fully policy-compliant. Ships built-in
  drivers for Claude Code, Codex, OpenHands, Gemini CLI, etc., plus `oracle`
  (run the reference solution) and `nop` (grade the untouched env) utility
  agents; 20+ import adapters (SWE-bench, SWE-smith, Aider Polyglot, …);
  local task dirs and local `registry.json` registries; `--n-concurrent`,
  retries with resume, and `--env docker|daytona|modal|e2b|…` execution
  backends. Apache-2.0, very active (v0.17.1, 2026-07). **Verdict: adopt at
  the interchange edge in both directions plus an agent shim — the full
  interop plan is §6 below. Not the core: Harbor has no synthetic-git-state
  builder, no seal/capture direction, no in-process Go fixture path, no
  mid-run gates/UI journeys.**
- **[SWE-smith](https://swesmith.com/blog.html)** (50k tasks, 250+ envs from
  arbitrary repos via LLM-synthesized bugs) and **R2E-Gym's SYNGEN**
  (back-translating commit history into issue specs + tests) — prior art for
  the *corpus factory* on top of capsules, i.e. for
  `repo-history-training-loop.md`, not for this substrate. Their shared
  lesson: **automated environment setup is the bottleneck**, which is why the
  env block below leans on existing declarations (devcontainer/lockfiles)
  instead of inference. **Verdict: learn; out of scope here.**

### Reproducible bug corpora (our bakeoff's family)

- **[BugSwarm](https://github.com/BugSwarm/bugswarm)** — 3,600+ mined
  fail/fix pairs, each a Docker image, 97% reproducible. Two lessons: (a)
  freezing the *built image* (not the recipe) is what actually survives
  ecosystem drift — capsules should treat a pushed image digest as the
  strongest seal tier; (b) the
  [critical-review finding](https://arxiv.org/pdf/1905.09375) that ~96% of
  its volume was unusable for repair research — **curation gates beat mining
  volume**, which is what `capsule verify` + oracle linting encode.
- **Defects4J / [GitBug-Java](https://arxiv.org/html/2402.02961) /
  [GitBug-Actions](https://arxiv.org/pdf/2310.15642)** — the classic
  checkout+compile+test corpora. GitBug-Actions' trick is notable: it uses
  the repo's **own GitHub Actions workflow** as the reproduction environment
  definition. For repos with good CI that's the cheapest honest env spec we
  can harvest during `capsule seal` (new Open Q7). **Verdict: learn.**

### Environment capture & pinning

- **[devcontainer.json](https://containers.dev/)** — the one *standard*
  users already ship in their repos for "this repo's dev environment."
  **Verdict: adopt as a front-end** — `environment.devcontainer: true` should
  let a capsule consume an existing devcontainer.json (and `seal` should emit
  one), with the caveat the ecosystem itself documents: reproducibility is
  only as strong as the image pin, so `seal` resolves floating tags to
  digests.
- **[Nix flakes](https://nixos-and-flakes.thiscute.world/development/dev-environments)**
  — the reproducibility gold standard (a study of 7M builds across 200
  revisions found 99.99% output-path stability). But the learning curve is
  famously steep and we can't require it of arbitrary user repos.
  **Verdict: optional backend, never required** — the `environment` block
  stays declarative so a nix backend can slot in later; don't build on it.
- **[ReproZip](https://www.reprozip.org/)** — the capture direction done as
  research software: trace syscalls during a run, pack every touched
  file/dependency into an `.rpz` bundle, replay via Docker/Vagrant. Exactly
  the `capsule seal` philosophy, and proof that *observed* capture beats
  *declared* capture for completeness — but Linux-only packing and
  academic-maintenance pace. **Verdict: learn (seal could later add an
  observed-trace mode); don't depend on it.**
- **mise / asdf / Devbox / pixi** — toolchain-version pinning UX; our
  `toolchain:` map is deliberately compatible with what these files express,
  and `seal` can read `.tool-versions`/`mise.toml` when present.
  **Verdict: interoperate, don't wrap.**

### Sandbox/state snapshotters (cloud)

**[E2B](https://e2b.dev/docs/sandbox/persistence)** (pause/resume persists
filesystem *and* memory), **Morph** (VM-state branching — "Infinibranch" —
~250ms forks for parallel exploration), **Daytona** — the commercial version
of "bottle a live moment and fork it N ways." They validate two capsule
semantics: seal-a-moment and cheap parallel opens. But they are cloud VM
products: wrong weight for local unit tests, wrong dependency for a
user-installable OSS primitive. **Verdict: learn the semantics; a cloud-VM
open backend is a possible arena `Placement` later, not a foundation.**

### Test-fixture layer

- **[Testcontainers](https://testcontainers.com/)** — the ergonomics model
  for `capsuletest`: declare the fixture in the test, get lifecycle +
  teardown for free, works in plain `go test`. Copy the API feel.
- **Git's own test suite / go-git-fixtures / `testscript`(txtar)** — prior
  art for deterministic synthetic repos: fixed author/committer identity +
  fixed `GIT_*_DATE`s ⇒ stable SHAs. Our synthetic builder is this pattern,
  packaged declaratively.

### Net design consequences folded into this proposal

1. Environment block gains `devcontainer: true` consumption + digest-resolving
   `seal` emission (adopted standard, §1).
2. Cache design follows SWE-bench's base/environment/instance image layering
   (answers Open Q3's economics half).
3. Standing probe: every scenario capsule must pass its ground-truth
   solution (SWE-bench's 99.78% validation discipline; bakeoff's GREEN@fix
   generalized).
4. New open questions: Harbor-task export (Q6), harvesting GitHub Actions
   workflows as env specs during seal (Q7).

## What changes

Introduce the **capsule**: a small declarative spec that names a
repo, a commit, a working-tree state, and an environment — plus a
deterministic **reconstruction** operation that materializes it, a
**verification** gate that proves the reconstruction matches, and adapters so
every existing consumer (Go unit tests, story flow tests, arena, bakeoff,
product-journey, swarm, e2e) opens capsules instead of rolling its own.

```
capsule.yaml ──▶ kitsoki capsule open ──▶ materialized workspace
   (spec)          (reconstruct)            (repo@commit + env + state)
                        │
                        ▼
              kitsoki capsule verify
              (digest check: RED/GREEN gates, tree hash, env probe)
```

One primitive, five consumers. The novelty budget is almost entirely
*unification + environment capture*; the git mechanics are reuse.

### 1. The capsule spec (`capsule.yaml`)

A capsule is a directory (in-repo under `capsules/<name>/`, or published as a
kit) containing:

```yaml
# capsule.yaml
name: query-string-bug299
source:
  repo: https://github.com/sindresorhus/query-string   # or `synthetic: true`
  commit: 3d31b8f…                                     # full 40-hex SHA, required
  # optional overlay applied after checkout — patches, extra files,
  # pre-seeded .worktrees, mid-rebase state scripts
  overlay: ./overlay/
environment:
  image: kitsoki/repo-runtime:sha256@…    # preferred: content-addressed image
  devcontainer: true                      # or: consume the repo's own
                                          #  devcontainer.json (seal resolves
                                          #  floating tags to digests)
  toolchain:                              # captured, not aspirational
    node: 22.11.0
    pnpm: 9.12.1
  install: pnpm install --frozen-lockfile
  lockfiles: ./locks/                     # captured lockfiles overlaid if the
                                          # pinned commit lacks them
  env:                                    # deterministic env vars (TZ, LANG,
    TZ: UTC                               #  SOURCE_DATE_EPOCH, no proxies)
network: none | replay | live             # default none; replay = HTTP cassette
verify:
  tree_digest: sha256:…                   # git tree hash of the materialized
                                          #  worktree after overlay
  probes:                                 # semantic checks, e.g. the bakeoff
    - name: red-at-baseline               #  RED gate generalized
      run: npx ava test/oracle.js
      expect: nonzero
scenario:                                 # optional — what this capsule is FOR
  kind: bugfix | prd | git-flow | onboarding | freeform
  ticket: ./ticket.md                     # leak-free task description
  oracle: ./oracle/                       # hidden grading assets (kept out of
                                          #  the candidate's tree, bench.py-style)
```

Two source families, one spec:

- **Pinned real repos** (the 10-OSS case, external bake-offs): `repo` +
  `commit`, materialized through a generalized `internal/kitgit` cache.
- **Synthetic repos** (unit/flow tests): no remote; the capsule *is* the
  overlay — a script or declarative file-set that builds the repo from
  nothing (`git init` + commits + optional broken states like mid-rebase).
  Deterministic authorship: fixed author/committer identity and
  `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE` so the resulting SHAs are stable and
  can be asserted.

### 2. Reconstruction: `internal/capsule` + `kitsoki capsule` CLI

New Go package `internal/capsule` (DI-seamed like `internal/kitgit`'s `Runner`)
with a thin CLI/MCP surface:

- `kitsoki capsule open <name> [--dest DIR]` — materialize into a workspace.
  Pipeline: **fetch** (kitgit-style commit-addressed cache at
  `${XDG_CACHE_HOME}/kitsoki/capsules/<commit>/`; a full SHA already cached
  never touches the network — same offline-reproducible property
  `internal/kitgit/kitgit.go:1-23` already guarantees for kits) → **checkout**
  (detached worktree per open, the `drive_cell.sh:202` pattern, so N parallel
  opens of one capsule never collide) → **overlay** → **environment**
  (container when `image` is set; host-mode with toolchain *probe-and-refuse*
  when not — never auto-install on a user's machine) → **seal** (write
  `capsule-manifest.json` recording exactly what was materialized: resolved
  SHAs, image digest, toolchain versions found, overlay digest).
- `kitsoki capsule verify <name|workspace>` — recompute `tree_digest`, run
  `probes`, diff against the spec. This is the generalized
  `bench.py verify/preflight` (RED@baseline / GREEN@fix becomes just two
  probes) and the gate every consumer trusts instead of trusting itself.
- `kitsoki capsule seal <workspace>` — the capture direction: given a live
  checkout, snapshot commit + dirty-state overlay + detected toolchain
  versions + lockfiles into a new `capsule.yaml`. This is how a user (or a
  dogfood run that just hit an interesting state) bottles a moment in time.
- `kitsoki capsule close <workspace>` — teardown via the isolated-clone
  sentinel/cleanup discipline that already shipped
  (`.context/isolated-clone-cleanup-task-list.md`).

Environment execution layers on what exists rather than inventing a runtime:
container mode reuses arena's `ContainerBackend`/`DockerBackend`
(`tools/arena/arena/executor.py:31,38`); host confinement composes with
`internal/host/validator_sandbox.go` and stays behind the pluggable
`AgentRuntime` boundary proposed in `docs/proposals/task-fs-sandbox.md` — this
proposal does **not** add a second sandbox mechanism.

Determinism contract (the "hermetic" in hermetically sealed):

- `network: none` is the default; opens fail loudly if install needs the
  network and the cache can't satisfy it. `replay` uses the existing
  starlark/http cassette machinery for any scripted fetches.
- Same spec + same cache ⇒ byte-identical tree digest, asserted by `verify`.
- Everything interpretive (LLM) stays outside the capsule; the capsule is the
  deterministic stage the interpretive work performs on — consistent with the
  moat rule (decisions pluggable + recorded, execution deterministic).

### 3. Consumer adapters (the integration surface)

| Consumer | Today | With capsules |
|---|---|---|
| **Go unit tests** | per-test `git init` boilerplate | `capsuletest.Open(t, "mid-rebase-conflict")` — a tiny test helper over `internal/capsule` with `t.TempDir` + auto-close; shared library of synthetic capsules replaces `initRepo`/`setupDogfoodRepo` clones |
| **Story flow tests** | `host.git` stubbed wholesale (`stories/dev-story/flows/bugfix_to_pr.yaml:32`) or starlark inspect cassettes | a third tier: flows may declare `capsule:` and run **real** `host.git`/`host.git_worktree` against the materialized synthetic repo — real git semantics, zero LLM, zero network. Fixes the class of gaps where stubs assert the wrong shape (bugfix-pipeline verify gaps) |
| **bugfix-bakeoff** | manifest.yaml + drive_cell.sh ad-hoc pinning | each `(project, bug)` **is** a capsule; `bench.py verify` delegates to `capsule verify`; drive_cell's worktree/cache code deletes into `capsule open` |
| **arena** | `Target` has `repo`, no ref/env (`model.py:32`) | `Target` gains `capsule:` (name or inline spec); `CellExecutor` opens the capsule per cell, mounts it into the container; INFRA-vs-MODEL retry keys off `capsule verify` (verify fails ⇒ INFRA, never a model score) |
| **product-journey / 10-OSS** | ephemeral `mkdtemp` clones, no pin | `github-targets.json` entries gain `capsule` refs; target prep = `capsule open`; the matrix becomes replayable months later |
| **swarm** | N concurrent UI sessions on one server | each swarm user's session can bind a fresh open of the same capsule — concurrent isolation falls out of detached-worktree-per-open |
| **dev-story agent workspaces** | `iface.workspace.create` → `host.git_worktree` worktrees; `clone_create` staged but unwired | the workspace provider becomes a thin profile over `capsule open --live` — see §4 below |
| **e2e / Playwright** | whatever the spec sets up | specs request capsules through the same CLI; `capsule-manifest.json` lands in the run's artifacts as provenance |
| **user repos** | nothing | `kitsoki capsule init` scaffolds capsules in *their* repo (the onboarding/`project-tools install` path, `internal/basestories` precedent); `capsule seal` bottles their current state; their bugfix/PRD stories run against their own capsules |

Also fix the known capsule-awareness bug this design forces: the starlark
fs-inspector roots at the MCP process CWD instead of the active
worktree/workspace (`.context/goal-seeker-STATE.md:131`) — under capsules the
workspace root becomes explicit input to the inspector, not ambient CWD.

### 4. Capsule-backed agent workspaces (the `iface.workspace` provider)

Capsules aren't only a *test* substrate — they can be the agent workspace
across dev-story, replacing worktrees for autonomous work. The seam already
exists and the migration is already half-staged:

- Rooms never call `host.git_worktree` directly; they provision through the
  abstract `workspace` capability (`stories/dev-story/app.yaml:316-352`,
  `default: host.git_worktree` at `:352`; call sites
  `stories/bugfix/rooms/idle.yaml:146-178`,
  `stories/implementation/rooms/idle.yaml:56-62`). Swapping the backend is a
  capability rebind — **zero room changes**.
- The worktree pain is documented and real: linked worktrees share refs,
  stash, reflogs, hooks/config, and lock files
  (`docs/architecture/hosts.md:1185-1191`); the shared stash stack clobbers
  parallel agents (`docs/stories/git-ops-conflict-avoidance.md:20-23`); the
  destructive shared-checkout incident forced per-session worktree suffixes +
  `.kitsoki-owner` sentinels (`internal/host/git_worktree.go:184-202`).
- The fix — isolated `clone_create` + sentinel-gated cleanup — landed in
  `e50fae3d` (2026-07-03) but **no story room invokes it yet**. It's plumbing
  without a driver.

The unification: an isolated clone of the current repo at HEAD with ambient
environment is exactly a **degenerate capsule** — `repo: self, commit: HEAD,
environment: none, verify: skipped`. So instead of finishing the clone
migration as a parallel lifecycle, finish it *as* the capsule engine:

- `capsule open --live` (or a `live: true` profile): materialize an isolated
  clone of the enclosing repo at the requested base, sentinel it, register it
  for cleanup — the existing `cloneCreate`
  (`internal/host/git_worktree.go:527-587`) becomes the live-profile
  implementation inside `internal/capsule`, not a sibling.
- A new `host.capsule_workspace` handler (same
  `Handler(ctx, args) (Result, error)` shape, registered beside
  `internal/host/handlers.go:489`) satisfies the existing
  `create/sync/cleanup_scan/cleanup_apply` op contract; rebind
  `iface.workspace.default` to it per instance to migrate incrementally.
- Payoffs beyond deduplication: **one** materialization/sentinel/teardown
  lifecycle instead of two evolving in parallel; pinned workspaces for free
  (a bugfix room reproducing a historical state opens a sealed capsule as its
  `world.workdir` — the reproduction workspace and the agent workspace are the
  same object); `capsule seal` becomes available mid-story ("bottle this
  moment" when a dogfood run hits an interesting state).

Overhead guardrails — where capsule semantics must *not* leak into everyday
work:

- The live profile skips verify, environment capture, and container
  indirection entirely; it must cost what `git clone` costs. Full hermetic
  opens are opt-in via a real `capsule.yaml`.
- Human-supervised worktree flow stays: `leave-worktree` exits and
  `.worktrees/` remain the interactive path, per the hosts.md guidance
  (worktrees for supervised work, isolation for autonomous runs).
- Throwaway internal worktrees (pr-split's `MkdirTemp` +
  `git worktree add` in `internal/host/git_vcs.go:410-424`) are not worth
  migrating — they never touch shared mutable state long enough to matter.

### 5. Scenario layer (what the capsule is *for*)

The optional `scenario` block generalizes the bake-off's ticket/hidden-oracle
split beyond bugs:

- **bugfix**: ticket + hidden oracle test, RED-at-open probe (today's model).
- **git-flow**: capsule opens in a state (diverged branch, conflicted rebase,
  dirty index, stale `.worktrees/`) and the oracle asserts the *repo end
  state* (branch graph shape, clean tree, PR opened against mock remote) —
  this is what dev-story's mechanical git rooms need for honest testing.
- **prd / design**: capsule pins the codebase the PRD must describe; grading
  compares against reference artifacts sealed with the capsule.
- **onboarding / product-journey**: capsule is the target repo persona
  journeys run against.

Oracles follow the bake-off lessons already learned: assert **behavior**, not
one implementation's error prose, and lint for brittleness
(`tools/bugfix-bakeoff/external/bench.py:401` `lint_oracles` generalizes here).

Mock remote: git-flow scenarios need `push`/`open_pr` without GitHub. The
capsule can declare a **bare peer repo** materialized alongside the workspace
(`git remote add origin <bare>`), plus the existing `host.git` flow-stub tier
for the `gh` surface. Real-`gh` scenarios stay live-gated, per the testing
policy.

### 6. Harbor/Terminal-Bench interop (use the prior art to the maximum)

Harbor is worth more than the export adapter Q6 originally sketched. Because
Harbor never calls an LLM itself and its task format is a plain directory,
it can serve as **the interchange and container-execution edge for the whole
harness stack** — five lanes, cheapest first:

**Lane 1 — conventions (free, adopt everywhere, no dependency).**
- Reward = explicit file write, never exit codes. Our bakeoff already paid
  for this lesson (`bench.py` score-verdict-as-exit-code hang); make it the
  standing rule for every capsule probe/oracle runner.
- `nop` + `oracle` as the two standing validation agents: `nop` (grade the
  untouched env) must score RED and `oracle` (run the sealed solution) must
  score GREEN before any scenario capsule goes live — this is the
  SWE-bench/bakeoff RED@baseline/GREEN@fix discipline, named.
- Multi-metric `reward.json` (dict of floats) as the adjudication output
  shape — bakeoff/arena verdicts become one honest scalar per axis instead
  of a single pass/fail.

**Lane 2 — export: `capsule export --harbor` (Q6, promoted to committed).**
Scenario capsule → Harbor task dir: `scenario.instruction` →
`instruction.md`; env block → `environment/Dockerfile` (image digest `FROM`);
hidden oracle → `tests/test.sh` writing `reward.txt`; sealed fix →
`solution/solve.sh`. A local `registry.json` under `capsules/` gives the
scenario library versioned distribution (`name@version`) without publishing.
Constraint held from S1: the scenario block stays Harbor-shaped
(instruction + hidden tests + oracle solution) so export is mechanical.
Payoff: every capsule scenario is instantly runnable by Claude Code, Codex,
OpenHands, Gemini CLI, … via Harbor's built-in drivers — external bake-off
cells for ~free, and a public-benchmark path if we ever want one.

**Lane 3 — agent shim: kitsoki *as* a Harbor agent (`tools/harbor-agent`).**
A ~50-line `BaseAgent` subclass whose `run()` drives headless kitsoki
(bugfix/dev-story) against the task container via `environment.exec`, in two
modes: **live** (real LLM, gated as always) and **replay** (cassette
harness — zero LLM cost, so even automatable where Docker exists). Fills
`AgentContext` (tokens, cost, exit code) from the kitsoki trace. Payoffs:
run kitsoki on `terminal-bench-2.0` and the SWE-bench adapter sets for
neutral-ground comparison against every other agent on the same tasks;
Harbor jobs become a standing regression benchmark for kitsoki itself; and
`oracle`/`nop` validate our own exported tasks before any live spend.

**Lane 4 — import: Harbor task dirs as capsule *sources*.**
`capsule import --harbor <task-dir>` maps the same fields in reverse
(Dockerfile → env image, tests → hidden oracle, instruction → scenario).
Harbor's 20+ benchmark adapters then become free corpus faucets: SWE-bench
Verified, SWE-smith's 50k tasks, Aider Polyglot, etc. flow into capsules
without us building a single miner — this is the cheapest way to seed the
`repo-history-training-loop` corpus, and its ATIF trajectory export
(`harbor traces export --sharegpt`) is the ready-made training-data format
that epic's promotion loop can emit into.

**Lane 5 — executor delegation: arena/bakeoff cells that fit the shape.**
A cell that is "black-box agent in a container, graded on end state" is
exactly one Harbor trial — and Harbor's `--n-concurrent`, `RetryConfig`
with resume-skips-completed, jobs-dir `TrialResult` layout, and
`--env docker|daytona|modal|e2b|gke` backends duplicate what
`drive_cell.sh`/arena's `ContainerBackend` maintain by hand (including the
retry semantics the exit-code bug hid inside). Plan: arena `Target` gains an
`executor: harbor` option; external-agent bakeoff cells (claude-code, codex —
Harbor already ships those drivers) run under it first; kitsoki-native cells
follow via Lane 3's shim. Arena keeps everything Harbor can't express:
persona/UI journeys, mid-run operator gates, staged flows, trace-rich
adjudication. INFRA-vs-MODEL mapping stays honest: Harbor `exception_info`
⇒ INFRA retry; verifier reward ⇒ MODEL score.

What Harbor does **not** replace: kitsoki's flow tests, cassette machinery,
staged-mode gates, and TUI/web journeys stay authoritative — Harbor has no
mid-run interaction model at all. It is the *edge* (interchange + container
fleet execution), never the inner harness.

## Impact

- **New:** `internal/capsule` (+ `capsuletest` helper), `kitsoki capsule`
  CLI verbs, `capsules/` spec format + a starter library of synthetic
  capsules (clean repo, mid-rebase, dirty index, stale worktree, diverged
  remote), MCP tools (`capsule_open`/`verify`/`seal`) for agent-driven use.
- **Generalized:** `internal/kitgit` cache (kits → any pinned repo),
  `bench.py` verify/preflight (→ `capsule verify`), `drive_cell.sh`
  worktree/caching (→ `capsule open`).
- **Extended:** arena `Target`/`JobSpec` schema (+ capsule field),
  `github-targets.json` schema, flow-test harness (new `capsule:` fixture
  tier), onboarding installer (`capsule init` for user repos), the
  `workspace` capability (new `host.capsule_workspace` provider; the
  `iface.workspace` op contract is unchanged, so rooms don't move), arena
  executor options (`executor: harbor` beside the existing backends).
- **New external dependency (S8 only):** Harbor (Apache-2.0, pip) — confined
  to `tools/harbor-agent` and the export/import adapters; nothing in
  `internal/` imports it.
- **Deleted on completion:** bespoke fixture code in the named tests,
  drive_cell's clone/worktree section, product-journey's temp-clone path.
- **Not touched:** TUI, tracing schema (capsule provenance rides in the
  existing artifact/trace channels as `capsule-manifest.json`), sandbox
  internals (composes with `task-fs-sandbox.md`, doesn't replace it).
- **Risk:** environment capture is genuinely greenfield here (no nix/mise/
  lockfile capture exists anywhere in the repo today). Scope it honestly:
  v1 = container-image digest + toolchain version probe + captured lockfiles.
  Full bit-reproducible builds (nix-style) are explicitly out.

## Slices (decomposition sketch)

| # | Kind | Scope | Depends on |
|---|---|---|---|
| S1 | runtime | `internal/capsule`: spec, open/verify/close on **synthetic** capsules; `capsuletest`; migrate 2–3 unit-test fixtures as proof | — |
| S2 | runtime | pinned-remote sources: generalize kitgit cache, `capsule seal`, environment probe + container mode via arena's backend | S1 |
| S3 | runtime | flow-test `capsule:` tier — real git host calls against synthetic capsules in `kitsoki test flows`; fix inspector CWD rooting | S1 |
| S4 | tooling | bakeoff + arena adoption: `Target.capsule`, `bench.py verify` delegation, drive_cell deletion; re-verify query-string corpus through capsules | S2 |
| S5 | tooling | product-journey 10-OSS pinning + swarm/e2e provenance manifests | S2, S4 |
| S6 | story/UX | `kitsoki capsule init` user-repo scaffolding + onboarding install path + docs; git-flow scenario capsule library for dev-story's git rooms | S1, S3 |
| S7 | runtime | live-profile workspace provider: fold `clone_create` into `internal/capsule`, add `host.capsule_workspace`, rebind `iface.workspace` in the dogfood instance, retire the parallel clone lifecycle | S1 |
| S8 | tooling | Harbor interop (§6): `capsule export/import --harbor`, local `registry.json`, `tools/harbor-agent` shim (replay + live modes), arena `executor: harbor` for external-agent cells | S2 |

Sequencing: S1 → {S2, S3, S7} in parallel → S4 → {S5, S6, S8}. S7 can also
land early as the driver that finally wires the staged `clone_create` shift.
S8's Lane 1 conventions (reward files, nop/oracle validation) apply from S1
regardless — they're format discipline, not integration work.

## Tasks (v1 exit = S1 shipped)

- [ ] Spec `capsule.yaml` schema + `capsule-manifest.json` seal format; load-time validation.
- [ ] `internal/capsule`: synthetic builder (deterministic dates/identity ⇒ stable SHAs), open (detached worktree per open), verify (tree digest + probes), close (sentinel cleanup).
- [ ] `capsuletest.Open(t, name)`; starter capsules: clean, mid-rebase-conflict, dirty-index, stale-worktree, diverged-remote (mine the existing bespoke fixtures for these shapes).
- [ ] Migrate `vcs_tools_test.go` `initRepo` and `gitops_rebase_resolve_test.go` fixtures to capsules; delete the bespoke builders.
- [ ] `kitsoki capsule open|verify|close` CLI verbs (thin over the package).

## Open questions

1. **Spec home for user repos** — `capsules/` at repo root vs
   `.kitsoki/capsules/`? Leaning `.kitsoki/capsules/` to match kits/config
   and keep user repo roots clean.
2. **Capsules as kits?** A pinned capsule is nearly a git kit with an
   environment block. Should distribution just *be* the kits mechanism
   (`.kitsoki/kits.lock` entry) rather than a parallel channel? Strong pull
   toward yes — decide before S2.
3. **Large-repo economics** — the 10-OSS list includes kubernetes/tensorflow.
   Container-side, SWE-bench's base/environment/instance image layering
   (see Prior art) answers the storage half. Git-side, shallow-but-pinned
   materialization (`--filter=blob:none` + exact SHA) is likely required;
   does that break tree-digest verification? Needs a spike.
4. **Windows** — validator sandbox already punts on Windows; capsules v1
   should declare host-mode-only there or punt identically.
5. **Relationship to repo-history-training-loop** — that epic wants a corpus
   *of* capsules. Is it the parent of this, or a consumer? Proposal: this is
   the substrate slice it depends on; keep them separate files with a
   cross-link.
6. **Harbor interop depth** — export/import/agent-shim/conventions are
   committed (§6; the scenario block stays Harbor-shaped from S1). Still
   open: how far Lane 5 executor delegation goes (does arena's own
   `ContainerBackend` eventually delete, or stay for non-Harbor-shaped
   cells?), and whether we ever publish the capsule scenario library to the
   public Harbor registry vs keeping the local `registry.json` private.
7. **Harvest CI workflows as env specs** — GitBug-Actions shows a repo's own
   GitHub Actions workflow is often the most honest environment definition
   available. Should `capsule seal` offer a `--from-ci` mode that derives the
   environment block from the workflow's setup steps for repos with good CI?
8. **Fate of `host.git_worktree` after S7** — once `host.capsule_workspace`
   carries autonomous provisioning, does the worktree handler shrink to the
   human-supervised path only, or do both live behind one handler with a
   mode switch? Leaning: keep two handlers, one op contract — the capability
   rebind is the mode switch, and `list`/`cleanup_scan` stay shared. Either
   way the parallel `clone_create` code path inside `git_worktree.go` is
   deleted, not maintained.

## Non-goals

- Bit-reproducible builds (nix-level determinism) — v1 pins images, versions,
  and lockfiles; it does not rebuild toolchains.
- A new sandbox/isolation runtime — confinement stays with
  `task-fs-sandbox.md` / `validator_sandbox.go`; capsules define *contents*,
  not *containment*.
- Live GitHub simulation — mock remotes are bare local repos; real `gh`
  interactions remain live-gated tests.
- Time-travel of Kitsoki itself — capsules freeze *target* repos, not the
  Kitsoki version driving them (that's CI's job).
- LLM anything — capsules are pure deterministic substrate; interpretive
  work stays outside, per the testing policy (mocks/cassettes always).
