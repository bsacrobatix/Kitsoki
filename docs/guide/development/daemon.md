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
