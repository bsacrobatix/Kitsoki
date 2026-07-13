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
