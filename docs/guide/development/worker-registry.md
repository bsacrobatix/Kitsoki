# Worker Registry

`kitsoki worker` manages the machine-local **worker registry**: the
canonical, unified set of remote worker identities that daemon federation
(observe) and Capsule CI (dispatch) can be pinned against, plus the
`enabled` bit and advertised capabilities operator tooling (and, eventually,
a portal Federation panel) reads. This is the standing-autonomy proposal's
§9 "Federation" ask (ask 4): "Unify Kitsoki's two remote-worker systems
behind one registry and give the user actual controls."

`kitsoki worker` is a different command from
[`kitsoki capsule worker`](capsule-ci.md), which runs a single sealed Capsule
execution envelope inside a prepared executor process. This command instead
manages the registry of worker identities those executors and daemon
federation dispatch against.

## Why two existing systems, one registry

Kitsoki already has two independently evolved remote-worker mechanisms, and
this feature does not merge their Go types — it unifies how they are
**operated**:

- **`daemon_federation.workers[]`** ([daemon.md](daemon.md)) is
  **observe-only**: health-polled (`connecting|online|degraded|offline`),
  SSH-tunnel-aware, machine-local (`.kitsoki.local.yaml`), used by the
  `kitsoki daemon` job aggregation view.
- **Capsule CI `remotes:`** ([capsule-ci.md](capsule-ci.md)) is
  **dispatch-only**: project-scoped (checked into a project's
  `.kitsoki/ci.yaml`), https-only, credential-env-based, used to route a
  sealed CI pipeline execution to a named executor.

They stay exactly as they are — `daemonfederation.Config` and
`internal/capsule/ci.Config` are unmodified, and every existing config and
call site keeps working with zero changes. The registry
(`internal/workerregistry`) adds a third, canonical, richer shape — the
superset the proposal describes — and reads the legacy `daemon_federation`
block as a back-compat fallback for its own listing/CLI/placement purposes.
Capsule CI `remotes:` stay project-scoped and are not automatically folded
into this machine-wide registry; a future iteration may add an explicit,
opt-in translation, but today they are independent and unaffected.

## Configuration: the canonical `workers:` block

Put the registry in `.kitsoki.local.yaml` (never the checked-in
`.kitsoki.yaml` — endpoints, tunnels, and credential env names are
machine-local or secret-bearing):

```yaml
workers:
  - id: build-vm
    label: Build VM
    placement: workstation      # thin | workstation | local-model
    endpoint: http://127.0.0.1:17777   # optional; omit for credential-only remotes
    enabled: true
    credential_env: BUILD_VM_TOKEN     # optional; env var name, never a value
    tunnel:                             # optional; same shape as daemon_federation's
      host: worker.example
      user: kitsoki
      local_port: 17777
      remote_host: 127.0.0.1
      remote_port: 7777
      identity_file: keys/worker
      known_hosts_file: keys/known_hosts
    capabilities:
      placements: [container, host]
      isolation: sandboxed
      networks: [git-mirror, package-mirror]

  - id: gx10
    label: GX10 local-model workstation
    placement: local-model
    enabled: true
    capabilities:
      placements: [host]
      isolation: none
      networks: []
```

Field notes:

- `id` is a unique lowercase slug (same rule as `daemon_federation.workers[]`:
  `^[a-z][a-z0-9-]{0,62}$`, and `local` is reserved).
- `placement` reuses `daemonfederation`'s existing enum verbatim
  (`thin`, `workstation`, `local-model`) rather than declaring a parallel
  one — one placement vocabulary across both federation surfaces.
- `capabilities` is the config-declared subset of
  `internal/capsule/executor.Capabilities` that makes sense to advertise
  ahead of a dispatch (`placements`, `isolation`, `networks`); the
  runtime-only fields (`id`, `environment_refs`, `cancellable`) are populated
  by the executor package itself at prepare time, not declared here.
- `enabled` is the operator control this registry adds that
  `daemon_federation.workers[]` never had — a disabled worker is skipped by
  placement checks and reported by `kitsoki worker list`/`drain`, but the
  registry does not itself stop `daemon_federation`'s Pool from polling a
  disabled worker's health (health observation and dispatch eligibility are
  deliberately separate concerns).

### Back-compat: `daemon_federation.workers[]`

When the `workers:` block is absent (or empty after base+local merge), the
registry falls back to translating `daemon_federation.workers[]` (which
takes zero code changes to keep working — see [daemon.md](daemon.md)) into
registry entries:

- `enabled` defaults to `true` — daemon federation has no enabled bit; every
  configured worker is implicitly usable today.
- `capabilities` is empty — daemon federation never declared
  isolation/network capabilities. A placement policy with non-empty
  `worker_classes` still evaluates the entry's `placement` field (translated
  1:1 from the daemon-federation worker's `placement`), so class-based
  placement policy works against legacy configs unchanged; only
  capability-shaped checks beyond class have nothing to evaluate for these
  entries.

The canonical `workers:` block always takes precedence over
`daemon_federation.workers[]` when both are present.

### Merge convention

`workers:` follows the exact same base(`.kitsoki.yaml`) + local
(`.kitsoki.local.yaml`) merge rule as `daemon_federation.workers[]` and every
other machine-local block in `webconfig.WebConfig`: a non-empty local
`workers:` list **replaces the base list whole** (not a field-level merge —
restate every entry you want in the local file). See
`internal/webconfig/webconfig.go`'s package doc for the general convention.

## CLI

```sh
kitsoki worker list [--json] [--config PATH] [--poll-timeout DURATION]
kitsoki worker add --id ID --label LABEL --placement CLASS [flags...] [--config PATH]
kitsoki worker remove <id> [--config PATH]
kitsoki worker enable <id> [--config PATH]
kitsoki worker disable <id> [--config PATH]
kitsoki worker drain <id> [--config PATH] [--poll-timeout DURATION]
```

`--config` defaults to `.kitsoki.yaml` in the current directory; every
subcommand derives the sibling `.local.yaml` from it (the same
`webconfig.LocalConfigPath` convention used everywhere else) and mutates only
that local file.

### `list`

Prints a table by default, or the stable JSON projection with `--json`.
`list` performs its **own short-lived HTTP poll** of entries that declare an
`endpoint` — it does not require a separately running `kitsoki daemon`
process, since `daemonfederation.Pool` is itself an HTTP client. Entries
without an `endpoint` (e.g. credential-env-only remotes) report
`health: "unknown"`. This is a deliberate judgment call: a CLI invocation is
a fresh process each time, so "cached" health has no meaningful home to live
in without inventing a new local daemon dependency; polling live, bounded by
`--poll-timeout` (default 2s), keeps the command self-contained and honest
about a worker that is actually unreachable right now.

### `--json` — the portal projection contract

```json
[
  {
    "id": "build-vm",
    "label": "Build VM",
    "placement": "workstation",
    "health": "online",
    "capabilities": {
      "placements": ["container", "host"],
      "isolation": "sandboxed",
      "networks": ["git-mirror", "package-mirror"]
    },
    "enabled": true,
    "jobs": 2
  }
]
```

This is `workerregistry.Projection`, the stable shape a future portal
Federation panel (POG/cross-repo, out of scope for this repo) consumes: **id,
label, placement, health, capabilities, enabled** — plus a `jobs` count where
cheaply derivable. It **never** includes credentials, tunnel identity file
paths, endpoints, or any other machine-local secret material; `Projection` is
a distinct Go type built field-by-field from `Entry`; it is not `Entry` with
some fields hidden, so a future field added to `Entry` without a JSON tag
still cannot leak into it by accident.

### `add` / `remove` / `enable` / `disable`

Edit `.kitsoki.local.yaml` safely: read, decode the `workers:` YAML node,
apply the change, validate the result (same rules as config-file load), and
atomically rewrite the file (temp file + rename). Every other top-level key
in the file — `agent_launch_policy`, `daemon_federation`, `harness_profiles`,
etc. — is preserved verbatim; only the `workers:` sequence is touched.
Comment preservation is not attempted.

```sh
kitsoki worker add \
  --id build-vm --label "Build VM" --placement workstation \
  --endpoint http://127.0.0.1:17777 \
  --capability-placement container --capability-placement host \
  --capability-network git-mirror --isolation sandboxed \
  --enabled=true
```

### `drain`

Advisory/soft-drain only — **no forceful kill**. It sets `enabled: false` (so
no new dispatch targets the worker) and, if the worker declares an
`endpoint`, polls it once to report jobs still attributed to it:

```
$ kitsoki worker drain build-vm
draining "build-vm": enabled=false, no new dispatch will target it
2 job(s) still attributed to this worker (not cancelled — drain is advisory):
  - job-abc123 (stories/build.md) status=running
  - job-def456 (stories/fix.md) status=running
```

If the worker has no endpoint or is unreachable, `drain` says so plainly
("no endpoint configured for this worker: live jobs could not be checked" /
"worker unreachable: live jobs could not be checked") rather than guessing.

### Per-dispatch pinning: `--worker`

`kitsoki capsule ci run --worker <id> [--lane LANE]` pins one dispatch to a
specific registered worker, overriding the pipeline's declared `executor:`.
The name must still resolve through the normal `internal/capsule/ci`
executor selection (built-in name or a `remotes:` entry in that project's
`.kitsoki/ci.yaml`) — pinning changes routing only, it does not invent a new
transport or bypass remote configuration.

Before dispatch, the pin is policy-checked (see
[launch-policy.md](../agents/launch-policy.md#placement-policy-federation)):

1. `<id>` must resolve to an **enabled** entry in the project's worker
   registry (`--worker vm-x` naming an unknown or disabled worker is denied
   before any envelope is built).
2. If the project's `.kitsoki.local.yaml` declares
   `agent_launch_policy.placement`, the worker's `placement` class must
   satisfy the dispatch's lane (`--lane`, defaulting to the pipeline name)
   per that lane's `worker_classes`. A lane not listed at all is denied once
   `placement:` is non-empty — this is how a lane like `delivery` stays
   local-only by simply never appearing in the map.

```sh
kitsoki capsule ci run change --workspace w1 --worker build-vm --lane build
```

Placement denial surfaces before the pipeline's executor is even selected —
launch preflight, not runtime, per invariant SA-I7.

## Relationship to `executor.ValidateCapabilities`

Placement policy (`agent_launch_policy.placement`, enforced by
`AgentLaunchPolicy.CheckPlacement`) and the sealed-envelope capability check
(`executor.ValidateCapabilities` in `internal/capsule/executor`) are two
separate, sequential gates for a remote dispatch:

1. **Placement preflight** (this feature): before any envelope exists, is
   this lane even allowed to target a worker of this class/network profile?
   Fails fast with a deterministic error.
2. **Capability validation** (pre-existing, untouched): once a sealed
   envelope's `Policy` is being prepared against a specific provider, do that
   provider's advertised `Capabilities` actually satisfy the envelope's
   `Policy`? This remains the runtime enforcement layer; placement preflight
   does not weaken or replace it.

A lane can never silently escalate isolation or egress by satisfying only
one of the two gates.

## Non-goals (v1)

- **Provisioning.** The registry manages workers that already exist; VM
  create/drain/destroy is the separate, not-yet-built "ephemeral workers"
  work.
- **Trust bootstrap.** SSH keys and worker tokens remain operator-owned,
  exactly as [daemon.md](daemon.md) already documents — nothing here changes
  how a worker gets trusted in the first place.
- **Remote catalog federation.** A stream dispatched to a remote worker ships
  as a content-addressed source bundle plus a sealed envelope; its catalog is
  a file in that bundle. No new catalog-federation protocol is introduced.
- **Portal Federation panel.** The `--json` projection above is the contract
  a panel would consume; building that panel is a POG/cross-repo concern out
  of scope for this repository.
