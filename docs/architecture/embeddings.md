# Embeddings

Kitsoki's vector substrate lives in `internal/embed/`. It is consumed by two
features: the `host.oracle.search` host verb (semantic retrieval over files)
and the embedding routing tier (paraphrase recall between the lexical tiers and
the LLM). Both consumers call the substrate directly; neither goes through the
generative oracle registry.

## The `internal/embed` package

### Embedder interface and Role

```go
type Role int
const ( Document Role = iota; Query )

type Embedder interface {
    Embed(ctx context.Context, texts []string, role Role) ([][]float32, error)
}
```

`Role` exists so callers declare intent and the `Embedder` applies the right
model-specific task prefix — callers never write prefixes themselves.

**Prefix discipline.** nomic-embed-text-v1.5 uses asymmetric prefixes:
documents get `search_document: `, queries get `search_query: `.
bge-small-en-v1.5 prefixes only the query side (`search_query: `).
`LocalEmbedder` (`internal/oracle/local_llm_embed.go`) applies these from the
`modelPrefixes` map keyed by model id and role. An unknown model gets no prefix
(safe default). Callers pass `Document` at index time and `Query` at lookup
time; they never write a prefix.

`FakeEmbedder` (test seam) generates deterministic, L2-normalized float32
vectors from FNV-1a hashes. It strips any `"key: "` prefix before hashing so
`"search_document: apple"` and `"apple"` produce the same test vector. No
network, no external dependencies.

### Index

```go
type Entry struct { ID string; Meta map[string]any; Vec []float32 }
type Hit   struct { ID string; Meta map[string]any; Score float32 }

func NewIndex(entries []Entry) *Index
func (idx *Index) Rank(query []float32, k int) []Hit
```

`Entry.Vec` must be L2-normalized before insertion. `/v1/embeddings` from
llama-server with `--pooling mean` returns pre-normalized vectors, so no
client-side normalization is needed. Cosine similarity reduces to a plain dot
product over normalized vectors; `Rank` implements a partial selection sort
(`O(n·k)`), which is faster than a full sort for typical retrieval sizes
(`k << n`). `Index` is read-only after construction; concurrent `Rank` calls
are safe.

### Store

```go
type StoreKey struct { Model, Dim, Pooling, CorpusHash string }

func NewStore(dir string) *Store
func (s *Store) Load(key StoreKey) ([]Entry, bool, error)
func (s *Store) Save(key StoreKey, entries []Entry) error
```

Corpora are gob-encoded and written atomically (temp file + rename) keyed by
`(model, dim, pooling, SHA-256 of the corpus content)`. Any change to the
model, truncation, or input text forces a cache miss and a fresh embed. Both
consumers key through the same `Store` with different corpus hashes.

### Ingestion (oracle.search)

`Ingest` (`internal/embed/ingest.go`) resolves corpus globs under
`workingDir`, reads each file (skipping binary files and files > 1 MiB), and
splits them into chunks:

- **Markdown** (`.md`/`.markdown`) — split on heading lines (`# … ######`);
  oversized sections are sub-split with window overlap.
- **Everything else** — fixed-size byte windows with configurable overlap.

Each chunk becomes an `Entry` with `ID = "<relpath>#<n>"` and
`Meta = {path, ordinal, text}`. The corpus hash is SHA-256 over all file
bytes in sorted path order.

## The embedding sidecar

`LocalEmbedder` is constructed over a `Sidecar` started with
`WithExtraArgs("--embeddings", "--pooling", "mean")` on a separate port with a
separate embedding GGUF (nomic-embed-text-v1.5 or bge-small-en-v1.5). The
`--embeddings` flag restricts a llama-server instance to embedding use, so
this is a second server process — not a flag on the chat sidecar.

In **endpoint mode** (`endpoint:` set in config) the embedder attaches to an
already-running server and never fetches or spawns. In **managed mode**
(`model:` set) the sidecar is fetched, verified (sha256), and spawned lazily
on the first call, reusing the same `EnsureRunning` / `/health` gate /
SIGTERM teardown from `internal/oracle/server/sidecar.go`. The model pin for
the embedding GGUF must be filled in `fetch.go` — it is currently a
placeholder; endpoint mode works today on any host.

## `host.oracle.search`

A room calls:

```yaml
- invoke: host.oracle.search
  with:
    query:     "{{ world.user_question }}"
    corpus:    "docs/runbooks/**/*.md"   # glob, relative to working_dir
    top_k:     5
    min_score: 0.4                       # optional cosine floor
  bind:
    hits: hits
    top:  top
```

`hits` binds to a ranked `[]{ path, chunk_id, text, score }` list (descending
by `score`). `top` is the first hit, or nil when the result is empty. An empty
glob or all hits below `min_score` binds an empty list — not an error.

The handler (`internal/host/oracle_search.go`):
1. Ingests the corpus via `embed.Ingest`.
2. Checks `embed.Store` for a cached index keyed by `(model, dim, pooling,
   corpus hash)`.
3. On a miss: embeds all chunks as `Document`, saves to the store.
4. Embeds the query as `Query`, calls `Index.Rank(topK)`, filters by
   `min_score`, and binds the results to world.

The verb is **read-only**: it reads files under `working_dir` and writes
nothing. Paths escaping `working_dir` are silently skipped.

`chunk:` overrides the defaults (`max`, `overlap`, `mode: heading|window`);
changing any field changes the corpus hash and forces a re-embed.

## Embedding routing tier

The routing tier (`internal/orchestrator/embed_tier.go`) sits between the
lexical template tier and the LLM inside `TrySemantic`. It is **off by
default** and enabled per app:

```yaml
app:
  routing:
    embedding:
      enabled: true
```

**`EmbedTierConfig` fields:**

| Field           | Default                  | Meaning |
|-----------------|--------------------------|---------|
| `Enabled`       | `false`                  | Opt-in gate |
| `Model`         | `nomic-embed-text-v1.5`  | Embedding model |
| `Dim`           | `256`                    | Matryoshka truncation for nomic |
| `Endpoint`      | `""`                     | Attach to running server instead of spawning |
| `ConfidentBar`  | `0.82`                   | top-1 cosine ≥ this → confident match |
| `Margin`        | `0.08`                   | top1 − top2 must exceed this; else tie |

The tier embeds allowed intent names as `Document` at startup (lazy, cached by
intent name), embeds the utterance as `Query` at turn time, ranks by dot
product, and applies the `ConfidentBar`/`Margin` gate:

- top1 ≥ bar **and** margin sufficient → `Verdict{ConfidenceEmbedding}` — routes without LLM.
- top1 ≥ bar **but** margin too narrow → tie → `AMBIGUOUS_INTENT` disambiguation.
- otherwise → zero verdict → falls through to LLM.

`semroute.ConfidenceEmbedding` is the new confidence band on the existing
five-band scale (below `ConfidenceTemplateFull` 0.80, above the LLM).

**Calibration note.** `ConfidentBar: 0.82` and `Margin: 0.08` are placeholder
values. The Oregon Trail bake-off (`stories/oregon-trail/`) with
`KITSOKI_EMBED_E2E=1` will tune them and measure the LLM-fallthrough delta
relative to the current 37.5% rate.

## Determinism

Both consumers pin a `(model, dim, pooling, corpus hash)` tuple in the store
key and in breadcrumbs. Pinning the llama.cpp release in `fetch.go` (as the
chat sidecar already does) keeps replay stable across upgrades. A reviewer
given the same corpus, model, and llama.cpp pin reproduces the ranking exactly.

## See also

- `internal/embed/` — substrate package (godoc covers all exported types)
- `internal/oracle/local_llm_embed.go` — `LocalEmbedder` + prefix table
- `internal/orchestrator/embed_tier.go` — `EmbedTier`, `EmbedTierConfig`
- `internal/host/oracle_search.go` — the host verb handler
- [`semantic-routing.md`](semantic-routing.md) §"Embedding routing tier" — the routing tier in context
- [`oracle-plugin.md`](oracle-plugin.md) §9 — embedding sidecar mode
