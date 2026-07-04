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

## Cost discipline

`run` defaults to the **no-LLM** path: for bugfix that is `bench.py verify`
(prove the oracle is armed: RED@baseline → GREEN@fix) executed inside the
container — exercising enumerate → container → score → rollup with **zero spend**.
`--live` is the only way to spend and is always explicit. The pipeline is fully
unit-tested with `FakeBackend` (no docker, no LLM): `tools/arena/tests/`.

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
