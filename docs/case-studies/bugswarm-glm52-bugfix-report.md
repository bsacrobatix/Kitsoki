# BugSwarm + GLM-5.2 bugfix comparison report

Generated at: `2026-07-06T00:00:00Z`.

This report is generated offline from committed evidence. It does not call
BugSwarm, Docker, or any LLM. Missing cells are reported as `pending`.

## Research Question

Compare the Kitsoki `bugfix` pipeline with raw prompts on GLM-5.2,
using total token usage as the primary cost axis, and prepare the same
success-rate comparison for a BugSwarm corpus alongside the existing
OSS oracle corpus.

The current committed evidence does not yet contain the full GLM-5.2
matrix. This report therefore separates observed results from missing
cells instead of imputing raw-prompt or BugSwarm numbers.

## Method

The headline matrix has one row per `(corpus, treatment)` bucket.
A cell is counted as attempted only when its quality is `solved`,
`partial`, or `failed`; `pending` and `blocked` are excluded from the
model-quality denominator. Token totals are summed only from committed
cell evidence that records real usage.

Inputs:

- GLM-5.2 bakeoff cells: `tools/bugfix-bakeoff/results/cells`.
- Arena supporting rollup: `tools/arena/results/round-1/rollup.json`.
- OSS oracle corpus: `tools/arena/corpus/cost-bench.manifest.yaml`.
- Source catalog: `tools/arena/corpus/sources.yaml`.

Primary metrics:

- success rate: `solved / (solved + partial + failed)`.
- partial rate: reported separately because hidden oracles can be
  implementation-coupled.
- total tokens: provider-neutral primary cost measure.
- USD cost: secondary; only shown where committed cell evidence provides
  it.

## Corpus Coverage

| corpus | tasks | repositories | verified/imported status |
|---|---:|---:|---|
| OSS oracle corpus | 26 | 12 | frozen and locally validated |
| BugSwarm | 0 | n/a | adapter-ready; verified tasks: 0 |

The OSS oracle corpus remains the active internal benchmark source. It
covers the pre-registered public OSS targets plus existing hidden-oracle
bugfix fixtures. BugSwarm is represented separately in the source
catalog, so its fail/pass CI artifact sampling process does not get
collapsed into the OSS oracle denominator.

BugSwarm source contract:

- import explicit exported artifact metadata with
  `tools/arena/scripts/bugswarm_to_arena.py`.
- require `image_tag`, `repo`, `failed_job_id`, and `passed_job_id`.
- treat the failed job as RED and the passed job as GREEN inside the
  artifact image.
- keep imported tasks unattempted until Docker verification proves both
  sides still reproduce.

## GLM-5.2 Headline Matrix

| corpus | treatment | n | attempted | solved | partial | failed | pending | success rate | tokens |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| bugswarm | kitsoki | 1 | 0 | 0 | 0 | 0 | 1 | n/a | n/a |
| bugswarm | raw-prompt | 1 | 0 | 0 | 0 | 0 | 1 | n/a | n/a |
| oss-oracle | kitsoki | 1 | 1 | 0 | 1 | 0 | 0 | 0.000 | 2,890,980 |
| oss-oracle | raw-prompt | 1 | 0 | 0 | 0 | 0 | 1 | n/a | n/a |

## Committed GLM-5.2 Cells

| task | treatment | quality | tokens | cost | evidence |
|---|---|---|---:|---:|---|
| bug9 | kitsoki | partial | 2,890,980 | $15.575450 | `tools/bugfix-bakeoff/results/cells/bug9-glm-5.2-kitsoki.json` |

## Evidence Gaps

- No committed raw-prompt GLM-5.2 result exists for the OSS oracle corpus.
- No BugSwarm artifact source has been imported and RED/GREEN verified yet.
- No committed GLM-5.2 Kitsoki or raw-prompt result exists for BugSwarm.

## Interpretation

- Committed GLM-5.2 Kitsoki evidence contains 1 attempted OSS oracle cell(s), 2890980 total tokens, and no solved cell yet.
- The GLM-5.2 raw-prompt arm remains pending; the report must not compute a token ratio from missing data.
- BugSwarm is adapter-ready in the source catalog, but the committed report has no imported artifact subset yet.

Bottom line: the committed GLM-5.2 evidence is not yet sufficient to
claim Kitsoki beats or loses to raw prompts. The report is useful now as
a reproducible evidence ledger and corpus scaffold; the headline
comparison still requires raw-prompt GLM-5.2 cells and verified
BugSwarm cells.

## Supporting Codex-Native OSS Round

The existing arena `round-1` results are supporting evidence for the
Kitsoki-vs-raw-prompt harness and token accounting, but they are not
GLM-5.2 cells. They should not be used to answer the GLM headline.

| treatment | n | attempted | solved | failed | success rate | tokens |
|---|---:|---:|---:|---:|---:|---:|
| kitsoki | 4 | 4 | 2 | 2 | 0.500 | 21,459,517 |
| raw-prompt | 4 | 4 | 2 | 2 | 0.500 | 537,743 |
