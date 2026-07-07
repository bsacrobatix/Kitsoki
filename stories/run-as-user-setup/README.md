# Run-As-User Setup

This no-LLM story applies macOS local-user delegation for coding-agent launches.
It opens on the setup plan screen; use `apply` to run the setup with
non-interactive sudo, or `accept`/`ok` after reviewing the plan to do the same.
If sudo needs a password or an agent CLI needs interactive login, the story stops
with the command output instead of hanging on an invisible prompt.

Run it from the project checkout:

```sh
kitsoki run @kitsoki/run-as-user-setup
```

It plans and can apply:

- a `.kitsoki.local.yaml` `agent_user_delegation:` receipt block;
- the matching `agent_launch_policy:` block;
- macOS account/group commands;
- root-owned `codex` and `claude` PATH wrappers;
- a narrow sudoers snippet;
- one capsule-assignment recipe;
- validation commands for delegated capsule write, protected checkout write-deny,
  and launch-policy rejection.

The apply path uses `host.run` and `sudo -n`. That makes it deterministic and
safe for headless runs: success means the setup was applied, while missing sudo
credentials or missing CLI authentication become visible story failures. The
automated path applies the account, group, wrappers, sudoers, sample capsule,
delegated write probe, protected-root write-deny probe, and wrapper smoke tests;
the launch-policy rejection command remains printed for explicit operator review
so the story does not accidentally start an interactive backend.

The current implementation still relies on PATH wrappers for the actual backend
switch. The `agent_user_delegation:` config is the local setup receipt and
startup-warning gate; first-class `run_as_user` launch support is a future
runtime slice.

Test it without LLM, sudo, or network:

```sh
kitsoki test flows stories/run-as-user-setup/app.yaml
```
