# Deploy

Operator documentation for running kitsoki as a service, beyond the local
binary.

- [`stateless-container.md`](stateless-container.md) — the stateless
  container shape: read-only rootfs, Postgres + object-store state, the
  `KITSOKI_STATE_DIR` writable-root contract, and the git repo service
  runbook (serve / backup / restore drill). Packaging sources live in
  [`../../deploy/stateless/`](../../deploy/stateless/README.md).

Architecture background:
[`../architecture/storage-backends.md`](../architecture/storage-backends.md)
and [`../architecture/graph-storage.md`](../architecture/graph-storage.md).
For the long-running local service, see `kitsoki daemon` (not covered here).
