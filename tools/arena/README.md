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

## Layout

| File | Role |
|---|---|
| `arena/model.py` | `JobSpec`, `Cell`, `CellResult`, enumeration |
| `arena/plugins/base.py` | `JobTypePlugin` protocol + registry |
| `arena/plugins/bugfix.py` | bugfix plugin — wraps `bench.py` oracle (verify / drive) |
| `arena/plugins/paired_task.py` | paired-task plugin — one task through multiple treatments with shared oracle JSON |
| `arena/executor.py` | `CellExecutor` + `ContainerBackend` seam (`DockerBackend` \| `FakeBackend`) |
| `arena/placement.py` | sweep scheduler (concurrency, INFRA-vs-MODEL retry) |
| `arena/rollup.py` | job-agnostic leaderboard → `rollup.json` + `rollup.md` |
| `arena.py` | CLI: `plan` · `run` · `plugins` |
| `specs/*.yaml` | example job specs |
| `tests/test_*.py` | no-LLM, no-docker end-to-end (FakeBackend) |

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
(scheduler already round-robins `placement.hosts`); **P2** a first-class
persona-qa plugin (score = the 19-check review gate) + onboarding plugin so
persona QA runs through `arena run` rather than `run.py` directly; **P3** retire
`escalate.sh`/matrix `emit_run`, single front door.
