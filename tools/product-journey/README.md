# Product Journey Evaluator

This directory holds the first runnable harness for the **exploratory product
journey** experiment:

- discover how Kitsoki behaves as a skeptical, practical evaluator,
- keep checks deterministic by default,
- and emit evidence artifacts (log + deck) as execution progresses.

## Naming

One product, four names:

| Name | What it is |
|---|---|
| **Persona QA** | The product ([`docs/persona-qa.md`](../../docs/persona-qa.md)). |
| **`scenario-qa`** | The story you run for one scenario x N transports ([`../../stories/scenario-qa/README.md`](../../stories/scenario-qa/README.md), alias `@kitsoki/persona-qa`). |
| **`product-journey-qa`** | The story for persona x scenario x 10-repo matrix sweeps ([`../../stories/product-journey-qa/README.md`](../../stories/product-journey-qa/README.md)). |
| **`product-journey`** | This directory — the deterministic runner backend (`run.py`) both stories drive. |
| **`persona_qa`** | `tools/persona_qa` — a shared support kit, not an operator surface. |

## Story-Owned Persona QA

Use `stories/scenario-qa` for project-facing Persona QA work:

```sh
kitsoki run @kitsoki/scenario-qa
```

Then drive the story with natural prompts:

```text
preview project-onboarding across all transports
check project-onboarding across all transports for core-maintainer on gears-rust
next leg
report
```

`tools/product-journey/run.py` is the deterministic backend for that story. It
emits run bundles, transport suites, review artifacts, and Slidey decks, but it
is not the operator product surface. `tools/persona_qa` remains an internal
compatibility package for schemas, completion-state conversion, retained fixture
deck generation, and no-LLM tests. See `docs/persona-qa.md` for the supported
story contract.

## How to run the compatibility runner

The harness is intentionally small and opinionated for the first milestone:

```sh
python3 tools/product-journey/run.py
```

That prints catalog status and perspective checks (including PostgreSQL and
Kubernetes placeholders).

Run a specific project check:

```sh
python3 tools/product-journey/run.py --project gears-rust --mode check
```

Emit a repeatable no-LLM dry-run bundle and Slidey deck:

```sh
python3 tools/product-journey/run.py --emit-run --project gears-rust --persona core-maintainer --seed demo
```

Emit the same bundle scoped to specific transports (`tui`, `web`, `vscode`,
`cli`, or `all`) so `execution-plan.md`/`driver-plan.json` enumerate scenario x
transport legs instead of one entry per scenario:

```sh
python3 tools/product-journey/run.py --emit-run --project gears-rust --persona core-maintainer --seed demo --transport all
python3 tools/product-journey/run.py --emit-run --project gears-rust --persona core-maintainer --seed demo --scenarios bugfix --transport tui,web
```

Each leg carries its own `required_mcp`/`evidence` (from the scenario's
`transports.overrides`, when declared) and a `transport_evidence_contract`
naming the capture tool, evidence kind, and proof level for that transport
(`tui` -> `render.tui_png` frames, `web` -> `visual.snapshot`/rrweb, `vscode`
-> `visual.open kind=vscode`, always labeled bridge-level, `cli` -> command
transcript, labeled terminal-level). A scenario that
doesn't allow a requested transport is skipped for it rather than erroring.
Omitting `--transport` keeps today's one-entry-per-scenario output unchanged.
Every execution step and driver-plan scenario also carries deterministic
`capture_routes`. A route is generated from the run id, scenario id, primary
story, transport profile, evidence kind, and driver manifest. It names the
story load intent, the primary `session.new` shape, resolved open/observe/act
tools, recording start/stop policy, artifact path template, and exact
attach/blocker/journal commands. Drivers should follow the route for setup and
recording instead of choosing ad hoc entrypoints.

Validate the reusable natural-use corpus before planning a sweep:

```sh
python3 tools/product-journey/run.py --validate-corpus
```

This checks that personas, scenarios, quality gates, evidence hints, and the
10-repo GitHub target catalog still line up. Inside
`stories/product-journey-qa/app.yaml`, submit `validate_corpus` for the same
no-LLM preflight.

Before spending a live persona run, run the capture preflight:

```sh
python3 tools/product-journey/run.py --capture-preflight --json-output
```

It fails closed when webshot capture is broken, Studio MCP `studio.ping` is not
healthy, the provider-quota state file is malformed, or an active quota
cooldown window would make unattended capture waste spend.

Emit a repeatable 10-repo GitHub planning matrix:

```sh
python3 tools/product-journey/run.py --refresh-github-targets --seed demo
python3 tools/product-journey/run.py --emit-matrix --seed demo
python3 tools/product-journey/run.py --emit-matrix --seed demo \
  --target-proof-file .artifacts/product-journey/target-proofs/<proof-id>
python3 tools/product-journey/run.py --emit-matrix --seed demo --matrix-personas all
python3 tools/product-journey/run.py --validate-matrix \
  --matrix-dir .artifacts/product-journey/matrices/<matrix-id> \
  --strict-target-proof
```

Prove the no-LLM end-to-end artifact loop in one command:

```sh
python3 tools/product-journey/run.py --dogfood-smoke --seed demo
```

This creates a 10-repo matrix, turns the first deterministic assignment into a
run bundle, seeds representative demo evidence plus one driver journal event per
scenario, reviews and validates the run, rolls it back into the matrix,
validates the matrix, and writes a smoke report under
`.artifacts/product-journey/dogfood/<dogfood-id>/`. It also emits a smoke-level
Slidey deck plus the normal run, matrix, and rollup decks. The demo evidence
proves aggregation, driver-journal wiring, and deck shape only; live visual MCP
or cassette evidence is still required before making product claims. Because
seeded demo artifacts are not proof evidence, the smoke command can pass while
the seeded run review remains `needs_evidence` with an unsatisfied quality gate.

Run the story-owned persona-QA marathon in deterministic replay mode:

```sh
python3 tools/product-journey/run.py --autonomous-marathon \
  --project vscode --persona core-maintainer --seed demo --scenarios bugfix \
  --autonomous-driver-mode replay \
  --ticket-repo owner/repo --gh-agent-public-base-url https://agent.example
```

Replay mode creates the run, attaches cassette-backed local proof artifacts,
records the driver journal and findings, runs native gitops filing/fixing/
close-out, refreshes review artifacts, validates, routes weaknesses to PRD/
design, and derives stats without operator glue. Leave
`--autonomous-driver-mode` at `pending` when a live budgeted driver should
capture evidence before finalization.

## Standing Universal QA Campaign

Use the active persona/scenario corpus as the source of truth for ongoing
product QA across Kitsoki's public product site, docs, Studio MCP, Codex agent
launch, web UI, TUI, and remote-worker experience. The reusable campaign loop
is:

1. Run no-LLM preflights (`--validate-corpus`, `--capture-preflight`, replay
   smoke) before any live driver spend.
2. Emit a bounded run or autonomous marathon from catalog data, not a private
   recorder case list.
3. Capture evidence through the generated `capture_routes` and attach it back
   to the run.
4. Route credible findings through the existing evidence-backed issue/fix
   pipeline; keep local stabilization bugs under `.artifacts/issues/bugs`
   unless the scenario's handoff boundary is GitHub.
5. Regenerate review artifacts, stats, and the Slidey deck from retained run
   artifacts. A red evidence, validation, issue, or gh-agent gate keeps the
   campaign in `needs_evidence` or blocked state.

The campaign-oriented active scenarios are:

- `docs-to-mcp-first-run` - product site/docs to Studio MCP and scenario QA.
- `agent-launch-experience` - Codex/Kitsoki agent launch, profile visibility,
  web/TUI state parity, and operator-ask behavior.
- `remote-worker-campaign` - VM/arena readiness, bounded batch placement,
  evidence-backed filing/fixing, and rollup refresh.
- `campaign-rollup-review` - stakeholder summary, Slidey deck, coverage gaps,
  cost, open issue/fix state, and next campaign slice.

The additional active personas (`workflow-author`, `remote-ops-owner`, and
`enterprise-evaluator`) are intentionally not Kitsoki-maintainer personas. They
exercise the same runner/story/issue/deck pipeline from the perspective of a
user adapting Kitsoki to their own stories, operating a remote worker, or
evaluating whether the product is adoption-ready.

Prove one reusable-driver scenario loop with cassette-backed proof evidence:

```sh
python3 tools/product-journey/run.py --driver-replay-smoke --seed demo
python3 tools/product-journey/run.py --driver-replay-smoke \
  --smoke-scenario project-onboarding \
  --seed demo
python3 tools/product-journey/run.py --driver-replay-sweep --seed demo
```

This creates a normal run bundle, attaches all `bugfix` minimum-evidence slots
or the selected scenario's minimum-evidence slots with `cassette://` refs,
records a matching `driver-journal` event, writes findings, reviews the run,
validates the bundle, and emits a compact smoke report/deck under
`.artifacts/product-journey/dogfood/<smoke-id>/`. The review is expected to
stay `needs_evidence` because the other scenarios are incomplete, but validation
must pass and the `driver-evidence-linked` check must be satisfied for the
captured scenario. Scenarios without a playback-capable minimum-evidence slot
will still expose the missing playback proof in review; that is a contract gap
to fix before calling that scenario representative.
Use `--driver-replay-sweep` when you want a single no-LLM gate that every
scenario can replay with cassette-backed proof evidence, a linked driver
journal, clean validation, and at least one playback item for the generated
Slidey deck. In smoke and sweep JSON, `status=passed` means the deterministic
replay contract passed; `readiness_status=ready|needs_evidence` is the separate
review-readiness signal to use before autonomous scheduling or landing.

This writes `.artifacts/product-journey/matrices/<matrix-id>/` with
`matrix.json`, `matrix.md`, and `deck.slidey.json`. The source target list lives
in `github-targets.json`; `--refresh-github-targets` writes
`.artifacts/product-journey/target-proofs/<proof-id>/target-proof.json` with
current GitHub API counts for each target's `bug_query` plus repository
popularity and license metadata. Feed that proof into `--emit-matrix` before a
live scored sweep so the matrix records whether every target currently satisfies
the 100-open-bug floor, configured stargazer floor, and open-source license
contract.
Use `--validate-matrix --strict-target-proof` before a live scored sweep; it
turns missing refreshed GitHub proof into an error instead of a draft-planning
warning.
Each matrix assignment includes deterministic `scenario_tasks` that specialize
the shared scenarios for the target repository, persona, stack, and bug query;
use those prompts to keep natural-use runs repeatable instead of inventing a new
task shape per run.
Every target `id` from `github-targets.json` is also accepted by `--emit-run`,
so a matrix assignment can become a concrete run bundle:

```sh
python3 tools/product-journey/run.py --emit-run --project vscode --persona docs-minded-contributor --seed demo-01
```

To reconnect a Kitsoki story session or MCP-only driver to an existing bundle,
load it through the story:

```text
load run_dir=.artifacts/product-journey/<run-id>
```

The story calls `--summarize-run --json-output` so the driver can see the run
paths, persona lens, review counts, compact `driver_scenarios`, final gates, and
proof backlog through `session.world last_result` before attaching evidence.
Loaded runs also expose `last_result.next_driver_capture_route`, the structured
route object for the first missing proof slot. That object is the stable
entrypoint for opening the primary story, recording the interaction, attaching
proof, journaling the attempt, or recording an honest blocker.

After one or more assignment runs have captured evidence and been reviewed,
roll them back up into the matrix:

```sh
python3 tools/product-journey/run.py --rollup-matrix \
  --matrix-dir .artifacts/product-journey/matrices/<matrix-id> \
  --rollup-run-dir .artifacts/product-journey/<run-id>
```

The rollup writes `rollup.json`, `rollup.md`, and `rollup.slidey.json` into the
matrix directory. Omit `--rollup-run-dir` to auto-discover run bundles whose
project, persona, and seed match matrix assignments. Use the assignment
`emit_run_command` in `matrix.json` or `matrix.md` when you want auto-discovery
to pick up the run without extra flags. The rollup includes per-scenario
outcome totals so repeated onboarding, bugfix, PRD/design, implementation, and
product-bug gaps are visible across runs. It also includes per-persona outcome
rows so cross-run review can compare whether core maintainer, dependency
debugger, docs-minded contributor, and IDE-first lenses produce different
evidence and findings. It also aggregates driver-journal events by scenario so
matrix review can tell which journeys the reusable driver actually attempted,
captured, blocked, or validated. It also aggregates scenario
`quality_gate` coverage so the matrix deck shows which journeys have enough
proof-source minimum evidence to count as completed, plus a missing-proof
evidence backlog that names the evidence kinds still needing live visual MCP or
cassette-backed capture. Each missing-proof row also lists affected run IDs,
project/persona pairs, and `driver-handoff.md` paths so the next capture pass
can jump directly from the rollup into the per-run proof backlog.
Validate a generated matrix before using it as the sweep contract:

```sh
python3 tools/product-journey/run.py --validate-matrix \
  --matrix-dir .artifacts/product-journey/matrices/<matrix-id>
```

`--validate-run --json-output` and `--validate-matrix --json-output` include a
`validation_issue_summary` field that lists the first error or warning check IDs.
The `--dogfood-smoke --json-output` path exposes separate run and matrix issue
summaries so the Kitsoki story can show why warning counts are non-zero.

This writes `.artifacts/product-journey/<run-id>/` with `run.json`,
`journey.md`, `metrics.json`, `bugs.json`, `findings.json`,
`scenario-outcomes.json`, `scenario-outcomes.md`, `evidence.json`,
`media-manifest.json`, `scenarios.json`, `execution-plan.json`,
`execution-plan.md`, `driver-plan.json`, `driver-plan.md`,
`driver-journal.json`, `driver-journal.md`, `agent-brief.json`,
`agent-brief.md`, `driver-handoff.json`, `driver-handoff.md`, `review.json`,
and `deck.slidey.json`.
Add `--publish-deck` when the generated deck should replace
`docs/decks/product-journey-eval.slidey.json` for review.

Use `agent-brief.md` as the live-driver handoff: it states the persona,
operating rules, persona lens, driver manifest, scenario order, MCP tools,
success criteria, and missing evidence without implying planned steps are
validated. The lens makes
the same scenario run differently for core maintainers, dependency debuggers,
docs-minded contributors, and IDE-first engineers while keeping behavior
repeatable. The brief names
`.agents/agents/product-journey-qa-driver.md` as the reusable live/cassette
driver. Use `driver-plan.md` for the
machine-readable harness, visual-surface, action-sequence, and gate contract,
including `resolved_mcp_tools` and per-action `resolved_tools` entries that map
scenario-level aliases like `session.open` to concrete driver tool names,
`driver-journal.md` for the auditable record of what the driver actually tried,
`execution-plan.md` for the detailed evidence slots and ready-to-fill
`--attach-evidence` commands, and `driver-handoff.md` as the operator handoff
that names the driver agent, dispatch modes, missing evidence, and final gates
without launching live LLM work. When demo or partial evidence is already
attached, use the handoff's `Missing Proof Evidence` section as the live or
cassette capture backlog; raw `missing_evidence` can be empty while proof-source
quality gates are still unsatisfied. Each missing proof row includes slot-level
capture hints, deterministic `capture_route` data, and ready-to-fill
`--attach-evidence` commands, so the driver can work directly from the handoff
instead of reverse-engineering setup, recording, or commands from the generic
evidence list.
When the run is loaded through `stories/product-journey-qa/app.yaml`, the story
also exposes `last_result.next_driver_capture` and
`last_result.next_driver_capture_route` plus
`last_result.next_driver_attach_command` so a reusable driver can start with the
first missing proof slot without reopening the markdown handoff.
Each scenario also carries a `quality_gate` with `minimum_evidence`,
`done_when`, and `block_if` rules. Live/cassette drivers should satisfy that
gate before calling a scenario done, or record a blocker tied to the matching
condition. The generated Slidey deck includes a `Proof gates` scene that rolls
up each scenario's minimum-evidence coverage and current outcome for review.
Every representative scenario includes `key_interaction_video` in its minimum
evidence so the final review deck can show playback of the important operator
path instead of only static traces or written artifacts.
`--validate-run` checks that `execution-plan.json` and `driver-plan.json`
include one actionable `--attach-evidence` command for every declared evidence
slot, and that the execution plan, agent brief, driver plan, and handoff retain
the story-owned final gates: `autonomous_watchdog`, `autonomous_fix`, `review`,
and `validate`.
It also checks that every evidence slot has a matching `capture_route`, and
that the route's setup entrypoint, artifact template, attach command, blocker
command, and journal command stay synchronized with the surrounding plan and
handoff.
It also enforces the
driver action contract: every scenario must keep the ordered
`open_surface -> read_current_frame -> act_as_persona -> capture_required_evidence -> journal_attempt`
sequence with the required fields and an auditable `journal_attempt` recording
path. A valid bundle should be directly usable by the driver.
`--review-run` includes the same contract as a hard review check and writes a
`Driver contract` Slidey scene, so human review can spot drift in the reusable
open/observe/act/capture/journal loop without opening the raw JSON.
Use `--driver-replay-smoke --smoke-scenario <scenario-id>` before a live pass
when you want a cheap proof that the attach commands, driver journal refs, media
manifest, review checks, and validation gates still compose around one
cassette-backed scenario.

### Driving Surfaces

Product-journey resolves abstract capabilities through a driver manifest in
`tools/product-journey/drivers/`. `kitsoki-mcp` is the default and preserves the
existing Kitsoki Studio MCP / visual MCP tool mapping. Pass `--driver <id-or-path>`
to `--emit-run` or `--emit-matrix` to generate bundles for another surface, and
use `--driver-smoke <id-or-path>` to validate a manifest without launching the
target app.

A manifest must provide `id`, `label`, `app_kind`, and a `capabilities` map for
the canonical keys in `schema.json` (`visual.open`, `visual.observe`,
`visual.act`, `session.open`, `session.status`, `session.submit`,
`session.drive`, `session.inspect`, `session.trace`, `render.tui`). Values may
be one concrete tool name or an ordered list. Optional `launch`, `ready`,
`affordances`, `evidence_contract`, and `oracles` describe how an external app is
brought up and proved, but smoke validation only shape-checks launch data and
never starts the app.

Scenarios should refer to manifest affordance names such as
`affordance:open-dashboard`, never raw selectors. The driver manifest is the
place where a downstream web app maps those names to selectors or action handles.
`drivers/web-generic.json` is a placeholder browser/Playwright-style surface,
and `drivers/web-generic.example.json` shows how a consumer can add launch,
ready, affordance, and oracle details in its own repo.

`drivers/claude-in-chrome.json` is the first *bound* real-browser surface: it
maps every canonical capability to the `mcp__claude-in-chrome__*` tools that a
Claude session with the Claude-in-Chrome extension actually exposes, so a
driver agent can explore the real UI (the kitsoki web UI, the product site, or
a downstream web app) instead of a placeholder tool list. Two constraints of
that surface are encoded in the manifest rather than left as tribal knowledge:

- **Evidence must be file-addressable.** Raw `computer` screenshots return an
  opaque id with no filesystem path, so they can never satisfy an evidence
  contract that records artifact paths. The manifest's `evidence_contract.web`
  names `gif_creator` as the primary proof tool: record around the interaction,
  export with `download: true`, then copy the `.gif` from `~/Downloads` into
  the run evidence dir before `--attach-evidence` (`.gif` classifies as a
  video media kind). Console and network reads return text only; the driver
  agent persists them to files itself.
- **Operating guidance rides in `notes`.** A driver manifest may declare an
  optional `notes` array (non-empty strings, schema- and smoke-validated);
  `--emit-run` interpolates it into `agent-brief.md` as a `### Driver Notes`
  section. The claude-in-chrome notes carry the empirically-derived rules —
  tabs-context first, screenshot-space coordinates, ref staleness across
  re-renders, small `browser_batch` sequences ending in a screenshot, and the
  no-dialogs rule — so every generated bundle briefs its driver on the
  surface's real failure modes.

Validate it like any other manifest:

```sh
python3 tools/product-journey/run.py --driver-smoke claude-in-chrome
python3 tools/product-journey/claude_in_chrome_driver_test.py
```

Attach evidence captured by a live or cassette-backed MCP run:

```sh
python3 tools/product-journey/run.py --attach-evidence \
  --run-dir .artifacts/product-journey/<run-id> \
  --scenario bugfix \
  --evidence-kind key_interaction_video \
  --evidence-path media/bugfix.mp4 \
  --evidence-source local \
  --notes "visual MCP capture from bugfix handoff"
```

After each scenario attempt, append a driver journal event so the run records
what was actually tried:

```sh
python3 tools/product-journey/run.py --record-driver-event \
  --run-dir .artifacts/product-journey/<run-id> \
  --scenario bugfix \
  --dispatch-mode replay \
  --driver-status captured \
  --mcp-tools session.open,render.tui,visual.observe \
  --evidence-refs traces/bugfix.jsonl,media/bugfix.mp4 \
  --summary "Replayed the bugfix story through the oracle gate."
```

When a run is created with `--live-budget-minutes 0`, `dispatch_mode=live` is
accepted only with `--driver-status blocked`. Captured or validated live events
fail closed in the story and in `--validate-run`; use replay/cassette evidence
or record the live path as blocked instead of silently falling through.

Attachment updates `evidence.json`, `media-manifest.json`, `scenarios.json`,
`scenario-outcomes.md`, `metrics.json`, `agent-brief.md`, `journey.md`, and
`deck.slidey.json`.
The manifest classifies captured artifacts as video, image, trace, document, or
artifact and feeds the Slidey playback scene with structured media entries.
Embeddable video, rrweb, and image evidence also produce standalone
`Playback evidence` scenes in `deck.slidey.json`, so review can jump directly to
key interactions instead of scraping paths from prose.
The generated deck also includes a `Persona lens` scene so cross-run review can
compare why a core maintainer, dependency debugger, docs-minded contributor, or
IDE-first engineer started from different surfaces and weighted evidence
differently.
Scenario outcomes summarize evidence coverage and finding counts per scenario
so onboarding, bugfix, PRD/design, feature implementation, and product-bug gaps
stay visible independently of the bundle-level review status.
Open observed `weakness` findings also generate `weakness-routes.json`,
`weakness-routes.md`, and a `PRD/design routes` deck scene. Those route rows
preserve the persona, scenario, evidence path, and suggested PRD idea so
usability/product-shape gaps go to `stories/prd` instead of the bugfix queue.
Use `--evidence-source local`, `retained`, `external`, or `cassette` for proof
evidence. `demo` is reserved for deterministic placeholder evidence, and
captured `unknown` evidence does not count as proof evidence. Use a real local
path relative to the run bundle, an absolute path, a repo-root path, a URL, or a
retained MCP reference such as `retained://...` or `image://...`. Review and
validation warn when captured local paths do not
resolve, so placeholder media cannot silently look like real playback proof.
Each evidence row also carries a `source`: `demo` for seeded placeholders,
`retained` for MCP retained references, `external` for URLs, `local` for
file-backed captures, `cassette` for recorded deterministic replay artifacts,
and `unknown` when a captured source cannot be inferred. Review decks and
readiness checks count proof evidence separately from demo/unknown evidence so a
no-LLM smoke can exercise the artifact loop without passing as live product
proof.

**File-backed proof must resolve.** `local` and `cassette` proof must point at a
real artifact on disk — a `cassette://product-journey/<run_id>/<rel>` URI is a
LOCAL recorded artifact (it resolves to `<run_dir>/<rel>`), not a remote URL, so
an unbacked `cassette://…/nothing.diff` neither resolves nor counts as proof
toward the quality gate. Only genuinely remote/opaque schemes (`http(s)://`,
`retained://`, `image://`, `trace://`, `mcp://`) are treated as present without a
stat. Regression: `tools/product-journey/cassette_proof_test.py`.

**Every scenario carries one playback-capable evidence slot.** Alongside its
free-form evidence kinds, each active (non-mined) scenario declares exactly one
kind from `rrweb`, `trace-replay`, `flow-fixture`, or `png-sequence` — the four
kinds that can actually be *replayed* (an rrweb viewer, `kitsoki test flows`, a
PNG frame sequence) rather than merely referenced. This slot is held to a
stricter bar than general proof evidence: a `cassette://`, `http(s)://`,
`retained://`, or other opaque/indirect URI is **never** accepted, even though
those same URIs count as proof elsewhere — see the memory note that a
`cassette://` reference is unbacked/fake proof for replay purposes. The
`playback-evidence-backed` review check and the `scenario-playback-evidence`
(corpus) / `playback-evidence-unbacked` (bundle) validation checks enforce
this. Regression: `tools/product-journey/playback_evidence_test.py`.

Findings carry an `origin`: `observed` for findings a driver/operator recorded
from a real interaction (the default for `--record-finding`), and `seeded` for
the templated placeholders that `--seed-demo-evidence` attaches. The
`observed-findings` review check enforces an accuracy floor: a run backed by real
proof evidence but carrying *only* seeded findings **fails** (it describes the
harness, not the product), while a pure no-LLM smoke with no proof evidence stays
a warning so the deterministic dogfood loop keeps passing.

Record a review finding for the deck summary:

```sh
python3 tools/product-journey/run.py --record-finding \
  --run-dir .artifacts/product-journey/<run-id> \
  --finding-kind weakness \
  --scenario project-onboarding \
  --title "Onboarding hid the next command" \
  --summary "The persona could not tell which Kitsoki story to launch after config generation."
```

Finding kinds are `strength`, `weakness`, `issue`, and `fix`.

If a scenario was attempted but cannot honestly capture evidence under the
current harness, record a blocker instead of leaving it invisible or pretending
it passed:

```sh
python3 tools/product-journey/run.py --record-blocker \
  --run-dir .artifacts/product-journey/<run-id> \
  --scenario prd-design \
  --title "Design scenario requires live model authorization" \
  --summary "No cassette exists for this path, and automated tests must stay no-LLM."
```

The review gate treats a scenario as attempted when it has captured evidence or
an explicit blocker, so missing live paths stay visible in the deck and rollup.

For the full issue-to-fix loop, drive the loaded
`stories/product-journey-qa/app.yaml` run through the story-owned autonomous
gate:

```text
autonomous_fix ticket_repo=<owner/repo> gh_agent_public_base_url=<url>
```

That single intent files every credible `issue` finding as a GitHub issue with
uploaded evidence, enqueues and drains native gh-agent repair jobs, refreshes
the review/deck artifacts, and validates the bundle. Under the story boundary
it calls the native `kitsoki gitops autonomous-fix` facade, which uses the same
artifact-preserving orchestration as the web Report-bug and TUI `/bug`
surfaces: `kitsoki bug file-findings`
(host.GitHubFileFindings) walks `findings.json` and, for every credible finding
(kind `issue`, origin not `seeded`) without a recorded issue, assembles an
expected/actual/reproduction body from the finding, the driver-plan scenario
contract, and the driver journal; searches open issues for a strong title match
before creating anything; and either comments the related finding on the
existing issue or uploads locally-resolvable evidence as GitHub release assets
linked from a new issue's `## Artifacts` section. Search failures fail the
filing gate closed instead of silently creating a duplicate. In both the related
and newly-filed paths, the command writes `item.github_issue` plus
`findings.filing` back into `findings.json`. Native gh-agent queue state
defaults to `<run_dir>/gh-agent-jobs.sqlite`; pass `gh_agent_db=<sqlite>` only
to override that run-local path. Before a queued repair is drained, the facade
posts a `kitsoki-autofix-claim` comment through `host.gh.ticket` and records
the claim URL in `findings.json`, so parallel agents and reviewers can see that
the issue is already in flight. Completed gh-agent fix jobs must expose both
reviewable fix evidence and an `independent-verify.md` asset produced by the
story dispatcher; the autonomous review and validation gates fail closed when a
job only provides a fix report or patch. After filing, gh-agent, review, and
validation gates are green, the same native gitops facade posts a
`kitsoki-fixed-in` close-out comment through `host.gh.ticket`, closes the
GitHub issue, and writes `findings.issue_closeout` plus closed issue state back
into `findings.json`; follow-up stats can then be derived from the run bundle
instead of hand-maintained notes.
If a completed gh-agent drain needs to be replayed without rerunning the whole
autonomous fix gate, use
`kitsoki gitops issue-closeout --run-dir <run_dir> --ticket-repo <owner/repo>`;
it reads the persisted `gh_agent.drained_jobs` from `findings.json` and performs
the fixed-in comment plus close through the same native ticket provider.
For older filed issues or cross-run summaries whose current state is not already
in `findings.json`, run the story intent
`stats refresh_issue_state=true ticket_repo=<owner/repo>` or the CLI
`python3 tools/product-journey/run.py --stats --refresh-issue-state --ticket-repo <owner/repo>`.
That refreshes the issue-state cache through
`kitsoki gitops issue-state-cache` / `host.gh.ticket`, not raw `gh`.

The lower-level `file_findings ticket_repo=<owner/repo> [mode=dry-run]` story
intent defaults to dry-run and remains useful for previewing filing in
isolation, but it is not the canonical full-loop gate. Real filing through that
story intent is debug-only and requires `mode=file debug_file=true`; normal
issue filing and fixing should use `autonomous_watchdog` followed by
`autonomous_fix`.
Once filing or autonomous fixing has been requested, the `findings-filed`
review check fails and `--validate-run` errors while any credible issue finding
remains unfiled, so "issues filed for all credible findings" stays part of
bundle readiness.

To prove the next hop without GitHub credentials or LLM cost:

```sh
python3 tools/product-journey/run.py --native-ghagent-smoke --json-output
python3 tools/product-journey/run.py --autonomous-fix-smoke --json-output
python3 tools/product-journey/run.py --persona-autofix-smoke --json-output
python3 tools/product-journey/run.py --autonomous-marathon-smoke --json-output
python3 tools/product-journey/run.py --autonomous-marathon-smoke \
  --autonomous-marathon-smoke-repeats 2 --json-output
python3 tools/product-journey/run.py --validate-marathon-smoke-ledger \
  --marathon-smoke-ledger .artifacts/product-journey/marathon-smokes/<id>/autonomous-marathon-smoke.json \
  --min-marathon-smoke-cycles 2 \
  --json-output
```

The native smoke creates a temporary product-journey bundle with a filed issue,
enqueues it through native `kitsoki gh-agent enqueue`, drains it through native
`kitsoki gh-agent drain` in replay mode with GitHub comments disabled, and
checks that fix artifacts and run URLs are persisted back into the bundle for
review. The autonomous smoke runs the full no-LLM envelope with a fake `kitsoki`
CLI behind the same story-owned contract: persona findings file as issues,
gh-agent fixes queue and drain, review artifacts refresh, and validation must
pass. The `persona_autofix_smoke` story intent / persona-autofix runner smoke
starts from a persona replay bundle with local proof artifacts and an observed
issue finding, then proves that bundle enters the native `kitsoki gitops
autonomous-fix` gate and publishes the filed issue, gh-agent run URL, fix
evidence, and independent verification. The `autonomous_marathon_smoke` story
intent adds the standing-loop shell around that path: it creates a scoped
persona-QA run, journals replayed driver evidence, files and fixes one
credible issue per core scenario for every active persona, records integration
landing proof for every gh-agent fix, and writes a retained JSON/Markdown
ledger under `.artifacts/product-journey/marathon-smokes/<id>/`. Pass
`--autonomous-marathon-smoke-repeats <n>` to require multiple complete
active-persona cycles; the retained ledger records `cycle_count`, one run entry
per persona per cycle, and issue/fix/landing totals for
`cycles x personas x scenarios`. The retained-ledger validator accepts
`--min-marathon-smoke-cycles <n>` so an operator or story gate can reject a
single-cycle ledger when the proof requires many complete cycles. It also routes
observed weakness findings into `weakness-routes.json` / `weakness-routes.md`
for `stories/prd` and derives found/filed/fixed stats from issue state so
manual stats are not part of the loop. Use
`--validate-marathon-smoke-ledger` to re-check that retained ledger later; it
fails closed if the JSON/Markdown ledger, per-persona run bundle, cycle
coverage, report, deck, review, validation, filed/fixed counts, or landing proof
no longer line up.
For live-budgeted pending marathons, run `capture_preflight` first; the story
fails closed before creating the driver handoff if capture preflight has not
passed. Replay marathons remain no-LLM and do not require this live-capture
preflight.
`--autonomous-driver-mode replay` is the no-LLM fully story-owned autonomous
mode. Explicit `--autonomous-driver-mode record` or `live` returns
`autonomous_marathon_ready_for_driver`; the product-journey story auto-queues
its `dispatch_driver` intent, dispatches the reusable driver through
`host.agent.task`, and re-enters the native finalizer. Automated flow tests
stub that task by id; they must not call a real model or silently become fake
proof passes.
Live-budgeted pending marathons must also provide `ticket_repo` and
`gh_agent_public_base_url` before handoff, so live capture cannot begin for a
run whose downstream autonomous filing, gh-agent repair, close-out, and
review-link gates cannot complete. The runner also checks
`<gh_agent_public_base_url>/healthz` and refuses `ready_for_driver` unless it
returns HTTP 200 with body `ok`; it then checks
`<gh_agent_public_base_url>/api/ready` and refuses handoff unless the hosted
agent reports `status=ready`, the same ticket repo, and an enabled drain loop.

Do not bypass this with raw `gh` commands. Product-journey issue filing and
autonomous fixes are intentionally routed through Kitsoki's native
gitops/gh-agent surfaces so artifact upload, issue metadata, queued repair
state, fix-run evidence, and review gates stay coupled.
The native `kitsoki gitops autonomous-fix` gate also runs the autonomous
watchdog before filing issues, then checks the hosted gh-agent `/healthz` and
`/api/ready` endpoints; readiness must match the ticket repo and report an
enabled drain loop, or the gate returns `autonomous_fix_invalid` with filing
still `not_run`.
Passing watchdog and health/readiness summaries are also written into the story
result and `autonomous-fix-report.md` so a reviewer can see both the standing
loop state and which hosted worker was trusted before issue filing and repair
started.

For a no-LLM dogfood/demo bundle with representative evidence and findings:

```sh
python3 tools/product-journey/run.py --seed-demo-evidence \
  --run-dir .artifacts/product-journey/<run-id>
```

This is not a substitute for real visual MCP capture, but it proves the report
aggregation, quality-gate accounting, driver-journal coverage, and Slidey deck
shape before a live run. It marks every required evidence slot captured with
deterministic placeholder paths and records one replay-mode driver event per
scenario, so review gates can exercise the full artifact contract while
validation still warns that those local paths do not resolve and review warns
that evidence is demo-only.

Review whether a bundle is ready for human discussion:

```sh
python3 tools/product-journey/run.py --review-run \
  --run-dir .artifacts/product-journey/<run-id>
```

The review writes `review.json`, updates `metrics.json`, and adds a readiness
scene to `deck.slidey.json`. Hard failures mean the bundle is still skeletal;
warnings identify useful evidence quality improvements, such as missing key
interaction video. A bundle is not `ready` unless the deck has playback media
or an explicit blocked-scenario finding explains why playback evidence could not
be captured.

Prepare the reusable driver handoff without spending live model calls:

```sh
python3 tools/product-journey/run.py --driver-handoff \
  --run-dir .artifacts/product-journey/<run-id>
```

Inside `stories/product-journey-qa/app.yaml`, submit `handoff` from a run to
refresh the same `driver-handoff.md/json` artifacts.

After review, run the read-only validator before treating the artifacts as a
stable contract for a live or cassette-backed run:

```sh
python3 tools/product-journey/run.py --validate-run \
  --run-dir .artifacts/product-journey/<run-id>
```

The validator checks required files, JSON shape, scenario/evidence/media
consistency, metrics freshness, review statuses, required review gate IDs, and
Slidey review scenes without rewriting the bundle. It recomputes review
pass/warn/fail counts from `review.checks` and compares them to `review.json`
and `metrics.json`, so stale summaries cannot pass as fresh review proof. If it
fails, run `--review-run` again after fixing or attaching the missing artifact.

For `gears-rust`, this prints the existing external-bakeoff readiness signal and
the local-only verification command. If you have a local checkout, it also
emits the exact environment-required command for validation:

```sh
BUGFIX_BAKEOFF_REPO=/path/to/checkout make gears-bakeoff
```

`postgresql` and `kubernetes` use local oracle scripts in
`tools/product-journey/checks/` so the runner can prove the real red@baseline /
green@fix split from the checked-out local repos.

Generate the deterministic report JSON, companion Slidey deck spec, and Markdown
review index:

```sh
python3 tools/product-journey/run.py \
  --mode report \
  --generated-at 2026-06-26T00:00:00Z
```

By default this writes:

- `.artifacts/product-journey-eval/<generated-at>/report.json`
- `.artifacts/product-journey-eval/<generated-at>/deck.slidey.json`
- `.artifacts/product-journey-eval/<generated-at>/report.md`

Use `--run-checks` only when you want to refresh local oracle evidence while
building the report. The default report uses the catalog's current validated
state and does not run expensive checks.

### Local product site for deterministic A/B testing

For all journey runs, use a local production build of the product site so no remote state is shared:

```sh
make web
GOCACHE="${KITSOKI_GOCACHE:-/private/tmp/kitsoki-gocache}" go run ./cmd/kitsoki web --addr 127.0.0.1:7777
```

This stages the production bundle locally and then serves it from a reproducible
local endpoint (`http://127.0.0.1:7777`) for every run against docs,
onboarding, and bugfix surfaces.

## Shared completion-state contract (arena)

`review.json`'s review gate is native to product-journey, but arena
(`tools/arena`) needs one job-agnostic grade it can score without knowing that
artifact's shape. `tools/persona_qa/completion.py` is the bridge:
`from_product_journey_report(report)` / `load_product_journey_run(run_dir)` turn
a run's `review.json` + `scenario-outcomes.json` + `driver-handoff.json` into a
`CompletionState` conforming to
[`schemas/completion-state.schema.json`](../../schemas/completion-state.schema.json)
— the same `verdict`/`health`/`metrics`/`evidence_refs` contract arena's bugfix
plugin scores bugfix cells from. The mapping: `ready` (+ valid/unknown
validation) → `solved`; `needs_evidence` with some passing/evidence signal →
`partial`; all checks failed with no evidence or a blocking scenario →
`failed`/`blocked`; a harness/validation error → `blocked` + `health:
infra:harness`. See `tools/persona_qa/tests/test_completion.py` and
`tools/arena/tests/test_completion_state.py` for the deterministic, no-LLM
coverage of this mapping.

## Files

- `catalog.json` — first-pass project + perspective registry.
- `github-targets.json` — 10 GitHub candidate targets for natural-usage
  journey sweeps.
- `personas.json` — reusable personas for deterministic journey assignment.
  Curated personas carry a `persona_lens` object (`starting_surface`,
  `first_question`, `evidence_emphasis`, `escalation_trigger`, `finding_bias`)
  read by `persona_lens()`; personas without it (today, every mined persona)
  fall back to a lens synthesized from `surface_preference`/`risk_focus`.
- `scenarios.json` — reusable scenario/task definitions with required MCP tools,
  expected evidence, and success criteria. Scenarios may declare an optional
  `transports` object (`allowed`, `required`, and per-transport `overrides` of
  `required_mcp`/`evidence`) consumed by `--emit-run --transport`; scenarios
  without it (today, every mined scenario) get an implicit contract derived
  from `required_mcp` by `default_scenario_transports()`.
- `schema.json` — current artifact and stage contract.
- `run.py` — entrypoint script used by the journey orchestrator.

## Output discipline

Smoke iterations pile up hundreds of timestamped run dirs under
`.artifacts/product-journey/`. Prune them with a retention policy that keeps any
`*-final` curated keeper plus the newest `--keep` runs and never touches the
`matrices/`, `dogfood/`, or `target-proofs/` subtrees. Dry-run by default:

```sh
python3 tools/product-journey/run.py --prune-runs --keep 12          # preview
python3 tools/product-journey/run.py --prune-runs --keep 12 --apply  # delete
```

- `.context/product-journey-runlog.md` stores the run log in the worktree root.
- `docs/decks/product-journey-eval.slidey.json` stores the hand-refined,
  proof-ready narrative reference. Report generation links to it and does not
  overwrite it.
- `.artifacts/product-journey-eval/<generated-at>/deck.slidey.json` is the
  generated companion deck for a specific structured report run.
