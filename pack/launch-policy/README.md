# Launch policy pack

This copy-based pack installs the day-one agent-launch enforcement layers into
a consumer repository: the Git primary-checkout hook, the Claude Code command
guard and settings wiring, and a local `agent_launch_policy:` seed.

```sh
pack/launch-policy/install.sh /path/to/consumer
pack/launch-policy/test-install.sh
```

The installer discovers adjacent Git repositories and records them as sibling
protected roots. It does not silently overwrite divergent managed files; use
`--force` after review.

To make direct interactive launches policy-aware in that shell, source the
installed activation file from the consumer repository:

```sh
source .kitsoki/launch-policy.sh
```

It puts `.kitsoki/bin` first on `PATH` and exports the Claude/Codex binary
override variables. The wrappers preserve native CLI arguments, ask `kitsoki
agent launch --raw --interactive` to enforce the local launch policy, then
delegate to the actual backend binary. Set `KITSOKI_AGENT_CLAUDE_REAL_BIN` or
`KITSOKI_AGENT_CODEX_REAL_BIN` only when the real executable is not otherwise
discoverable on `PATH`.
