# Project object graph viewer (prototype)

One Vue app with two projections of the same seed catalog
(`../seed-objects.yaml`):

- **Catalog** — layered list/detail workbench: layer map, type filters,
  lifecycle badges, incoming/outgoing edge navigation, product-site
  projection, and the change-request preview.
- **Graph** — Vue Flow + ELK canvas (merged from the
  `.artifacts/graph-viewer-library-research` spike): focus-neighborhood
  radius (1/2/3/all edges), LR/RL/TB/BT layout, orthogonal edge routing with
  loop/backtrack lanes, animated edge motion, and collision-aware edge
  labels. The seed catalog is converted to the `kitsoki.graph/v1` wire shape
  (`src/graph/seed-graph.ts`, same shape as `internal/app/graph/wire.go`);
  the research mock graphs (story topologies, observed runs, traces, git)
  remain selectable as extra graph sources.

Selection is shared: picking an object in the catalog sidebar focuses it on
the canvas, and clicking a canvas node selects it in the catalog.

Run it:

```sh
pnpm install
pnpm dev
```

Deterministic rendering audits (headless Chrome via the shared
`playwright-core` install; start `pnpm dev` first):

```sh
pnpm audit:labels   # every rendered edge label legible & unclipped
pnpm audit:motion   # edges track nodes during drag (no detached lines)
pnpm audit:routes   # forward/loop/backtrack lanes route as declared
```

The viewer is itself described in the seed catalog (`feature-graph-viewer`
and its requirements/use-cases/implementation/evidence nodes) — the dogfood
meta cycle. This prototype informs goal G5
(`docs/goals/project-object-graph/GOAL.md`): the production destination is a
graph surface in `kitsoki web` (tools/runstatus) fed by the Go loader, not
this standalone app.
