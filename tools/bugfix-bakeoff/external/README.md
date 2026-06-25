# External-project bake-off — "should I use kitsoki for MY project?"

The parent [`tools/bugfix-bakeoff`](../README.md) compares kitsoki's `bugfix`
pipeline against a naive single-prompt agent on **kitsoki's own** bugs. This
`external/` subtree generalises that to the question a prospective user actually
asks: *if I onboard my repo and let kitsoki fix a bug, do I get a good fix — and
at what cost compared to the real one?*

It runs the same study against a real, third-party OSS repo:
**[`sindresorhus/query-string`](https://github.com/sindresorhus/query-string)** —
small/simple (one ~558-LOC parser, `base.js`) yet **mature** (274 commits, 90
releases, since 2013), with a deep filed-issue → fixing-PR → regression-test
history. That makes it an honest stand-in for a customer repo.

## The pieces

| File | What |
|---|---|
| `query-string/manifest.yaml` | the 3 pinned bugs (issue, PR, `fix_sha`, `baseline_sha`, oracle, the ticket the pipeline is fed) |
| `query-string/oracles/qs{1,2,3}.test.js` | the regression test the real PR shipped, **isolated** — the hidden good/bad oracle |
| `score_qs.sh` | the **deterministic** good/bad detector — overlays the hidden oracle on a candidate tree, runs `ava`, exits 0 iff GREEN |
| `query_string_bakeoff_test.go` | the gated reproducible test (`make qs-bakeoff`): clone → onboard → arm-oracle RED → real-fix GREEN |

## The 3 bugs

| id | issue / PR | bug | `baseline_sha` (= `fix^`) | `fix_sha` |
|----|------------|-----|---------------------------|-----------|
| qs1 | [#336](https://github.com/sindresorhus/query-string/issues/336) | encoded separator `%7C` splits a single value into an array | `2e1f45a` | `ec67fea` |
| qs2 | [#404](https://github.com/sindresorhus/query-string/issues/404) / [#406](https://github.com/sindresorhus/query-string/pull/406) | `types` schema ignored with `arrayFormat: comma` + single item | `88e1e36` | `3e61882` |
| qs3 | [#392](https://github.com/sindresorhus/query-string/pull/392) | `bracket-separator` mis-parses a single URL-encoded value | `4287e77` | `19c43d4` |

Each baseline is the fix commit's first parent, so the checkout is
byte-reproducible and the bug is present. The oracle is **kept out of the
candidate's tree until scoring**.

## The deterministic good/bad detector

`score_qs.sh --bug qs1 --tree <candidate-worktree>`:

1. copies the candidate tree (never mutates it), links a prebuilt `node_modules`
   (`QS_NODE_MODULES=…` to skip a re-install);
2. appends the **hidden oracle** (`oracles/qs1.test.js`) to `test/parse.js`;
3. runs `npx ava --match "<title>"` → **GREEN = good fix, RED = bug remains**
   (the script's exit code is the verdict);
4. also runs the full `ava` suite as an *informational* regression signal.

> **Oracle is primary, suite is secondary — by design.** For qs1/qs2 a *correct*
> behavioral fix legitimately flips one PRE-EXISTING test's expectation (the real
> PR edited it too). So a source-only correct fix scores `partial` (oracle GREEN,
> suite RED) until the candidate also updates that test. That gap is exactly the
> quality signal the bake-off surfaces: kitsoki's pipeline runs the full suite
> and updates the affected test (→ `solved`); a careless single-prompt may not
> (→ `partial`). Verified: qs1/qs2 source-only fix → `partial`, qs3 → `solved`.

## Run the gated scaffold check (deterministic, free)

```sh
make qs-bakeoff      # ~32s: clone + onboard + arm all 3 oracles (RED->GREEN)
```

Excluded from `make test` by the `qsbakeoff` build tag. It needs network, git,
node/npm, and an installed `kitsoki`. It proves the fixtures are armed and the
scorer is honest **before** any LLM is spent.

## Run a cost-bearing cell (operator-only, real LLM)

Same model as the parent bake-off — drive `stories/bugfix` (kitsoki treatment)
or a single multi-stage prompt (control) under a candidate model, from the bug's
`baseline_sha` worktree, then score the resulting tree:

```sh
# after the agent leaves its fix in <worktree>:
QS_NODE_MODULES=/path/to/query-string/node_modules \
  tools/bugfix-bakeoff/external/score_qs.sh \
    --bug qs1 --tree <worktree> --candidate opus-4.8 --treatment kitsoki \
    --out results/cells/qs1-opus-4.8-kitsoki.json
```

Cells use the shared [`results/SCHEMA.md`](../results/SCHEMA.md), so
`aggregate.py` + the report/deck tooling consume them unchanged. The cost is read
from the kitsoki trace (`payload.meta.cost_usd`) — the **exact** price of the
fix kitsoki would propose, lined up against the real maintainer fix.
