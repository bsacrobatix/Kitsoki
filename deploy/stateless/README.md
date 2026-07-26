# Stateless kitsoki deployment

Container packaging for the stateless-orchestrator shape: kitsoki runs with a
read-only root filesystem, all durable state in Postgres
(`KITSOKI_DB_BACKEND=postgres`, `KITSOKI_PG_DSN`) and object storage (git
bundle backups via `kitsoki repo serve --backup`). `/scratch` is the single
writable mount (`KITSOKI_STATE_DIR=/scratch/state`,
`XDG_DATA_HOME=/scratch/state`, `TMPDIR=/scratch/tmp`,
`KITSOKI_CACHE_DIR=/scratch/cache` — set by the image).

- `Dockerfile` — multi-stage build (see header comments for the full
  read-only-rootfs contract). Build from the repo root:
  `make stateless-image` or
  `DOCKER_BUILDKIT=1 docker build -f deploy/stateless/Dockerfile -t kitsoki-stateless .`
- `entrypoint.sh` — creates the `/scratch` layout, then `exec kitsoki "$@"`.
- `docker-compose.yaml` — local approximation of prod: `kitsoki web` +
  `kitsoki repo serve` (both `read_only: true`) + `postgres:16` + minio as the
  S3 stand-in. Header comments explain the virtual-hosted-style minio wiring.

SQLite/local behavior is untouched: without the Postgres env the image (and
the plain binary) keeps today's sqlite default.
