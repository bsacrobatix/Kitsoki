# deploy/stateless/Dockerfile — stateless kitsoki service image.
#
# Packages cmd/kitsoki for the stateless-orchestrator deployment shape: all
# durable state lives in Postgres (sessions, satellites, trace stream, graph
# catalogs) and object storage (git bundle backups), so the container itself
# can run with a read-only root filesystem and be replaced at will.
#
# Two stages, mirroring test/e2e/Dockerfile (the repo's existing packaging
# convention):
#
#   builder  — golang:1.25 + Node + pnpm, running `make install`, the real
#              shipping build path (builds & embeds the runstatus SPA, stories
#              and skills, then compiles the binary). CGO_ENABLED=0 is safe:
#              kitsoki uses the pure-Go modernc SQLite driver and pure-Go pgx,
#              so the binary is fully static.
#
#   runtime  — debian:bookworm-slim carrying only what kitsoki shells out to:
#              git (gitserve/gitbackup and workspace ops), bash, and
#              ca-certificates (TLS to Postgres/object storage). Non-root.
#
# Read-only-rootfs contract (how to run this image):
#
#   docker run --read-only \
#     -v kitsoki-scratch:/scratch \            # or --tmpfs /scratch
#     -e KITSOKI_DB_BACKEND=postgres \
#     -e KITSOKI_PG_DSN=postgres://user:pass@host:5432/kitsoki?sslmode=disable \
#     -e DO_SPACES_KEY_ID=... -e DO_KITSOKI_TEST_API_KEY=... \  # or the env
#       # names you pass via --bucket-key-env/--bucket-secret-env
#     kitsoki-stateless web --addr 0.0.0.0:7777
#
#   /scratch is the ONLY writable path. The entrypoint creates:
#     /scratch/state — KITSOKI_STATE_DIR (see internal/statedir), the single
#                      runtime state root: session traces + sidecars, the
#                      default sessions.db, embedded-postgres data/cache and
#                      graph-mcp ledgers all re-root here. XDG_DATA_HOME points
#                      here too, as a belt for residual XDG-only writers.
#     /scratch/tmp   — TMPDIR for all temp files.
#     /scratch/cache — KITSOKI_CACHE_DIR (model/tool caches).
#
#   With KITSOKI_DB_BACKEND=postgres the SQLite path is unused for sessions;
#   it remains valid as the sqlite/embedded-postgres fallback so the image
#   also runs without Postgres for local smoke tests.
#
# Build context is the repo root:
#   DOCKER_BUILDKIT=1 docker build -f deploy/stateless/Dockerfile -t kitsoki-stateless .
# or `make stateless-image`.

# ---------------------------------------------------------------------------
FROM golang:1.25-bookworm AS builder

# Build deps: Node (for the SPA) + pnpm (via corepack) + git (module fetches,
# build revision stamping).
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git gnupg \
 && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && corepack enable && corepack prepare pnpm@11.3.0 --activate \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Warm the module cache first (better layer caching on source-only changes).
COPY go.mod go.sum ./
RUN go mod download

# Then the full source and build via the documented path. CGO_ENABLED=0 keeps
# the binary static (modernc sqlite + pgx are pure Go — verified with
# `CGO_ENABLED=0 go build ./cmd/kitsoki`).
COPY . .
RUN CGO_ENABLED=0 make install INSTALLDIR=/out \
 && /out/kitsoki version

# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

# Runtime deps kitsoki exec()s: git (gitserve/gitbackup/workspaces) + bash.
# ca-certificates for TLS to managed Postgres and object storage.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git bash \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 kitsoki \
 && useradd --uid 10001 --gid kitsoki --home-dir /scratch --shell /usr/sbin/nologin kitsoki \
 && mkdir -p /scratch /repos && chown kitsoki:kitsoki /scratch /repos

# /repos is pre-created (owned by kitsoki) so a named volume mounted there
# inherits writable ownership for `kitsoki repo serve --root /repos`.
COPY --from=builder /out/kitsoki /usr/local/bin/kitsoki
COPY deploy/stateless/entrypoint.sh /usr/local/bin/kitsoki-entrypoint
RUN chmod 0755 /usr/local/bin/kitsoki-entrypoint

USER kitsoki:kitsoki
WORKDIR /scratch

# All writable state funnels through /scratch (see contract above).
ENV KITSOKI_STATE_DIR=/scratch/state \
    XDG_DATA_HOME=/scratch/state \
    TMPDIR=/scratch/tmp \
    KITSOKI_CACHE_DIR=/scratch/cache \
    HOME=/scratch

VOLUME /scratch

# kitsoki web (7777) and kitsoki repo serve (9418) are the two intended
# commands; compose runs one container per role.
EXPOSE 7777 9418

ENTRYPOINT ["/usr/local/bin/kitsoki-entrypoint"]
CMD ["web", "--addr", "0.0.0.0:7777"]
