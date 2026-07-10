# Story: story-native capsule CI

**Status:** Draft v1. Nothing in this story/pipeline slice is implemented yet.
Existing stories, execution modes, local CI host calls, and flow/cassette tests
are the composition substrate.
**Kind:**   story
**Epic:**   [capsule-ci.md](capsule-ci.md)
**Depends on:** [`capsule-control-plane.md`](capsule-control-plane.md),
[`capsule-environments-executors.md`](capsule-environments-executors.md)

## Why

Kitsoki stories already express richer CI than a shell-step DAG: deterministic
checks, schema-bounded LLM tasks and reviews, human checkpoints, refinement
loops, failure routing, budgets, and fully traced decisions. Yet projects still
describe “CI” as build/test command strings in the project profile and call the
local `iface.ci` provider. There is no small project manifest that says which
story is the required change gate, how a local or remote trigger enters it, what
environment/policy it needs, and which typed result constitutes pass/fail.

The opportunity is not to add a CI stage DSL. It is to make a story itself the
pipeline and give stories a standard trigger/result envelope.

## What changes

Add `.kitsoki/ci.yaml`, a routing and policy manifest that maps named pipelines
to story apps. It declares **where and under what authority** a story runs, not
the steps inside it.

```yaml
# .kitsoki/ci.yaml
schema: capsule-ci/v1
project_profile: .kitsoki/project-profile.yaml
default_environment: ci

pipelines:
  change:
    story: .kitsoki/stories/project-ci/app.yaml
    triggers: [local, pull_request]
    environment: ci
    mode: one-shot
    required: true
    permissions:
      network: replay
      external_write: deny
      remote_read: allow
    agents:
      policy: allow
      profiles: [ci-reviewer]
      max_cost_usd: 2.00
      on_unavailable: needs_input
    result:
      schema: schemas/capsule-ci-verdict-v1.json
      pass_exits: [passed]
      fail_exits: [failed]
      park_exits: [needs_input]
```

Run it identically on either placement:

```sh
kitsoki capsule ci run change --ref HEAD --executor local
kitsoki capsule ci run change --ref HEAD --executor remote-ci
```

The runner creates or adopts a Capsule workspace, registers an artifact job,
builds the execution envelope, starts the named story, and requires a
schema-valid terminal verdict. A story may call `iface.ci.run_tests`, inspect
diffs, invoke read-only or write-capable agents, ask an LLM reviewer to decide,
park for a human, run a refinement loop, or request a sync/promotion plan. Those
are ordinary story rooms and gates, not CI-specific engine concepts.

## Impact

- **Net-new:** `.kitsoki/ci.yaml` schema/loader; one generic
  `stories/capsule-ci/` reference story and one generated project wrapper;
  trigger/result schemas and no-LLM flows.
- **Engine/host changes:** the control-plane `capsule.ci.*` launch/status tools
  and execution-envelope inputs come from the runtime slices; no new room
  effects or gate types.
- **Docs on ship:** `docs/stories/ci.md`, an onboarding recipe, and project CI
  examples.

## Reuse inventory

| Pipeline concern | Mechanism | Reference |
|---|---|---|
| Project commands | `iface.ci.run_tests|build` | `stories/dev-story/app.yaml:297-309`; `internal/host/local_ci.go:31-80` |
| Isolated mutable source | Capsule workspace handle | [`capsule-control-plane.md`](capsule-control-plane.md) |
| Local/remote parity | Execution envelope/provider | [`capsule-environments-executors.md`](capsule-environments-executors.md) |
| Human/LLM/autonomous gates | execution mode + per-room decider | [`execution-modes-and-gate-deciders.md`](execution-modes-and-gate-deciders.md#2-execution-mode) |
| Agent restriction | toolboxes, effect class, `sandbox:` | `docs/stories/state-machine.md:1224-1248`; `docs/architecture/hosts.md:996-1040` |
| Durable run identity | artifact job registry/index | [`artifact-driven-stories.md`](artifact-driven-stories.md#shipped-substrate) |
| Deterministic test/replay | intent-only flows + cassettes | `docs/tracing/testing.md`; existing `stories/bugfix/flows/` |
| Remote ingress/status | provider/GitHub-agent adapter | [`kitsoki-github-agent.md`](kitsoki-github-agent.md) |

## Trigger and result contracts

Every pipeline receives a normalized trigger object plus Capsule handles:

```yaml
world:
  ci_job_id:             { type: string, default: "" }
  ci_pipeline:           { type: string, default: "" }
  ci_trigger:            { type: object, default: {} }
  ci_source:             { type: object, default: {} }
  ci_workspace:          { type: object, default: {} }
  ci_environment:        { type: object, default: {} }
  ci_policy:             { type: object, default: {} }
  ci_changed_paths:      { type: array,  default: [] }
  ci_verdict:            { type: object, default: {} }
  ci_refine_feedback:    { type: string, default: "" }
  ci_review_cycle:       { type: int,    default: 0 }
  ci_review_budget:      { type: int,    default: 2 }
  abandon_reason:        { type: string, default: "" }
```

The normalized trigger has `{kind, provider, event_id, actor, ref, base_ref,
head_sha, pr?, changed_paths?, requested_pipeline, received_at}`. Provider raw
payloads are artifact references, not unbounded world blobs.

The required `capsule-ci-verdict/v1` artifact contains:

```json
{
  "schema": "capsule-ci-verdict/v1",
  "pipeline": "change",
  "outcome": "passed",
  "summary": "All deterministic gates and the schema-bounded review passed.",
  "checks": [
    {"id": "tests", "kind": "deterministic", "outcome": "passed", "evidence": ["artifact:..."]},
    {"id": "review", "kind": "llm_decision", "outcome": "passed", "decision_ref": "event:..."}
  ],
  "promotion_eligible": true
}
```

Allowed outcomes are `passed|failed|needs_input|cancelled|infra_failed`.
`promotion_eligible` is derived and checked by the runner; the story cannot make
it true while required checks are missing or the source/environment/story digest
does not match the run.

## Story graph

The generic reference story demonstrates composition rather than mandating this
exact project graph:

```text
idle ── run ──▶ prepare ──▶ deterministic_checks
                              │ pass
                              ▼
                         llm_review  ◀── refine ──┐
                              │                  │
                  approve ────┼──── changes ──▶ refine_change
                              │                  │
                    uncertain/blocked            └─ budget ─▶ needs_input
                              ▼
                         adjudicate
                         │   │    │
                      passed failed needs_input
                         │     │      │
                         └─────┴──────┴─▶ typed verdict + terminal exit
```

`deterministic_checks` may be several imported sub-stories or host calls.
`llm_review` uses a read-only toolbox and schema. A project that wants no LLM
omits that room/import and keeps the same trigger/result contract. A project
that allows automated fixes uses a separate writer toolbox and Capsule handle;
the review decision and mutation remain distinct calls.

Verifier-only assets (hidden regression tests, reference patches, private
acceptance data) are passed to the named check/verifier process by the Capsule
provider and never exposed through the coding agent's FS/MCP handle. Their
digests and outcomes enter evidence; their contents follow artifact access and
redaction policy.

## Per-room detail

### `idle` — validate launch contract

- **`on_enter`:** none; render pipeline, source, environment, executor, spend,
  and permission summary from the execution envelope.
- **Intents:** `run`, `quit`.
- **View:** exact source SHA, story/environment lock digests, external-write
  posture, LLM profile/budget, and whether the trigger expects synchronous or
  parked completion.

### `prepare` — prove the capsule is ready

- **`on_enter`:** deterministic `capsule.workspace.status`, environment verify,
  changed-path normalization, and required input/schema checks.
- **Routing:** preparation mismatch -> `infra_failed`; ready ->
  `deterministic_checks`.

### `deterministic_checks` — run project facts

- **`on_enter`:** project story composes `iface.ci` calls or imported check
  stories. Each result emits an evidence artifact rather than relying only on
  prose logs.
- **Routing:** required failure -> `failed`; all required facts present -> next
  review/adjudication room.

### `llm_review` — optional schema-bounded interpretive gate

- **`on_enter`:** `host.agent.decide`/`task` with declared profile, toolbox,
  sandbox, cost cap, diff/evidence references, and a result schema.
- **Intents:** `approve`, `refine(feedback)`, `needs_input`, `quit`; execution
  mode may auto-decide or park.
- **View:** model/profile/cost, schema verdict, evidence references, and review
  limitations—not just “AI passed.”

### `refine_change` — optional bounded writer

- **`on_enter`:** a write-capable agent receives only the Capsule MCP grant for
  this workspace. It cannot publish or promote unless those separate tools were
  granted.
- **Intents:** rerun checks, stop for operator, quit. Cycle budget prevents
  unbounded autonomous spend.

### `adjudicate` — build the typed result

- **`on_enter`:** deterministic script validates required check ids, evidence,
  source/story/environment digests, budget/policy status, and produces
  `capsule-ci-verdict/v1`.
- **Exits:** `passed`, `failed`, `needs_input`, `cancelled`, `infra_failed`, each
  requiring the typed verdict.

## Net-new files

```text
.kitsoki/
├── ci.yaml
├── environments/ci.yaml
└── stories/<project>-ci/app.yaml

stories/capsule-ci/
├── app.yaml
├── rooms/{idle,prepare,deterministic_checks,llm_review,refine_change,adjudicate}.yaml
├── prompts/review.md
├── schemas/{trigger,review-verdict,ci-verdict}.json
├── scripts/build_verdict.star
├── flows/{deterministic_pass,deterministic_fail,llm_review_replay,needs_input,budget_exhausted}.yaml
└── README.md
```

The base story is optional composition. Onboarding may generate a smaller
project wrapper that imports only deterministic checks and adjudication.

## Flow fixtures

- `deterministic_pass` — prepared capsule -> checks -> valid `passed` verdict.
- `deterministic_fail` — a failed required command produces `failed`, never a
  model/infra ambiguity.
- `llm_review_replay` — cassette decision is linked by event id and cost/profile
  metadata; no live model.
- `needs_input` — unavailable profile, low confidence, human gate, or missing
  authority parks honestly.
- `budget_exhausted` — refinement at budget yields `needs_input`, not a hidden
  extra agent call.
- `remote_equivalence` — fake remote executor consumes the same envelope and
  produces the same normalized verdict as local.
- `receipt_mismatch` — changed source/environment/story digest prevents
  promotion eligibility.

## Tasks

```text
## 1. Routing and contracts
- [ ] 1.1 Define/load `.kitsoki/ci.yaml`; validate story, trigger, environment, executor, permission, agent budget, and result references
- [ ] 1.2 Define normalized trigger and capsule-ci-verdict/v1 schemas plus terminal-exit mapping
- [ ] 1.3 Add `capsule ci plan|run|status|cancel` CLI/MCP over artifact-job and execution-envelope seams

## 2. Reference composition
- [ ] 2.1 Scaffold stories/capsule-ci with typed views and prepare/check/review/refine/adjudicate rooms
- [ ] 2.2 Implement deterministic verdict builder and promotion-eligibility checks
- [ ] 2.3 Add no-LLM flows for pass/fail/park/budget/remote-equivalence/digest mismatch
- [ ] 2.4 Add cassette-backed LLM review and Capsule-MCP-only writer fixture; prove no ambient tools are required

## 3. Adopt and document
- [ ] 3.1 Extend onboarding to generate `.kitsoki/ci.yaml`, environment definition, and a minimal project CI wrapper from project-profile commands
- [ ] 3.2 Dogfood Kitsoki's focused validation + review story locally, then through a fake and one gated real remote executor
- [ ] 3.3 Add GitHub trigger/check adapter consuming the same pipeline/result contract
- [ ] 3.4 Migrate story/CI docs and examples; trim/delete this proposal
```

## Open questions

1. **Base story vs. contract only** — require import of `@kitsoki/capsule-ci` or
   allow any story with the trigger/result contract. *Lean: contract is
   authoritative; the base story is a gold-standard convenience.*
2. **Live LLM CI default** — deny unless pipeline opts in vs. inherit harness
   availability. *Lean: explicit `agents.policy: allow`, allowlisted profiles,
   cost cap, and fallback are all required.*
3. **Synchronous trigger timeout** — keep a provider request open vs. always
   return a run URL. *Lean: artifact job/run URL immediately; provider adapters
   may poll or subscribe and publish the terminal receipt.*

## Non-goals

- Recreating YAML `steps`, `needs`, matrices, or shell-script DAGs.
- Calling a real LLM in Kitsoki's automated tests.
- Granting publish/merge authority because a pipeline is required or green.
- Requiring every CI story to auto-fix; read-only review and deterministic-only
  pipelines are first-class.
