# Graph Storage — the CatalogStore Seam, YAML Interchange, and Postgres Catalogs

The graph engine (registry, typed edges, lint, neighbors, diff, changesets —
see [`decomposition-graph.md`](decomposition-graph.md)) is pure in-memory and
storage-agnostic. This document describes the storage seam underneath it: the
`CatalogStore` interface, the default YAML file store, the Postgres catalog
backend, and the permanent role YAML keeps as the interchange/review format.

The YAML file catalog remains the default. Postgres catalogs are opt-in via
explicit `pg:` references and a Postgres db backend
([`storage-backends.md`](storage-backends.md)).

## The CatalogStore seam

`internal/graph/store.go` defines the seam extracted behind the engine's two
historical choke points (`LoadCatalog` for reads, `commitScratchOperations`
for writes):

```go
type CatalogStore interface {
    Load(ctx)  (*Catalog, CatalogRev, error)
    Commit(ctx, base CatalogRev, ops []Operation, opts CommitOptions) (CommitResult, error)
    Ref() string
}
```

- **`CatalogRev`** is an opaque revision token: the exact catalog state a
  `Load` observed, used as the optimistic-concurrency base for `Commit`. For
  the file store it is the catalog's content digest; for Postgres it is the
  catalog's revision counter. Equality means "the catalog has not changed
  since that Load".
- **`Commit`** persists already-validated operations against that base, and
  fails with a CAS-conflict error (`IsCASConflict`) when the catalog moved
  underneath the caller. Rejections are results, not errors: `CommitResult`
  carries `RejectReasons` (operations could not apply) or `LintIssues` (the
  candidate introduced NEW error-severity lint) — in both cases nothing was
  committed. `CommitOptions.DryRun` builds and lint-gates without persisting.
- Everything above the seam — changeset parsing/validation, guard fills,
  auto-authorize policy, the lint-delta gate's policy — stays in the engine
  and is shared by every backend. Store-routed verbs (`ProposeVia`,
  `AuthorizeVia`, `WithdrawVia`, `RebaseVia`, `ApplyVia` in
  `internal/graph/storeverbs.go`) reproduce the exact file-verb semantics,
  including bounded CAS retry, over any `CatalogStore`.

A storage-neutral `CatalogData` layer (`internal/graph/catalogdata.go`) with
`ApplyOperationsToData` mirrors the file path's `applyOperations` semantics —
all seven operation kinds, including the rename reference-walk — so backends
share one operation-application implementation.

## FileCatalogStore — YAML stays first-class

`FileCatalogStore` (same file) is a pure extraction of today's behavior: the
YAML catalog on disk, the scratch/canonicalize/CAS commit pipeline, byte-for-
byte unchanged, and still the default. Small or local catalogs may stay
YAML-native indefinitely — the seam makes the file store a peer backend, not
a legacy shim.

Beyond being a live store, **YAML is the permanent interchange and review
format** (plan decision update, 2026-07-26):

```
kitsoki graph export --catalog-id <id> [--out <path.yaml>]
kitsoki graph import --catalog <path.yaml> --catalog-id <id> [--force]
```

- **Export** emits a deterministic single-file YAML snapshot of a stored
  catalog. Determinism is a tested contract: re-exporting an unchanged
  catalog is byte-identical, which is what makes exports usable for review,
  S3 backup, and PR-style diffing.
- **Import** loads a YAML catalog (single file or bundle dir) into Postgres.
  It is rev-guarded: replacing a stored catalog that has commits beyond the
  initial import (rev > 1) requires `--force`.
- **Round-trip is the test**: import → export must reproduce the catalog
  losslessly. `storage: top_level` edges normalize into edge rows on import
  and rehydrate losslessly on export.

Both commands require a Postgres db backend; under the default `sqlite`
backend they fail with a clear error (graph catalogs have no SQLite backend —
the YAML file catalog is the local surface, `openGraphCatalogDB` in
`cmd/kitsoki/db_backend.go`).

## pgcatalog — the Postgres backend

`internal/graph/pgcatalog` implements `CatalogStore` on schema `graph`
(idempotent DDL, multiple catalogs per database keyed by `catalog_id`):

| Table | Contents |
|---|---|
| `graph.graph_catalogs` | one row per catalog: `catalog_id`, `schema_pin`, `rev` (optimistic-concurrency counter), lint hints / write policy / feedback routing as JSONB |
| `graph.graph_types` | type registry declarations, one JSONB `decl` per type |
| `graph.graph_nodes` | `id`, `type_id`, `schema_pin`, `title`, `status`, `visibility`, `sources`, `fields` JSONB |
| `graph.graph_edges` | `(src, field, dst, ord)` — **all** typed edges normalized here, regardless of the declared `storage` |
| `graph.graph_audit` | append-only history: `rev`, `changeset_id`, `actor`, op `summary` JSONB, `created_at` |

`Commit` is one transaction: lock the catalog row's `rev` with
`SELECT ... FOR UPDATE`, check the base revision (CAS), apply the operations,
run the **same** registry validation and lint-delta gate as the YAML loader,
append the `graph_audit` row, and bump `rev`. Node/edge/type/lint parity is
verified against the real `pog/catalog.yaml` in the package tests.

Each `Load` is a fresh full read into the in-memory engine — identical to the
per-call reload contract the file store and every MCP/host read already have
(a full catalog scan is trivially cheap at current sizes; recursive CTEs are
the incremental escape hatch if a query ever outgrows memory-load).

### History

The file catalog's git-era `git log --follow` timeline does not exist for
rows. Instead `graph.history` merges changeset-era entries with the
`graph_audit` table (`AuditEntries` in pgcatalog), written in the same
transaction as every commit — so history is not best-effort, it is exactly
the commits that happened.

## `pg:` references in MCP and host tools

Postgres catalogs are addressed by an explicit reference spelling that no
filesystem path can accidentally look like:

```
pg:<catalog-id>
```

(`pgcatalog.RefPrefix` / `pgcatalog.IsRef`). Every binding surface that takes
a catalog path — host `graph` handlers, MCP graph tools, catalog aliases —
routes `pg:` refs through the seam with unchanged tool schemas. `graph.open`
reports `{rev}` for a pg catalog without any git subprocess.

The `*sql.DB` behind `pg:` refs is injected by the session-store wiring:
opening a Postgres session store calls `host.SetGraphPGDB(s.DB())`
(`cmd/kitsoki/db_backend.go`), so graph refs resolve against the configured
backend; `KITSOKI_PG_DSN` remains the fallback when no store has been opened.

## What canonicalize/CAS still exist for

The comment-preserving `yaml.Node` rewriting, block-scalar canonicalization,
and content-digest CAS scratch-tree pipeline exist **only** because the YAML
file is a live, hand-edited store. They remain fully in service for
`FileCatalogStore`, and `canonicalize` cleanly rejects `pg:` refs — it is a
YAML-file concern with no Postgres meaning.

Retiring that machinery is a *candidate* at the eventual cutover, **not a
committed decision**: the seam keeps `FileCatalogStore` as a peer backend, so
the YAML-as-live-store machinery lives exactly as long as YAML-native
catalogs do. The loader and the deterministic emitter are permanent either
way — they are the import/export interchange path.

## See also

- [`storage-backends.md`](storage-backends.md) — backend selection and the
  shared Postgres handle graph catalogs ride on.
- [`decomposition-graph.md`](decomposition-graph.md) — the graph model this
  storage serves.
- [`cross-repo-governance.md`](cross-repo-governance.md) — federation, which
  `pg:` catalog handles slot into.
