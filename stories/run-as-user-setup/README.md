# Run-As-User Setup

This no-LLM story guides an operator through macOS local-user delegation for
coding-agent launches. It opens on the setup plan screen; use `plan`, `accept`,
or `ok` to generate the plan if needed, then use `accept` or `ok` after the
listed validation probes pass.

Run it from the project checkout:

```sh
kitsoki run @kitsoki/run-as-user-setup
```

It does not create users, edit sudoers, or run privileged commands itself. It
generates:

- a `.kitsoki.local.yaml` `agent_user_delegation:` receipt block;
- the matching `agent_launch_policy:` block;
- macOS account/group commands;
- root-owned `codex` and `claude` PATH wrappers;
- a narrow sudoers snippet;
- one capsule-assignment recipe;
- validation probes for delegated capsule write, protected checkout write-deny,
  and launch-policy rejection.

The current implementation still relies on PATH wrappers for the actual backend
switch. The `agent_user_delegation:` config is the local setup receipt and
startup-warning gate; first-class `run_as_user` launch support is a future
runtime slice.

Test it without LLM, sudo, or network:

```sh
kitsoki test flows stories/run-as-user-setup/app.yaml
```
