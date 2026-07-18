# Integration Guide

Operator runbooks for external systems that Kitsoki can authenticate with or
drive.

- [`github-app-setup.md`](github-app-setup.md) — set up the GitHub App that
  backs the live `@kitsoki` agent and short-lived local GitHub tokens.
- [`hosted-pog.md`](hosted-pog.md) — deploy POG on the GitHub-agent VM with
  every POG-owned route behind Kitsoki's invitation-only GitHub login.

Architecture context:

- [`../../architecture/github-agent.md`](../../architecture/github-agent.md) —
  how the `@kitsoki` dispatch service fits into Kitsoki.
- [`../../architecture/transports.md`](../../architecture/transports.md) —
  external thread transports and session keys.
