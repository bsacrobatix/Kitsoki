# Unified comparison-job runner — design

**Goal:** one tool that runs any large comparison/sweep job — bug-fixing,
onboarding, persona QA, … — with every cell executing **in a Docker container**,
and containers placed on **either the local Docker host or a remote VM** (the VM
is just another Docker host). Subsumes `tools/bugfix-bakeoff/` and the matrix
half of `tools/product-journey/run.py`.

## What exists today (three strands)

| Strand | Strength | Gap |
|---|---|---|
| **product-journey** (`run.py` matrix) | Planner/rollup **brain**: matrix emit (`target × persona × scenario`), GitHub target-proof + selection contract, deterministic rollup aggregation + validation + Slidey deck | **Emits contracts, never executes** — cells are shell-command templates for an external agent |
| **bugfix-bakeoff** (`external/`) | Cell **executor**: `drive_cell.sh` (per-cell worktree isolation, retry/backoff, health-classify INFRA-vs-MODEL, oracle score), Docker scoring already present (`Dockerfile.repo-runtime`, `run_repo_docker.sh`) | Thin sweep (`escalate.sh` sequential, `aggregate.py` rollup); Docker only at **score** time, not drive time |
| **VM/remote** (recent) | `drive_cell.sh` pushed over SSH to a VM; **completion-state** JSON for remote-control polling; `provision_vm.sh` installs both harness CLIs | Same `drive_cell.sh` code path, just relocated — no container at drive time |

Key realisation: **product-journey is the brain without a body; bugfix-bakeoff
is the body without a general brain.** The unified tool = product-journey's
matrix/rollup brain + bugfix-bakeoff's cell executor + a container/placement
layer, with **job-type as a plugin**.

## The two orthogonal axes (per Brad)

1. **Containerization is always on.** Every cell runs in a Docker container — the
   unit of isolation + reproducibility. Replaces the host-worktree drive path.
2. **Placement is orthogonal.** Where that container runs is just a choice of
   Docker host: the local daemon, or a remote VM's daemon (`docker --context
   vm-N` / `DOCKER_HOST=ssh://vm-N`). **The VM executes the containers** — it is
   not a third sibling backend. Local is the degenerate "place on local daemon".

So: **cell → image → container instance → placed on {local | vm-N} Docker host →
completion-state collected back.** The existing VM remote-control + completion-
state machinery becomes "place a container on a remote Docker host and poll it".

## Proposed architecture (5 layers)

```
┌─ 1. JOB SPEC / MATRIX ───────────────────────────────────────────────┐
│  declarative: axes (targets × variants × job-type) → enumerated cells │
│  reuse: product-journey matrix emit, github target-proof, selection   │
│  contract, seeded persona/variant assignment                          │
└──────────────────────────────────────────────────────────────────────┘
            │ cells[]
┌─ 2. JOB-TYPE PLUGIN ─────────────────────────────────────────────────┐
│  interface per job type (bugfix | onboarding | persona-qa | …):       │
│    • image(cell)        → which container image / build context       │
│    • drive(cell)        → prompt + story + harness mode                │
│    • evidence(cell)     → required artifacts / contract                │
│    • score(cell, out)   → verdict {solved|partial|failed|...} + metrics│
│  bugfix → oracle inject+run; persona-qa → 19-check review gate;        │
│  onboarding → profile/commands/target assertions                      │
└──────────────────────────────────────────────────────────────────────┘
            │ per-cell job
┌─ 3. CELL EXECUTOR (in container) ────────────────────────────────────┐
│  build/pick image → run container → inside: drive.sh (claude|codex    │
│  backend) drives kitsoki MCP → emit trace + completion-state + result │
│  reuse: drive.sh backend seam, retry/backoff, classify_cell()         │
└──────────────────────────────────────────────────────────────────────┘
            │ container spec + completion-state contract
┌─ 4. PLACEMENT / SCHEDULER ───────────────────────────────────────────┐
│  schedule N containers across Docker hosts (local daemon | vm pool    │
│  via docker context). concurrency cap, retry, INFRA-vs-MODEL health.  │
│  poll completion-state for remote cells. provision_vm = register a    │
│  VM as a docker host/context.                                         │
└──────────────────────────────────────────────────────────────────────┘
            │ result artifacts per cell
┌─ 5. ROLLUP / LEADERBOARD ────────────────────────────────────────────┐
│  job-type-agnostic result schema → aggregate (by target/variant/      │
│  job-type) → validate → Slidey deck + markdown. reuse product-journey │
│  rollup + bakeoff by_treatment/by_candidate buckets.                  │
└──────────────────────────────────────────────────────────────────────┘
```

## The unifying data model

```
JobSpec
  job_type:  bugfix | onboarding | persona-qa | <plugin>
  targets[]:  { id, repo, stack, proof… }          # github-targets.json shape
  variants[]: { id, backend, model, effort, … }    # candidates.yaml shape (was "candidate")
  axes:       extra per-job axis (bug | persona | scenario)
  placement:  { hosts: [local, vm-1, …], concurrency, retry }
  → enumerates → Cell[]

Cell = { id, target, variant, axis-coords, job_type, image, status }

CellResult  (job-type-agnostic)
  cell, verdict, metrics{cost_usd, tokens, wall_s, …},
  health{class: infra:* | model:result | incomplete}, evidence_refs[], trace_ref
```

`candidate` (bakeoff) and `persona/scenario` (product-journey) both collapse into
**variant + axis** on a cell. `treatment` (kitsoki vs single) is just another
variant axis.

## Where each existing piece lands

- **Reused almost as-is:** product-journey rollup/validation/target-proof
  (`build_matrix_rollup`, `validate_matrix_bundle`, `fetch_github_target_proof`);
  bakeoff `bench.py classify_cell` (health), `decide_quality` (verdict logic),
  `aggregate.py` buckets; `drive.sh` backend seam; completion-state schema.
- **Generalised:** scenario→job-type plugin; `emit_run_command` shell template →
  a real container dispatch; `escalate.sh` sequential → a parallel placement
  scheduler; oracle-only scoring → pluggable `score(cell)`.
- **New:** (a) container-at-drive-time (orchestrator image with both CLIs +
  kitsoki, cell repo mounted), (b) placement scheduler over docker contexts,
  (c) the job-type plugin registry, (d) one CLI front door.

## Container model

- **Orchestrator image** (new): `golang + node + python + rust` (extend
  `Dockerfile.repo-runtime`) **+ `claude` + `codex` CLIs + kitsoki binary**. The
  cell's target repo is mounted/cloned inside; `drive.sh` runs in-container.
- One container per cell (isolation = container, not worktree). Score either in
  the same container or a sibling per current Docker-score path.
- Placement = `docker --context <host> run …`. A VM is provisioned once
  (`provision_vm.sh` → install Docker + register as a remote context) and then
  holds N concurrent cell containers.

## Locked decisions (2026-06-29)

- **Shape:** Python, extending product-journey's matrix/rollup brain.
- **Home/name:** new `tools/arena/`; port bugfix + persona-qa on as the first two
  plugins; retire `escalate.sh` + matrix `emit_run` later.
- **First slice (P0):** bugfix job-type + container-at-drive + **local** placement
  + rollup, proven on `query-string`.
- **No-LLM skeleton proof:** the P0 cell runs the oracle **arming** path
  (`bench.py verify`: RED@baseline → GREEN@fix) *inside a container* — exercises
  enumerate → container exec → score → rollup with **zero spend**. The paid
  `live` drive (drive.sh) is the same plumbing, gated behind an explicit flag.
- **DI:** the container layer is a `ContainerBackend` interface (DockerBackend |
  FakeBackend) so the whole pipeline is unit-testable with no real docker/LLM.

## Open decisions (deferred to later phases)

1. **Language/shape of the new tool.** (a) Python orchestrator extending
   `product-journey/run.py`'s matrix code (fastest reuse of the rollup brain), or
   (b) a Go `cmd/` (first-class, typed, embeds with kitsoki), or (c) thin bash
   over the existing scripts. Reuse favours **Python**; longevity favours **Go**.
2. **Name + home.** e.g. `tools/arena/`, promote `tools/bakeoff/` as the umbrella,
   or a `kitsoki compare` subcommand. (Leaning `tools/arena/` to avoid clobbering
   the proven bugfix-bakeoff during migration.)
3. **Migration vs greenfield.** Build the new runner and port bugfix + persona-qa
   onto it as the first two plugins (retiring `escalate.sh` / matrix `emit_run`),
   or leave both harnesses and only add the container/placement layer under them.
4. **Scope of first slice.** Recommend: **one job-type (bugfix) + container drive
   + local placement + rollup**, proven against the existing query-string project,
   THEN add persona-qa plugin + VM placement. (Walking skeleton first.)

## Recommended phasing

- **P0 — walking skeleton:** Cell + JobSpec model; job-type plugin interface;
  bugfix plugin wrapping today's oracle; container-at-drive (local placement
  only); rollup reusing product-journey aggregation. Prove on `query-string`.
- **P1 — placement:** docker-context scheduler (local + 1 VM), concurrency,
  completion-state polling, INFRA-vs-MODEL retry.
- **P2 — second plugin:** persona-qa as a plugin (its score = the 19-check review
  gate) over the same 10 github-targets; onboarding plugin third.
- **P3 — consolidate:** retire `escalate.sh` + matrix `emit_run`; one CLI; docs.

## No-LLM / cost discipline

- Matrix emit, target-proof (cached), rollup, validation stay **deterministic /
  no-LLM** (as both harnesses already are).
- Only the cell **drive** spends; gate it behind explicit run, reuse bakeoff's
  preflight + completion-state so a sweep is fully prepared/cost-estimated before
  any spend. Persona-qa keeps its replay/flow no-LLM path for CI.
