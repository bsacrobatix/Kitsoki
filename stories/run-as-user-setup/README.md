# Run-As-User Setup

This no-LLM story applies macOS local-user delegation for coding-agent launches.
It opens on a small setup control screen. Use `apply` to run the deterministic
setup with non-interactive sudo; typed `accept` or `ok` input takes the same
path. Use `details` only when you want the generated receipt and setup summary,
and use `edit values` when a default user, path, or CLI location is wrong. If
sudo needs a password, the story moves to an authorization screen with the
`sudo -v` handoff and a retry action instead of dumping a generic command
failure.

This setup is not a replacement for launch policy or sandboxing. Launch policy
decides whether an agent may start in a working directory; the delegated macOS
user is the separate OS account that should lack write permission to protected
operator checkouts after the backend starts.

The `codex_bin` and `claude_bin` defaults are `auto`. Apply resolves them from
the Kitsoki process PATH plus common macOS locations before writing sudoers or
wrappers. Missing backend CLIs move to a CLI-path screen with edit-and-retry
actions; the story does not wait until the wrapper smoke test fails with a raw
`command not found`.

Run it from the project checkout:

```sh
kitsoki run @kitsoki/run-as-user-setup
```

It applies:

- a `.kitsoki.local.yaml` `agent_user_delegation:` receipt block;
- the matching `agent_launch_policy:` block;
- macOS account/group commands;
- root-owned `codex` and `claude` PATH wrappers;
- a narrow sudoers snippet;
- one capsule-assignment recipe;
- validation commands for delegated capsule write, protected checkout write-deny,
  and launch-policy rejection.

The apply path uses `host.run` and `sudo -n`. That makes it deterministic and
safe for headless runs: success means the setup was applied, missing sudo
credentials become a dedicated authorization-retry state, missing backend CLI
paths become a dedicated edit-and-retry state, and other command failures remain
visible story failures. The automated path applies the account, group, wrappers,
sudoers, sample capsule, delegated write probe, protected-root write-deny probe,
and wrapper smoke tests; the launch-policy rejection command remains printed for
explicit operator review so the story does not accidentally start an interactive
backend.

`kitsoki agent launch` consumes the configured `wrapper_bin` directly, records
`run_as_user` in the launch plan, and no longer needs the wrapper directory
prepended to `PATH`. The broader `kitsoki run` / `kitsoki web` live agent paths
still rely on backend CLIs resolving from the operator environment, so keep
using the wrapper directory on `PATH` for those surfaces.

The macOS setup warning remains visible until `.kitsoki.local.yaml` contains an
enabled `agent_user_delegation:` block with both `run_as_user` and
`wrapper_bin`. A `run_as_user` value without wrappers is incomplete because
Kitsoki cannot delegate the backend CLI launch.

Test it without LLM, sudo, or network:

```sh
kitsoki test flows stories/run-as-user-setup/app.yaml
```
