# Orchestrator VM: persistent daemon + queue worker

Deploys the P0 "POG off the laptop" persistent half described in
`.context/p0-persistent-vm-orchestrator-plan.md`: a single always-on
DigitalOcean droplet ("orchestrator") that hosts the durable Kitsoki daemon
and the merge-queue worker for a project (POG, initially), while every
fixer/verifier/gate execution dispatches out to ephemeral droplets. This
directory is the deploy-time half of that plan; `docs/runbooks/pog-orchestrator-cutover.md`
is the operational half (how you actually move the laptop's live queue over).

## Topology

```
                                   DigitalOcean region sgp1
                         ┌──────────────────────────────────────────┐
                         │  Persistent VM: "orchestrator"            │
                         │                                          │
                         │  systemd --user (kitsoki, lingering)     │
                         │  ┌────────────────────────────────────┐  │
                         │  │ kitsoki-daemon.service              │  │
                         │  │  kitsoki daemon --addr 127.0.0.1:7777│  │
                         │  │  --db ~/daemon.sqlite               │  │
                         │  │  --config .kitsoki.yaml             │  │
                         │  │  (runstatus RPC/SSE, hosted stories, │  │
                         │  │   colony-runner / colony-drain, ...) │  │
                         │  └────────────────────────────────────┘  │
                         │                                          │
                         │  systemd (system)                        │
                         │  ┌────────────────────────────────────┐  │
                         │  │ kitsoki-queue-worker.service        │  │
                         │  │  kitsoki queue worker               │  │
                         │  │  --project  $PROJECT_ROOT (POG)     │──┼──▶ POG checkout, branch main
                         │  │  --target   main                    │  │    (landing branch lives HERE;
                         │  │  --gate     node scripts/kitsoki-ci.mjs│    clean merges via queue CAS)
                         │  │  --concurrency $CONCURRENCY          │  │
                         │  └──────────────┬─────────────────────┘  │
                         └─────────────────┼────────────────────────┘
                                            │ dispatch (vmpool; sibling
                                            │ agents' work — not in this dir)
                          ┌─────────────────┼─────────────────┐
                          ▼                 ▼                 ▼
                  ephemeral worker   ephemeral worker   ephemeral worker
                  kitsoki-worker-*   kitsoki-worker-*   kitsoki-worker-*
                  (s-4vcpu-16gb,     `kitsoki capsule    (destroyed after
                   boots, runs one   worker serve`,       Release; orphans
                   gate/session,     TLS + single-job     reconciled, never
                   destroyed)        token                billed forever)
                          │                 │                 │
                          └────────┬────────┴────────┬────────┘
                                   ▼                  ▼
                     Spaces bucket kitsoki-test.sgp1 (S3-compatible;
                     internal/objectstore) — sources/<sha>/ frozen source
                     bundles, runs/<id>/ traces + artifacts + WIP bundles
```

Nothing outside the orchestrator VM ever holds a long-lived credential:
ephemeral workers get presigned bucket URLs and a single-job bearer token
minted for their one droplet lease (`internal/capsule/vmpool/pki.go`). The two
durable secrets — the Spaces key and the DigitalOcean API token — live only in
`~/.config/kitsoki/daemon.env` on the orchestrator (see `provision.sh` and the
"Secrets" section below).

## What each unit does

### `kitsoki-daemon` — systemd **--user** unit, not a system unit

Installed by running `kitsoki daemon install-systemd` **as the `kitsoki`
system user**, not via a hand-written unit file in this directory — the
command (`cmd/kitsoki/daemon_systemd.go`) always writes a `systemd --user`
unit to `$HOME/.config/systemd/user/kitsoki-daemon.service` (there is no
`--user`/`--system` switch on the command itself; it is unconditionally a
user unit). `provision.sh` runs it via `runuser -u kitsoki` and
`loginctl enable-linger kitsoki` so that user's systemd instance starts at
boot with no interactive login ever required. Its rendered `ExecStart` is
`kitsoki daemon --addr <addr> --db <path> --config <path>` (see
`daemonInstallSystemdCmd` for the exact flags provision.sh passes:
`--working-directory`, `--addr`, `--db`, `--config`). It serves the
runstatus RPC/SSE surface and every hosted daemon-mode story (colony-runner,
colony-drain, etc.) as durable artifact jobs bound to a stable
`/s/<job-id>` link, reattaching sessions on restart per
`docs/architecture/` daemon-mode conventions.

### `kitsoki-queue-worker.service` — systemd **system** unit (this directory)

Runs `kitsoki queue worker` (`cmd/kitsoki/queue.go`) as the durable owner of
the merge queue's claim → prepare → gate → finalize loop against the
project checked out at `$PROJECT_ROOT`. It is the direct port of POG's
laptop launchd worker
(`ops/launchd/com.pog.kitsoki-queue.plist` in the POG repo), same flags
(`--gate`, `--target`, `--retry-delay 5m --max-retry-delay 30m
--max-attempts 5`), plus `--concurrency` — see the knob below. All of those
values live in `/etc/kitsoki/queue-worker.env`
(`EnvironmentFile=/etc/kitsoki/queue-worker.env` in the unit), never hardcoded
in the unit file itself.

### `kitsoki-queue-worker@.service` — templated variant

Not used by the P0 single-project topology. If the orchestrator ever hosts a
second project or a second protected target, instantiate this template
(`systemctl enable --now kitsoki-queue-worker@<name>.service`) instead of
duplicating the base unit — each instance reads its own
`/etc/kitsoki/queue-worker-<name>.env` and runs under its own
`--worker-id queue-worker-<name>`, so two instances never share
`PROJECT_ROOT`/`TARGET`/queue state or collide on worker identity.

## The concurrency knob

`kitsoki queue worker --concurrency N` runs `N` claim/prepare loops inside one
process, each independently claiming and preparing a candidate under its own
worker-id lease; only the FIFO/emergency head of the train ever finalizes, so
raising `N` cannot land candidates out of order or double-land one (see the
doc comment on `queueWorkerCmd` in `cmd/kitsoki/queue.go` and
`docs/architecture/merge-queue.md`). Changing concurrency is a single-file
edit and a restart, nothing else:

```
sudo $EDITOR /etc/kitsoki/queue-worker.env   # edit CONCURRENCY=<N>
sudo systemctl restart kitsoki-queue-worker
```

## Log locations

The queue worker is a plain system unit, so its journal is the usual:

```
journalctl -u kitsoki-queue-worker -f          # follow
journalctl -u kitsoki-queue-worker --since -1h # last hour
```

The daemon is a `systemd --user` unit under the `kitsoki` account, so either
run `systemctl --user` as that user, or — simpler, and works without a
login session — filter the system journal by the user-unit field, which
`journalctld` indexes regardless of who is reading it:

```
sudo journalctl _SYSTEMD_USER_UNIT=kitsoki-daemon -f
# or, as root, drive its own systemd --user instance directly:
sudo runuser -u kitsoki -- env HOME=/var/lib/kitsoki XDG_RUNTIME_DIR=/run/user/"$(id -u kitsoki)" \
	systemctl --user status kitsoki-daemon
```

Both units log to journald only (`StandardOutput=journal`); there is no flat
log file to rotate or manage by hand.

## Disk hygiene

The queue worker materializes a prepared speculative tree per candidate under
`$PROJECT_ROOT/.capsules/` (staging instances, WIP-preservation snapshots,
conflict-resolution artifacts) — that, not the ephemeral droplets, is the
disk consumer that lives on this VM long-term. Per repo convention
(`AGENTS.md`), reclaim it with the durable Capsule cleanup plan/apply pair,
never by hand-deleting under `.capsules/`:

```
kitsoki capsule cleanup plan  --project "$PROJECT_ROOT"
kitsoki capsule cleanup apply --project "$PROJECT_ROOT"
```

Wire whichever cadence the doctor's disk-capacity floor calls for (a
`systemd` timer running `capsule cleanup apply` on this same unit's schedule
is a natural follow-up; not included here — see "CLI gaps" in the delivering
agent's final report for what does and does not exist yet).

## Secrets

Exactly one env file holds real secrets: `~/.config/kitsoki/daemon.env`
(`/var/lib/kitsoki/.config/kitsoki/daemon.env`), read by
`EnvironmentFile=-%h/.config/kitsoki/daemon.env` in the daemon's rendered
unit. `provision.sh` writes it as a **placeholder template only** — commented
`KEY=` lines, no values — and never echoes a real secret anywhere. Fill in,
out of band, one time:

- `DO_KITSOKI_TEST_API_KEY` — Spaces secret key for the `kitsoki-test.sgp1`
  bucket (paired with access key ID `DO801QYJLKD3UM7ZEUKE`;
  `internal/objectstore`).
- `DO_GAGNRENOUS_TURD_API_KEY` — DigitalOcean API token for ephemeral droplet
  control (`internal/capsule/vmpool`).

`/etc/kitsoki/queue-worker.env` holds no secrets (project root, target ref,
gate command, concurrency, retry budget only) but is still installed
root-owned `0600` for consistency and because a gate command string is
sometimes operationally sensitive.

## Provisioning

```
sudo deploy/orchestrator/provision.sh --source-checkout /path/to/kitsoki
```

See the flag reference in `provision.sh`'s own header comment for
`--kitsoki-bin`, `--project-root`, `--target`, `--gate`, `--concurrency`,
`--daemon-addr`, `--install-dir`, and `--skip-services`. Provisioning is
idempotent and safe to re-run; it does **not** clone the project checkout the
queue worker will operate on, populate real secrets, or touch DNS/ingress —
those are explicit, verified steps in
`docs/runbooks/pog-orchestrator-cutover.md`.
