# arena — generalized comparison-job runner

One tool to run any large comparison/sweep — **bug-fixing, onboarding, persona
QA, …** — where every cell executes **in a Docker container**, and containers are
placed on **either the local Docker host or a remote VM** (a VM is just another
Docker host). It unifies `tools/bugfix-bakeoff/` (the cell executor) and the
matrix half of `tools/product-journey/` (the planner/rollup brain) behind a
single `JobSpec` and a **job-type plugin** interface.

Design + rationale: [`docs/proposals/arena-comparison-runner.md`](../../docs/proposals/arena-comparison-runner.md).

## The model

```
JobSpec ──enumerate──▶ Cell[] ──execute(container)──▶ CellResult[] ──▶ rollup
   │                      │                                │
 job_type            target × variant ×                job-type-agnostic
 targets[]           axis (e.g. bug)                   verdict + health + metrics
 variants[]          = the unit of work
 axes{}
 placement{hosts,…}
```

- **Cell** = `target × variant × axis-coords` for one `job_type`. The bakeoff
  `candidate` and the product-journey `persona/scenario` both collapse into a
  cell's **variant + axis**.
- **Two orthogonal axes:** *containerization* is always on (the cell's isolation
  unit); *placement* is just which Docker host the container lands on — `local`
  (active daemon) or `vm-N` (a remote context). The VM **executes the container**;
  there is no separate VM code path.
- **Job-type plugin** is the only thing that knows what a comparison *means*:
  `image(cell)` · `drive_command(cell, live)` · `score(cell, …) → CellResult`.

## The shared completion-state contract

`bugfix.py`'s `score()` no longer regexes the container's stdout/stderr. Whatever
runs inside the container (`bench.py verify`/`score`, and eventually
`drive_cell.sh`) writes one **versioned completion-state JSON** —
[`schemas/completion-state.schema.json`](../../schemas/completion-state.schema.json)
(`verdict` / `health` / `metrics` / `evidence_refs`) — that this plugin reads back
from the shared repo mount (`KITSOKI_MNT` inside the container == `REPO_ROOT`
outside it, for `local` placement) instead of parsing text. The same contract
backs the product-journey side: `tools/persona_qa/completion.py` builds a
schema-conformant completion-state from a `review.json`/`scenario-outcomes.json`
run bundle, so any future persona-qa arena plugin scores from the identical
shape a bugfix cell does. A missing or malformed file is reported as an explicit
`infra:*` health (`infra:missing-completion-state`,
`infra:completion-state-malformed`) — stdout/stderr infra-signal regexing (e.g.
`"connection refused"`) survives ONLY as a fallback for when the file is absent.

## Layout

| File | Role |
|---|---|
| `arena/model.py` | `JobSpec`, `Cell`, `CellResult`, enumeration |
| `arena/plugins/base.py` | `JobTypePlugin` protocol + registry |
| `arena/plugins/bugfix.py` | bugfix plugin — wraps `bench.py` oracle (verify / drive), scores from the completion-state file |
| `arena/plugins/paired_task.py` | paired-task plugin — one task through multiple treatments with shared oracle JSON |
| `arena/executor.py` | `CellExecutor` + `ContainerBackend` seam (`DockerBackend` \| `FakeBackend`) |
| `arena/placement.py` | sweep scheduler (concurrency, INFRA-vs-MODEL retry) |
| `arena/rollup.py` | job-agnostic leaderboard → `rollup.json` + `rollup.md` |
| `arena.py` | CLI: `plan` · `run` · `plugins` |
| `specs/*.yaml` | example job specs |
| `tests/test_*.py` | no-LLM, no-docker end-to-end (FakeBackend) |

## Corpora: targets and personas from product-journey

`tools/product-journey/` owns two reusable corpora: `github-targets.json` (the
vetted OSS repo list, with a `selection_contract` and optional refreshed
`target-proof.json` metadata) and `personas.json` (the persona lens catalog).
Rather than hand-copying ids into a spec, a `JobSpec` can load them directly —
read-only; product-journey stays the owner and arena never writes to either
file.

```yaml
job_type: persona-qa

# Materializes Target[] from the corpus instead of hand-inlining `targets:`.
# Path is resolved relative to the repo root.
targets_from: tools/product-journey/github-targets.json

# Optional: merge a refreshed target-proof.json's per-target checks into each
# Target's meta (meta.target_proof.status/open_bug_count/…). Accepts the proof
# file or its containing proof dir. Targets absent from the proof are untouched.
target_proof_from: .artifacts/product-journey/target-proofs/<proof-id>

variants:
  - id: kitsoki-gpt-5.5
    backend: codex
    model: gpt-5.5

axes:
  scenario: [fix_bug, file_product_issue]

# Loads axes.persona = [persona ids...] from personas.json — only fills the
# axis in when the spec doesn't already hand-inline `axes.persona` (that
# always wins, so existing specs are unaffected).
persona_axis_from: tools/product-journey/personas.json
```

Hand-inlined `targets:` / `variants:` / `axes:` keep working completely
unchanged — `targets_from` only applies when `targets:` is absent, and
`persona_axis_from` only fills in the `persona` axis when it isn't already
hand-specified. See `tools/arena/tests/test_corpus_loading.py` for the
parsing contract (pure file parsing, no docker/LLM).

## Usage

```bash
# enumerate cells (no execution)
python3 tools/arena/arena.py plan --spec tools/arena/specs/bugfix-query-string.yaml

# run the sweep — DEFAULTS to the no-LLM arming path (oracle RED→GREEN verify)
python3 tools/arena/arena.py run --spec tools/arena/specs/bugfix-query-string.yaml \
    --out .artifacts/arena/qs-skeleton

# the paid path (explicit opt-in to spend on real agent drives)
python3 tools/arena/arena.py run --spec … --out … --live
```

WB.2 paired-task gate:

```bash
python3 tools/arena/tests/run_no_llm.py
```

## Cost discipline

`run` defaults to the **no-LLM** path: for bugfix that is `bench.py verify`
(prove the oracle is armed: RED@baseline → GREEN@fix) executed inside the
container — exercising enumerate → container → score → rollup with **zero spend**.
`--live` is the only way to spend and is always explicit. The pipeline is fully
unit-tested with `FakeBackend` (no docker, no LLM): `tools/arena/tests/`.

For `paired-task`, the no-LLM path uses fixture oracle JSON for every treatment
arm and still exercises the same enumerate -> drive -> score -> aggregate ->
report path. A live run only happens with `arena run --live`.

## Image selection

Every job-type plugin implements `image(cell) -> str` (see
`arena/plugins/base.py`) — that's the only place a cell's container image is
decided. The bugfix plugin's convention (`arena/plugins/bugfix.py`):

```python
def image(self, cell: Cell) -> str:
    return cell.target.meta.get("image") or f"kitsoki-arena-repo/{cell.target.id}:latest"
```

i.e. a target can pin an explicit image via `target.meta.image` in the spec;
otherwise the plugin falls back to a per-project default tag. Follow this same
pattern for any plugin that needs the browser-capable image: either hard-code
the browser tag in `image()` for job types that always need a browser (e.g. a
future swarm plugin), or read it from `cell.target.meta["image"]` /
`cell.variant.meta["image"]` when a spec should be able to opt in per-target.

### Browser-capable image (`Dockerfile.repo-runtime-browser`)

`tools/arena/Dockerfile.repo-runtime-browser` layers Chromium + Playwright
(pinned to `tools/runstatus/package.json`'s `@playwright/test` version) on top
of the existing repo-runtime image, for cells that need to drive a real
browser (persona-qa web surfaces today; the swarm job type later). The base
repo-runtime image is untouched — plain bugfix cells never pay the extra
Chromium weight; only a plugin that explicitly requests this image does.

Build it (two steps — base first, then the browser layer):

```bash
docker build -f tools/bugfix-bakeoff/external/docker/Dockerfile.repo-runtime \
    -t kitsoki-arena-repo-runtime:latest tools/bugfix-bakeoff/external/docker

docker build -f tools/arena/Dockerfile.repo-runtime-browser \
    --build-arg BASE_IMAGE=kitsoki-arena-repo-runtime:latest \
    -t kitsoki-arena-repo-runtime-browser:latest tools/arena
```

or run the one-shot build+smoke script (docker-gated, **not** part of the
standing no-docker CI check — run it by hand when the Dockerfile or the
pinned Playwright version changes):

```bash
tools/arena/scripts/smoke-browser-image.sh
```

It builds both images and proves `npx playwright --version` and a real
headless Chromium page-render inside a container run of the image. This
change does **not** wire any plugin to the browser image — that's
`swarm-arena-job` (and any future persona-qa-via-arena plugin), which should
set `image()` to return `kitsoki-arena-repo-runtime-browser:latest` (or a
project-tagged variant of it) once they need one.

## Status — P0 (walking skeleton)

Done: model + enumeration, plugin interface + bugfix plugin, container executor
with the DI backend seam, local placement scheduler, rollup, CLI, no-LLM test.

**Proven end-to-end** on `query-string` (2026-06-29): the 6-cell no-LLM arming
sweep ran in real containers (`kitsoki-arena-repo/query-string:latest`, the
repo-runtime image) and scored **6/6 armed · win-rate 1.0 · 0 infra failures** —
each cell git-cloned the OSS repo, `npm install`ed, and proved the oracle
RED@baseline → GREEN@fix inside its own container. Build the image once with
`docker build -f tools/bugfix-bakeoff/external/docker/Dockerfile.repo-runtime \
-t kitsoki-arena-repo/query-string:latest tools/bugfix-bakeoff/external/docker`.

**VM placement proven** (2026-06-30): the same bugfix sweep ran on a remote
DigitalOcean droplet via a docker context over SSH (`vm-1`) — 3/3 armed,
win-rate 1.0, 0 infra failures — and the persona-QA product-journey smokes ran
in containers on that same VM (corpus valid; both smokes `passed`). The only new
code was placement-aware mounts: `placement.host_repo[host]` declares the
checkout path on each host's daemon (a remote `-v` source resolves on the VM, not
locally). Spec: `specs/bugfix-query-string-vm.yaml`.

Next (see design doc): **P1** a *pool* of VM hosts + completion-state polling
(scheduler already round-robins `placement.hosts`); **P3** retire
`escalate.sh`/matrix `emit_run`, single front door.

## Status — P2 (persona-qa plugin landed)

`persona-qa` is now a first-class job type alongside `bugfix` (`arena plugins`
lists both). One cell = one `(target, persona, scenario)` triple:

- **Non-live** drives the existing deterministic
  `tools/product-journey/run.py --driver-replay-smoke` path — cassette-backed
  evidence, zero LLM spend, real run bundle on disk (`review.json`,
  `scenario-outcomes.json`, `driver-handoff.json`, decks).
- **Live** (gated behind `--live`, cost-bearing, never run in CI) instead emits
  a fresh run bundle with `--emit-run` and dispatches the
  `product-journey-qa-driver` agent headlessly against it
  (`.agents/agents/product-journey-qa-driver.md`) before reviewing it.
- **score()** never regexes stdout for a verdict. It only reads the `run_dir`
  pointer out of the container's structured JSON output, then hands that
  directory to `tools.persona_qa.load_product_journey_run` — the same
  completion-state bridge `bugfix.py` reads its `--completion-state` file
  through — which derives verdict/health/metrics from the run bundle's real
  `review.json` on disk. Stdout/stderr text is only an INFRA fallback for a
  harness crash before any JSON was ever printed.
- **unify-corpora** wiring: `tools/arena/specs/persona-qa-onboarding.yaml`
  loads its target from the inline shape and its persona axis from
  `persona_axis_from: tools/product-journey/personas.json`, enumerating one
  cell per persona and completing a no-LLM sweep with `placement.concurrency: 2`.
- Tests: `tools/arena/tests/test_persona_qa_plugin.py` proves registration,
  argv shape for both drive paths, scoring against a real on-disk review.json
  bundle (partial + solved + missing-run_dir + infra-crash cases), and a
  2-concurrent `FakeBackend` sweep across 3 personas with axis coords carried
  through to `CellResult`/rollup.

Not yet wired: the browser cell image (`arena-browser-image`) and the swarm job
type build on this plugin but are separate changes.

## Status — swarm job type landed (swarm-arena-job)

`swarm` is a third job type alongside `bugfix`/`persona-qa` (`arena plugins`
lists all three). Unlike persona-qa's "one cell per persona", **one swarm cell
IS the whole tier-1 swarm run** — the shared `kitsoki web` server plus its
N scripted Playwright users, from `tools/swarm/` +
`tools/runstatus/tests/playwright/swarm-replay-users.spec.ts` (`swarm-tier1`).
The swarm itself is arena's unit of placement; the users inside it are not
separate cells, so scaling the swarm out just means placing more swarm cells
(more targets/variants/axis combos, or more hosts), the same way any other
job type scales.

- **image()** always requests the browser-capable image
  (`kitsoki-arena-repo-runtime-browser:latest`, from `arena-browser-image`,
  the first job type to actually use it) unless a spec pins
  `target.meta.image`/`variant.meta.image` to an override.
- **No separate `--live` path.** The tier-1 harness never calls an LLM — every
  user is a scripted replay driven from `tools/product-journey/personas.json`
  lenses over a flow fixture — so `drive_command` runs the identical
  `cd tools/runstatus && npx playwright test tests/playwright/swarm-replay-users.spec.ts`
  command whether or not `--live` is passed. `live` is accepted only for
  interface parity with `bugfix`/`persona-qa`.
- **Axis-driven knobs:** `axis.users` maps onto the harness's own `SWARM_USERS`
  env var and `axis.interactive_concurrency` onto
  `SWARM_INTERACTIVE_CONCURRENCY` (both already read by
  `swarm-replay-users.spec.ts`). `axis.persona_mix` / `axis.fixture` are
  threaded onto the command line as `SWARM_PERSONA_MIX`/`SWARM_FIXTURE` for
  forward-compat, but the standing harness does not read them yet (it always
  rotates every persona in `personas.json` and always drives the hardcoded
  `happy_path.yaml` fixture) — wiring the harness itself to honor those two
  knobs is a follow-up to `swarm-tier1`, out of this change's scope
  (`tools/arena/arena/plugins/swarm.py` + `tools/arena/tests/test_swarm_plugin.py`
  only; it never touches `tools/swarm/**` or the spec file).
- **score()** never regexes stdout for a verdict. The ONE thing read from
  stdout is the harness's own `[swarm] wrote <path> (...)` line
  (`swarm-replay-users.spec.ts`'s final `console.log`, right after
  `tools/swarm/results.ts`'s `writeResults` returns) — a path, not a verdict —
  mirroring `persona_qa.py`'s `_extract_run_dir` convention. That path is
  mapped from its container form (`KITSOKI_MNT/...`) back to the host path
  (`REPO_ROOT/...`, for `local` placement) and the real `SwarmResults` JSON is
  read off disk. The **aggregate rule**: `all_completed` AND `all_isolated`
  AND `all_console_clean` AND `all_audit_clean` AND (the negative control,
  if it ran, actually detected the seeded cross-talk fault) => `solved`;
  zero completions, or a negative control that ran but failed to detect the
  fault (the isolation gate itself is broken), => `failed`; anything else
  partially clean => `partial`. A results file that was never written at all
  (harness crashed before `writeResults` ran) is reported as an explicit
  `infra:*` health (`infra:missing-results-path`, `infra:harness`,
  `infra:results-malformed`), never guessed as a model verdict.
- Tests: `tools/arena/tests/test_swarm_plugin.py` proves registration,
  image selection (default + override), full argv/env composition (users /
  interactive_concurrency / persona_mix / fixture axes all reaching the
  command line, defaults when axes are absent), scoring against real on-disk
  `SwarmResults` JSON (solved / partial / zero-completions-failed /
  negative-control-failed / negative-control-unexercised / infra-missing /
  infra-crash), the container<->host results-path mapping, and a
  2-concurrent `FakeBackend` sweep across a `users` axis with axis coords
  carried through to `CellResult`/rollup — all with zero docker, zero LLM
  spend. A real docker-gated run (the actual swarm inside the browser image)
  is manual acceptance only, out of this test gate's scope.

## Status — usable-kitsoki-gate job type registered (usable-kitsoki-release-gate, Task 2)

`usable-kitsoki-gate` is a fourth job type alongside `bugfix`/`persona-qa`/
`swarm` (`arena plugins` lists all four). Per
`docs/proposals/usable-kitsoki-release-gate.md`, one cell drives the whole
mined scenario corpus for one `persona x surface` combination (the parity
verdict record — `arena/plugins/usable_kitsoki_gate_schema.json`, Task 1 — is
emitted once per scenario within that cell, not once per cell), the same
"the cell is the unit of placement, not the individual run inside it"
convention `swarm.py` established.

- **image()** dispatches on `axis.surface`: `web`/`tui` request the
  browser-capable image (`kitsoki-arena-repo-runtime-browser:latest`); `mcp`
  is a headless stdio surface and gets the plain
  `kitsoki-arena-repo-runtime:latest`. `target.meta.image`/`variant.meta.image`
  override either default, same escape hatch as `swarm.py`/`persona_qa.py`.
- **drive_command()** dispatches on `axis.surface` too: `web` reuses the
  swarm-style convention (`cd tools/runstatus && npx playwright test ...`);
  `tui`/`mcp` shell into their own stub runner script under
  `tools/usable-kitsoki-gate/`. `GATE_SURFACE`/`GATE_PERSONA`/
  `GATE_SCENARIO_CORPUS`/`GATE_RUN_ID`/`GATE_RESULTS_PATH` env vars carry the
  cell's coords through either path. **None of the three concrete harness
  entry points exist yet** (`tests/playwright/usable-kitsoki-gate-web.spec.ts`,
  `tools/usable-kitsoki-gate/run_tui_gate.py`,
  `tools/usable-kitsoki-gate/run_mcp_gate.py`) — S1 (workbench producer
  contract) and S4 (scenario foundry / mined corpus) are separate proposals
  that haven't landed. This plugin's job today is the argv/env composition
  and scoring contract, proven by test, so S1/S4 land against an already-
  stable seam. No separate `--live` path exists yet either, for the same
  reason (mirrors `swarm.py`'s `live` no-op).
- An empty scenario corpus (no targets/variants/axes) enumerates to **zero
  cells**, not an error — `JobSpec.cells()` already returns `[]` for empty
  `targets`/`variants`/any axis with an empty value list; no special-casing
  needed in the plugin (`arena plan --spec <empty-corpus-spec>` prints
  `cells=0` and exits 0).
- **score()** never regexes stdout for a verdict. The ONE thing read from
  stdout is the harness's own `[usable-kitsoki-gate] wrote <path> (...)`
  line, mirroring `swarm.py`'s `[swarm] wrote <path>` convention. The bundle
  at that path (`{"run_id", "records": [...]}`) is mapped from its container
  path back to the host path and every record is validated against
  `usable_kitsoki_gate_schema.json` — a record that fails validation blocks
  the cell (`infra:results-malformed`) rather than silently scoring past it.
  Clean records are reduced into the three `GATE_CONDITIONS`
  (`usable_kitsoki_gate_constants.py`): zero `silent_bounce`, zero
  `misroute_adjacent`, and `parity_percent(candidate_and_source_completed,
  source_completed) >= PARITY_THRESHOLD_PERCENT` — all three passing is
  `solved`, any one failing is `failed`. This reduction is **per cell**, i.e.
  per `(persona, surface)`; the cross-surface **worst-surface-gating**
  reduction the proposal describes needs multiple cells' results compared
  against each other and belongs to a higher rollup layer (Task 3, gated on
  S1/S4 landing — explicitly out of scope here).
- Tests: `tools/arena/tests/test_usable_kitsoki_gate_plugin.py` proves
  registration alongside the other three job types, image selection per
  surface (+ target/variant meta overrides), full argv/env composition for
  all three surfaces (web/tui/mcp, including the default-surface and
  no-persona-axis fallbacks and the explicit `scenario_corpus`/`run_id`
  threading), the empty-scenario-corpus zero-cells behavior, and scoring
  against a real on-disk parity-records bundle (solved / silent-bounce-fails
  / misroute-fails / below-threshold-fails / at-threshold-boundary-solves /
  empty-records-solves / schema-violation-blocks / missing-pointer-infra /
  crash-infra), plus the container<->host results-path mapping — all with
  zero docker, zero LLM spend.
