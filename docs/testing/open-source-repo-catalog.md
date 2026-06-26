# Open-source repo test catalog

This is the landing page for reusable open-source projects we use to test
Kitsoki's developer workflows against real codebases. It tracks what exists,
what is only a candidate, which suites and harnesses are available, and which
results are already durable enough to cite.

The first implemented lane is the external bug-fix bake-off:
[`tools/bugfix-bakeoff/external/`](../../tools/bugfix-bakeoff/external/). That
harness onboards a repo, prepares parent-commit bug baselines, hides the real
PR's regression test as an oracle, drives a Kitsoki workflow, then grades the
candidate tree deterministically with no LLM.

## Status legend

| Status | Meaning |
|---|---|
| Candidate | Good target, but fixtures have not been mined or verified. |
| Selected | Chosen for fixture mining; no committed manifest yet. |
| Manifested | `tools/bugfix-bakeoff/external/projects/<id>/manifest.yaml` exists. |
| Armed | Every committed oracle proves RED at baseline and GREEN at the real fix. |
| Driven | At least one live model/workflow cell has been run. |
| Reported | Results have a durable case study, report, or deck. |

## Constructor Fabric inventory

These projects are still important test targets, but they are Constructor Fabric
or local-self references rather than reusable public OSS claims. Track them here
so we can use them for workflow coverage without mixing them into the public
candidate list.

| Project | Repo type | Suites / harnesses | Implemented | Results |
|---|---|---|---|---|
| `studio` | Constructor Fabric product surface | Studio MCP, web/TUI flows, story QA, no-LLM flow fixtures, Playwright where UI evidence is required | Candidate tracking row; no external-project manifest yet | Use for dev-story and Studio-MCP workflow coverage. Needs a concrete fixture catalog before it can report reusable results. |
| `slidey` | Constructor Fabric OSS-adjacent tool repo | Node/Vue test suite, VS Code extension tests, Slidey conversion/fidelity harnesses, MCP integration checks | Candidate tracking row; not yet represented as an external bake-off project | Useful for frontend/tooling workflows and MCP setup issues. Needs selected bug fixtures and deterministic oracles. |
| `gears-rust` | Local/private Rust monorepo reference | Per-bug Cargo oracles; `GEARS_RUST_REPO=... make gears-bakeoff`; `suite: false` because the full workspace is heavy | Manifested with 4 armable bugs and additional reference-only cases | Armable fixtures are RED->GREEN. Reference corpus records pass/fail/stall outcomes from prior dogfood runs. Not an OSS reuse candidate. |
| `kitsoki` | This repo, local self-benchmark | Go/TypeScript project manifest folded into the external harness; local-only mirror grading | Manifested with 3 armed fixtures | Self-benchmark support exists. Use for regression of the harness itself, not for customer-facing OSS claims. |

## Top 10 reusable OSS candidates

These are the default public open-source targets to mine next. Constructor Fabric
projects are tracked separately above. A candidate graduates only after we can
name real filed bugs, the fixing commits, isolated regression tests, the baseline
commit, and a deterministic command that proves RED at baseline and GREEN at the
real fix.

| Rank | Project | Stack / suite | Why it is a good candidate | Status | Implemented / results | Next action |
|---:|---|---|---|---|---|---|
| 1 | `sindresorhus/query-string` | JavaScript, AVA oracle plus full `npx ava` secondary suite | Small parser, mature history, fast install/test, already proves the full workflow. | Reported | Manifested and armed with 3 bugs: `qs1`, `qs2`, `qs3`. GPT-5.5 / `codex-native` solved 3/3; GLM-5.2 pending because the provider was rate-limited. See [`docs/case-studies/query-string-bakeoff.md`](../case-studies/query-string-bakeoff.md). | Keep as the reference project and expand model cells when providers are available. |
| 2 | `pillarjs/path-to-regexp` | JavaScript/TypeScript, Jest or project test runner | Compact routing/parser library with crisp behavioral bugs and small diffs. | Candidate | Not implemented. | Mine 3 PRs that added parser/matcher regression tests. |
| 3 | `ljharb/qs` | JavaScript, npm test suite | Query parsing domain similar to `query-string`, but different implementation and broader compatibility surface. | Candidate | Not implemented. | Find encoding, nesting, or array-format bugs with added tests. |
| 4 | `sindresorhus/globby` | JavaScript, AVA | File-pattern behavior creates easy-to-state bugs and fast focused tests. | Candidate | Not implemented. | Mine ignore, cwd, gitignore, and Windows-path fixes. |
| 5 | `mrmlnc/fast-glob` | TypeScript, npm test suite | Mature glob engine with many edge cases; good stressor for path/platform semantics. | Candidate | Not implemented. | Identify small regression-test PRs that do not require slow fixture setup. |
| 6 | `yargs/yargs-parser` | JavaScript, npm test suite | CLI parsing bugs are compact, user-visible, and easy to express as tickets. | Candidate | Not implemented. | Mine coercion, aliases, arrays, config, and default-value fixes. |
| 7 | `eemeli/yaml` | JavaScript/TypeScript, project test suite | Parser/emitter edge cases produce deterministic oracle tests and non-trivial root-cause fixes. | Candidate | Not implemented. | Select fixtures that avoid large snapshot churn. |
| 8 | `pallets/click` | Python, pytest | CLI framework with mature tests, many filed issues, and concise repros. | Candidate | Not implemented. | Prove a Python project manifest: install, focused pytest oracle, full-suite secondary signal. |
| 9 | `python-dateutil/python-dateutil` | Python, pytest | Date/time parser bugs exercise semantic reasoning beyond string manipulation. | Candidate | Not implemented. | Mine parser/timezone regressions with narrow tests and stable baselines. |
| 10 | `BurntSushi/ripgrep` | Rust, Cargo tests | Mature Rust CLI/library with real-world parser/search behavior; useful beyond JS/Python. | Candidate | Not implemented. | Start with small library-level fixes; avoid fixtures needing platform-specific shell setup. |

## Harness catalog

| Harness / command | Cost | What it proves | Current coverage |
|---|---:|---|---|
| `python3 tools/bugfix-bakeoff/external/bench.py verify --project <id>` | Free | Project fixtures are armed: oracle RED at baseline and GREEN at the real fix. | `query-string`, plus Constructor Fabric/local references `gears-rust` and `kitsoki`. |
| `make qs-bakeoff` | Free, gated | Query-string clone, dev-story onboarding, and fixture arming all work end to end. | Public reference path. |
| `GEARS_RUST_REPO=... make gears-bakeoff` | Free, gated, local heavyweight | Rust oracle path works against a local mirror without dirtying the checkout. | Private/local reference only. |
| `tools/bugfix-bakeoff/external/drive_cell.sh --project <id> --bug <bug> --candidate <candidate> --score` | Cost-bearing | A live model drives the Kitsoki workflow, then the hidden oracle grades the resulting tree. | Used for `query-string` GPT-5.5 cells. |
| `tools/bugfix-bakeoff/external/escalate.sh --project <id> --ladder <name>` | Cost-bearing | Finds the cheapest candidate rung that solves each bug. | Implemented; needs more public projects/results. |
| `python3 tools/bugfix-bakeoff/external/bench.py summarize --project <id> ...` | Free | Regenerates deterministic report/deck artifacts from committed results. | Available for external bake-off results. |

## Results catalog

| Result set | Location | Durable claim | Gaps |
|---|---|---|---|
| Query-string GPT-5.5 run | [`tools/bugfix-bakeoff/external/results/`](../../tools/bugfix-bakeoff/external/results/) and [`docs/case-studies/query-string-bakeoff.md`](../case-studies/query-string-bakeoff.md) | GPT-5.5 solved 3/3 query-string bugs through the Kitsoki bugfix pipeline; each fix passed the hidden oracle and full AVA suite. | GLM-5.2 was provider-pending; add more model cells and a single-prompt control if we need a full matrix. |
| Query-string scaffold | `make qs-bakeoff` | Public OSS onboarding and oracle arming are deterministic and free. | Keep the passing date fresh when behavior changes. |
| Gears-rust reference | [`tools/bugfix-bakeoff/external/projects/gears-rust/manifest.yaml`](../../tools/bugfix-bakeoff/external/projects/gears-rust/manifest.yaml) | Heavy Rust oracle shape is represented: per-bug Cargo commands, standalone oracles, suite disabled. | Private/local, so it should not be used as the public OSS proof. Reference-only fixtures need more injection modes before arming. |

## Candidate graduation checklist

1. Pick a repo from the top-10 list or add a new row with a concrete reason.
2. Mine at least 3 real bugs with linked issues or self-contained fixing PRs.
3. For each bug, record `fix_sha`, `baseline_sha = fix_sha^`, `fix_source`,
   the isolated regression test, and the user-facing ticket text.
4. Prove the isolated oracle is RED at baseline and GREEN at the real fix before
   spending on model cells.
5. Commit `tools/bugfix-bakeoff/external/projects/<id>/manifest.yaml` plus
   `oracles/<bug>.<ext>`.
6. Add or update the project row above with suite, harness, status, and gaps.
7. Run cost-bearing cells only on explicit operator request.
8. Commit durable results under `tools/bugfix-bakeoff/external/results/` and
   summarize public claims in `docs/case-studies/` when there is enough evidence.

## Ownership notes

- This page is the tracking surface. Do not bury candidate state in `.context`
  once it affects reuse.
- Generated reports, decks, logs, and scratch notes belong under `.artifacts/`
  until they are promoted to durable docs.
- Automated tests must stay free of real LLM calls. Live model cells are
  operator-only and should be marked pending when providers fail or throttle.
