# Stateless Container Deployment

How to run kitsoki as a read-only, replaceable container: durable state in
Postgres and S3-compatible object storage, git repos on a small stateful repo
service, and every remaining mutable path confined to one writable mount.
This is the operator-facing companion to
[`../architecture/storage-backends.md`](../architecture/storage-backends.md)
and [`../architecture/graph-storage.md`](../architecture/graph-storage.md).

Nothing here changes local behavior: SQLite and file-based traces remain the
default. The stateless shape is opt-in, engaged by environment.

## Target architecture

| Role | What it is | State |
|---|---|---|
| Orchestrator (`kitsoki web`) | The kitsoki service, read-only rootfs, replaceable at will | None durable in the container. Sessions, satellites, trace stream, graph catalogs in Postgres (`KITSOKI_DB_BACKEND=postgres`, `KITSOKI_PG_DSN`) |
| Postgres | One database, schema-per-subsystem | The durable relational + stream + graph store |
| Object store | S3-compatible (`internal/objectstore`: SigV4, virtual-hosted-style, Presign) | Git bundle backup chains; blob offload target |
| Repo service (`kitsoki repo serve`) | Git smart HTTP over a volume of bare repos | **Stateful by design** — a volume plus commit-by-commit S3 backup, not an HA cluster |

## KITSOKI_STATE_DIR — the writable-root contract

`internal/statedir` resolves a single runtime state root for deployments that
must confine every mutable default location to one mount. Set the
`KITSOKI_STATE_DIR` environment variable, or the persistent `--state-dir`
flag (which exports the env var; flag wins, matching `--kitsoki-repo`
precedence). When set, the default runtime-writable locations re-root:

| Path under `<state>` | Contents |
|---|---|
| `sessions/` | per-session JSONL traces + co-located sidecars (transcripts, frames, annotations) |
| `sessions.db` | default SQLite sessions database |
| `pg/` | embedded-postgres data dir (derived from `dir(sessions.db)/pg`) |
| `cache/embedded-pg/` | embedded-postgres binary download cache |
| `graph-mcp/` | graph-mcp feedback + receipts ledgers |

When unset, nothing changes — callers keep their historical defaults
byte-for-byte (`~/.kitsoki/sessions`, `$XDG_DATA_HOME/kitsoki/sessions.db`,
`<repo>/.artifacts/graph-mcp`, `os.UserCacheDir()/kitsoki/embedded-pg`). Only
the root moves; key schemes and path tails are unchanged.

### Honest residue: what still escapes the state dir

`KITSOKI_STATE_DIR` covers the serve-path defaults, not every writer in the
binary. Known escapes (point the surrounding env at the writable mount, or
accept them as non-serve paths):

- The `~/.kitsoki/repo` pointer write (`internal/kitrepo`, runs during
  invocation preparation). `HOME` must be writable.
- An explicit graphsrv `--journal` path deliberately wins over the state dir.
- XDG-cache writers honor `XDG_CACHE_HOME`, not the state dir: base
  skills/stories materialization, the `internal/kitgit` clone cache
  (`${XDG_CACHE_HOME:-~/.cache}/kitsoki/kits/git`).
- `os.UserCacheDir()` writers: local-model fetch cache
  (`internal/agent/server/fetch.go`; pre-warm via `make fetch-models`, honors
  `KITSOKI_CACHE_DIR`), agent-root synthesis (`internal/agentroot`).
- Home-anchored agent-launch and onboarding surfaces (pending-seed,
  onboarding, profile setup, agent backends, ghagent credstore, tmux/IDE
  integrations) — agent-launch paths, not the web/daemon serve path.
- Workspace clones/capsules under the repo's `.capsules/`, and read-only
  inputs (`~/.kitsoki/secrets.yaml`, `~/.claude` transcripts for mining) —
  out of scope by design.

The shipped image handles this by making `HOME`, TMP, and caches all point
into the one writable mount (next section).

## The image and compose file

Files under [`../../deploy/stateless/`](../../deploy/stateless/README.md):

- **`Dockerfile`** — multi-stage: a `golang:1.25-bookworm` + Node/pnpm
  builder running `CGO_ENABLED=0 make install` (the real shipping path —
  builds and embeds the runstatus SPA, stories, and skills; the binary is
  fully static: pure-Go modernc SQLite and pgx), then a
  `debian:bookworm-slim` runtime with only `git`, `bash`, and
  `ca-certificates`, non-root (uid 10001). Build with `make stateless-image`.
- **`entrypoint.sh`** — creates `/scratch/{state,tmp,cache}` (volumes start
  empty) and `exec kitsoki "$@"`.
- **`docker-compose.yaml`** — a runnable local approximation: `kitsoki-web`
  and `kitsoki-repo` (both `read_only: true`), `postgres:16` (healthchecked),
  and minio as the S3 stand-in. `internal/objectstore` speaks
  virtual-hosted-style S3 and `ParseBucketURL` keeps only the hostname, so
  minio listens on port 80 with `MINIO_DOMAIN` and a network alias for the
  bucket host — read the file's header comment before changing the wiring.

`/scratch` is the only writable path. The image pins
`KITSOKI_STATE_DIR=/scratch/state` (the state-dir contract above),
`XDG_DATA_HOME=/scratch/state`, `TMPDIR=/scratch/tmp`,
`KITSOKI_CACHE_DIR=/scratch/cache`, `HOME=/scratch`, which routes the
sessions.db default, session traces/sidecars, graph-mcp ledgers, temp files,
model caches, and home-anchored writes into the mount. Minimal run:

```
docker run --read-only \
  -v kitsoki-scratch:/scratch \
  -e KITSOKI_DB_BACKEND=postgres \
  -e KITSOKI_PG_DSN=postgres://user:pass@host:5432/kitsoki?sslmode=disable \
  kitsoki-stateless web --addr 0.0.0.0:7777
```

Without the Postgres env the image still runs on its sqlite default under
`/scratch` — useful for smoke tests, not the stateless shape.

## Repo service runbook

`kitsoki repo` wraps `internal/gitserve` (smart-HTTP `git http-backend` CGI
with strict repo-path validation, symlink containment, chunked-request
de-chunking, an injectable auth seam, and a read-only mode that rejects
receive-pack) and `internal/gitbackup` (incremental bundle chains on the
object store).

All object-store flags follow the repo convention: `--bucket-url` plus
`--bucket-key-env` (default `DO_SPACES_KEY_ID`) and `--bucket-secret-env`
(default `DO_KITSOKI_TEST_API_KEY`) naming the env vars that hold credentials.

```
# Create a bare repo under the serving root
kitsoki repo init --root /repos --name team/project

# Serve; back up every pushed repo to the object store
kitsoki repo serve --root /repos --addr 0.0.0.0:9418 --allow-unauthenticated \
  --backup --backup-prefix repos/ \
  --bucket-url https://<bucket>.<region>.digitaloceanspaces.com

# One-shot backup / forced full compaction point
kitsoki repo backup --repo /repos/team/project.git --prefix repos/team/project.git [--full] --bucket-url ...

# Restore a repo from its bundle chain
kitsoki repo restore --prefix repos/team/project.git --target /repos/team/project.git --bucket-url ...
```

Safety defaults: `serve` binds `127.0.0.1:9418`; a non-loopback `--addr` is
refused without an explicit `--allow-unauthenticated` (in compose the flag is
required inside the container while the host port stays loopback-bound).
`--read-only` serves fetches/clones only.

Backup semantics (`git-backup-manifest/v1`, `internal/gitbackup`): each repo
gets a bundle chain under `<prefix>/bundles/` with a JSON manifest written
after the data and generation-guarded for the single-writer contract. After
each push the serialized, burst-coalescing queue emits the smallest
expressible link — incremental bundle, a bundle-less refs entry for
deletions/rewinds, or a full-backup fallback when a pruned basis makes the
increment inexpressible. `--full` is a compaction point. Restore replays the
newest full bundle plus increments with size/sha256 chain verification, exact
tip-ref pinning, and `git fsck`, building in a staging sibling and renaming
into place only on success — a failed restore never poisons the target.

### Restore drill

Backups you have not restored are hopes, not backups. Periodically:

1. `kitsoki repo restore --prefix repos/<name>.git --target <scratch>/restore-drill.git --bucket-url ...`
   (the target must not be an existing non-empty repo).
2. Verify tips match the live repo: compare `git -C <live> for-each-ref`
   against the restored repo (restore pins exact manifest tip refs; `git
   fsck` already ran as part of restore).
3. Delete the drill target.

## What is explicitly NOT done

This deployment shape is real but partial. Do not present it otherwise:

- **SQLite remains the default backend.** The cutover (and any retirement of
  the sqlite driver or of YAML-as-live-store machinery) is an explicit future
  decision, not implied by any of this.
- **The runstatus SPA still polls.** The cursor RPC and cursor-driven SSE
  exist server-side for stream backends; the frontend and the
  `runstatus.jobs.list` federation surface have not moved onto them.
- **Sidecars and evidence are not on S3.** Transcripts, frames, annotations,
  and `.capsules/ci` evidence stay file-shaped; only git bundle backups use
  the object store today.
- **The git service is stateful by design** — a volume plus tested
  restore-from-S3, not HA.
- **Durable objective receipts are not in a default production path** (opt-in
  constructor only; see
  [`../architecture/storage-backends.md`](../architecture/storage-backends.md)).
- **`KITSOKI_STATE_DIR` has residue** (list above); the image compensates
  with `HOME`/XDG/TMP env, not by covering every writer.
