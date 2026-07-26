# Kitsoki daemon

`kitsoki daemon` runs the multi-story web and JSON-RPC surface as a durable
job service for a task-oriented frontend. It uses the normal session SQLite
database and adds one artifact-job row for every session started through the
daemon.

## Start locally

From the project whose stories and `.kitsoki.yaml` the daemon should serve:

```sh
kitsoki daemon --addr 127.0.0.1:7777
```

Open `http://127.0.0.1:7777/`. The Home view includes **Current jobs** when
daemon-backed jobs exist. Each row has a stable `/s/<job-id>` link. A frontend
can read the same records through JSON-RPC:

```json
{"jsonrpc":"2.0","method":"runstatus.jobs.list","params":{}}
```

Bind only to trusted localhost or an authenticated internal proxy. The daemon
does not add HTTP authentication.

## Run graph-declared campaigns

Daemon mode can watch generic campaign nodes from the project graph and dispatch
their actions through the story runtime. Enable the provider in
`.kitsoki.yaml`:

```yaml
campaigns:
  catalog: graph/catalog.yaml
  type: campaign
  max_definitions: 200
  max_bytes: 262144
```

The catalog path is fixed by daemon configuration. A campaign node uses the
following contract:

```yaml
schema: project/campaign/v1
id: recurring-review
type: campaign
title: Recurring review
status: active
application_id: review-runner
enabled: true
paused: false
cadence_seconds: 300
budget:
  max_ticks_per_day: 48
  max_concurrency: 1
action:
  kind: story-intent
  story: stories/review/app.yaml
  intent: review
  input:
    scope: current
```

Only `status: active` nodes whose `application_id` exactly matches the calling
application are visible. `host.campaign.watch` reconciles those definitions and
starts one process watcher for that application:

```yaml
host:
  call: host.campaign.watch
  with:
    poll_seconds: 30
```

The result contains `watch_job_ref`, its compatibility alias `job_id`,
`job_refs`, `campaign_count`, and `restored`. The watcher reference identifies
process-bound polling. Entries in `job_refs` identify durable artifact jobs
created for actions that were due during that call. Every dispatched action
also receives its campaign idempotency key as `request_id`.

`host.campaign.snapshot` returns bounded deterministic status for the same
application. Its required `max_campaigns` and `max_bytes` inputs must remain
within the platform ceilings and cannot request an unbounded response. The provider
rejects malformed definitions and unsupported action kinds instead of
truncating or invoking external command authority. Durable claims enforce
enabled and paused state, cadence, daily tick budget, concurrency, and
idempotency before exact story-intent dispatch.

## Federate VM and workstation workers

Each worker is an ordinary self-contained daemon: it owns its SQLite database,
profiles, jobs API, stable session links, and restart behavior. A local
controller may aggregate several workers by adding machine-local configuration
to `.kitsoki.local.yaml`:

Worker IDs are lowercase slugs and must be unique; `local` is reserved for the
controller's own daemon.

```yaml
daemon_federation:
  workers:
    - id: thin-do
      label: Thin DigitalOcean worker
      placement: thin
      endpoint: http://127.0.0.1:17777
      tunnel:
        host: 203.0.113.10
        user: kitsoki
        local_port: 17777
        remote_host: 127.0.0.1
        remote_port: 7777
        identity_file: /home/me/.ssh/kitsoki-worker
        known_hosts_file: /home/me/.ssh/known_hosts

    - id: gx10
      label: GX10 local-model workstation
      placement: local-model
      endpoint: http://127.0.0.1:17778
      tunnel:
        host: gx10.lan
        user: kitsoki
        local_port: 17778
        remote_host: 127.0.0.1
        remote_port: 7777
        identity_file: /home/me/.ssh/kitsoki-worker
        known_hosts_file: /home/me/.ssh/known_hosts
```

Starting the local `kitsoki daemon` supervises one strict OpenSSH local forward
per `tunnel` entry. It uses batch mode, fails when forwarding cannot be
established, verifies the host against the explicit known-hosts file, and
reconnects after a dropped SSH process. An `endpoint` without `tunnel` is also
supported for an operator-provided authenticated HTTPS/private-network path.

The Home view shows local and remote worker health plus their durable jobs.
`runstatus.jobs.list` remains an array and adds `worker_id`, `worker_label`,
`placement`, and `open_url`; job IDs are not rewritten. Consumers must key a
federated row by `(worker_id, job_id)`. `runstatus.workers.list` returns
`connecting`, `online`, `degraded`, or `offline` health, last-seen/error, and
job count without exposing SSH targets or key paths. Polling is concurrent and
partial: an offline worker remains visible but cannot hide local or healthy
worker jobs.

Keep subscription OAuth where its CLI login lives. A typical early-adopter
topology is:

- local daemon: native Claude/Codex CLI profiles using ambient subscription
  authentication;
- thin VM: Claude/Codex CLI retargeted to an OpenAI- or Anthropic-compatible
  API through VM-local environment variables;
- powerful workstation/GX10: a `builtin.local_llm` profile or an API profile
  pointing at that machine's model server.

Do not copy local subscription credential stores to a VM as part of federation.
Worker credentials belong in that worker's `~/.config/kitsoki/daemon.env` or
other provider-native secret store.

### Worker deployment

Install Kitsoki on the worker, configure its local profiles, and keep the
daemon on loopback:

```sh
kitsoki daemon install-systemd --addr 127.0.0.1:7777 \
  --working-directory /opt/kitsoki-worker
systemctl --user daemon-reload
systemctl --user enable --now kitsoki-daemon
curl --fail --silent http://127.0.0.1:7777/rpc \
  -H 'content-type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"runstatus.jobs.list","params":{}}'
```

The curl probe must run on the worker or through an SSH tunnel. Never expose
port 7777 publicly; daemon HTTP has no authentication.

### Ephemeral workers (phase 2)

The phase-one registry is static. A later provisioner owns create, readiness,
drain, destroy, and cost metadata for per-second VMs. Provisioning must not
change the stable `(worker_id, job_id)` contract or make the UI report a VM as
destroyed before its durable jobs and evidence have been drained.

## Restart contract

Daemon-created jobs persist these facts before they are exposed: job ID,
session ID, story path, status, phase, trace path, and run URL. The same job ID
is also stored as the `daemon:<job-id>` external session key.

After a service or OS restart, the daemon:

1. marks prior active artifact jobs `interrupted` with reason
   `daemon_restarted`;
2. reattaches each persisted daemon session under its original job ID;
3. restores its trace-backed state and stable link; and
4. returns successfully restored session jobs to `running`.

This does not replay an arbitrary handler that was executing at the instant of
the crash. `internal/jobs` marks process-bound `running` or `awaiting_input`
rows failed with `process_died_mid_job`; blindly repeating a shell command,
agent call, or external write could duplicate side effects. A story that needs
automatic work replay must provide an idempotent, checkpointed executor and an
explicit resume action.

Campaign schedules and their next due times survive the same restart. Any
campaign dispatch recorded as running is marked `interrupted` with reason
`daemon_restarted`; neither it nor the process watcher is claimed to have
resumed. Restored artifact-job sessions keep their stable references, and the
application starts a new watcher by invoking `host.campaign.watch`.

## Install a systemd user service

Install Kitsoki first; do not create a unit from `go run`, whose executable is
temporary. Review the generated unit, then install it:

```sh
kitsoki daemon install-systemd --dry-run
kitsoki daemon install-systemd
systemctl --user daemon-reload
systemctl --user enable --now kitsoki-daemon
```

The generated unit records the current project as `WorkingDirectory`, the
actual Kitsoki executable, `.kitsoki.yaml`, the persistent database path, and
`127.0.0.1:7777`. Override those with `--working-directory`, `--config`,
`--db`, and `--addr`. Replacing a different existing unit requires `--force`.

Optional provider credentials and harness variables belong in:

```text
~/.config/kitsoki/daemon.env
```

The unit reads that file with `EnvironmentFile=-`, so an absent file is valid.
Keep it mode `0600` when it contains secrets.

To start the user service during boot before an interactive login, enable
linger once as an administrator or the target user:

```sh
loginctl enable-linger "$USER"
```

Operate and inspect the service with:

```sh
systemctl --user status kitsoki-daemon
journalctl --user-unit kitsoki-daemon -f
systemctl --user restart kitsoki-daemon
```

The unit uses `Restart=on-failure`, a two-second retry delay, and a 15-second
shutdown timeout. SIGTERM follows the daemon's normal HTTP and registry close
path before systemd escalates.
