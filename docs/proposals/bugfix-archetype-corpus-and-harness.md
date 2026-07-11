# Bugfix-archetype corpus growth + config-driven mechanism harness

**Status:** Draft v1. **Slice 1 (config-driven target story) landed** — see
"What exists today" and Tasks below. Everything else (corpus path choice,
Slices 2-5) is still open, pending Brad's call on BugSwarm vs. GitBug-Java
(see "Corpus research").
**Amends:** `docs/research/cost-efficiency-benchmark.md` (FROZEN M0 protocol) — per
that document's own rule ("Any change after this freeze requires a new,
explicitly versioned amendment section, not a silent edit"), this proposal is
the amendment candidate for its `bugfix_test_repair` archetype corpus (§5) and
its round protocol (§7). It does not touch H1–H4, the metric definitions, or
any other archetype.
**Builds on:** `docs/proposals/arena-comparison-runner.md` (the harness this
extends), `docs/goals/generalized-usage/decomposition.yaml` walls WB.1–WB.4.
**Related, not superseded:** `.context/2026-07-11-gx10-small-model-study-final-report.md`
— a separate study (GX10 hardware evaluation) that hand-rolled a weaker,
one-off version of the same run→score→diagnose→patch loop this protocol
already formalizes. Its failure taxonomy (F1–F7, M1) is reusable evidence,
folded in below; its GX10-hardware-purchase question is out of scope here.

## Why

Brad asked to grow the bugfix-optimization work using an open bug corpus and a
harness flexible enough that swapping corpus or target story is a config
change, not a code change. Two things are true at once:

1. **This repo already has almost exactly that harness**, mid-flight. `tools/arena/`
   (`docs/proposals/arena-comparison-runner.md`) plus the frozen
   `docs/research/cost-efficiency-benchmark.md` protocol (WB.0–WB.5 in
   `docs/goals/generalized-usage/decomposition.yaml`) already: mined
   `bugfix_test_repair` as the #1 evidence-based archetype
   (`tools/arena/corpus/archetypes.yaml:34-59`, 46 sessions / 35 user hits —
   the strongest signal of any candidate archetype), froze a corpus contract
   (`tools/arena/corpus/cost-bench.manifest.yaml`, `tools/arena/corpus/sources.yaml`),
   built a mechanism/treatment axis (`tools/arena/arena/treatments/registry.py`)
   that already includes a codeact-style mechanism
   (`kitsoki-mcp-codeact`, threading `implementation_mode` straight into
   `stories/bugfix/rooms/implementing.yaml:207,234`'s existing
   `host.agent.task` vs `host.agent.codeact` switch), and **already ran a real,
   paid round** (`tools/arena/results/round-1/`, ~$8.15, 8 live cells, real
   traces).
2. **Round 1's own result is why this needs work.** Win-rate came back 0.5 for
   *both* arms (kitsoki and single-briefed) on a 4-task, 1-archetype sample —
   not yet a clean signal either way (`tools/arena/results/round-1/training-packet.md:59`).
   The corpus that produced that result is 2 query-string tasks + 2
   kitsoki-self tasks — small, and half of it is self-sourced, which is
   exactly threat-to-validity #1 the frozen protocol itself pre-registered
   ("tasks mined from *our* usage of *our* repo could favor kitsoki",
   `docs/research/cost-efficiency-benchmark.md:351-353`).

So the actual ask, reframed against what's already committed: **grow the
`bugfix_test_repair` archetype's corpus with real, verified, externally-sourced
cases from a genuinely open corpus, and close the two concrete harness gaps
that stop that corpus (and any future target story) from being a config
change.** This is additive to round 1, not a restart — the existing 4 training
tasks, their results, and the round-1 training packet stay as committed
history.

## What exists today

| Piece | State | Evidence |
|---|---|---|
| Archetype selection | Frozen, evidence-based | `tools/arena/corpus/archetypes.yaml` (mined from 79 real transcripts, WB.1) |
| `bugfix_test_repair` corpus | Frozen, 20 training + held-out tasks over 10-OSS + kitsoki-self; **0 cases from any third-party bug-fix corpus** | `tools/arena/corpus/cost-bench.manifest.yaml`, `sources.yaml:6-23` |
| BugSwarm source | Registered, adapter-ready, **1 unverified seed case** | `tools/arena/corpus/{sources.yaml:25-69,bugswarm.seed.yaml}` |
| Harness (paired-task plugin) | Landed, proven live in round 1 | `tools/arena/arena/plugins/paired_task.py`, `tools/arena/results/round-1/` |
| Mechanism/treatment axis | Landed: `raw-codex` / `codex-codeact` / `kitsoki-mcp` / `kitsoki-mcp-codeact` | `tools/arena/arena/treatments/registry.py:13-44` |
| Target story for the `kitsoki`/`kitsoki-mcp-codeact` treatments | **Fixed this session (Slice 1)** — `--story` flag, defaults preserved | `tools/arena/lib/paired_task_runner.py:48,162,907,985`, `tools/arena/arena/plugins/paired_task.py:60`, `tools/bugfix-bakeoff/external/drive_cell.sh` (`--story` case arm + heredoc), `tools/arena/arena/plugins/bugfix.py:99-101` |
| Separate `bugfix` job-type plugin (simpler, no treatment axis) | Landed, unused by the WB protocol (round 1 ran through `paired-task`) | `tools/arena/arena/plugins/bugfix.py` |
| `implementation_mode` mechanism hook inside the story itself | Already lands on `host.agent.task` vs `host.agent.codeact` | `stories/bugfix/rooms/implementing.yaml:207-279` |

Three separate bugfix-driving paths exist in this repo today (worth naming so
nobody rebuilds one thinking it's the only one): (1) standalone
`tools/bugfix-bakeoff/external/{drive_cell.sh,escalate.sh}` — direct,
non-arena, model-ladder escalation, used for one-off cells like
`bug9-glm-5.2-kitsoki.json`; (2) arena's `bugfix` job-type plugin — wraps (1)
for containerized/placed sweeps, proven on query-string, no treatment axis;
(3) arena's `paired-task` job-type plugin — the one WB.2–WB.4 actually use.
This proposal's harness fixes target (3) as primary since that's both the
frozen-protocol path and the path any new corpus adapter would generate specs
for; the story-config fix also applies to (1)/(2) since they duplicate the
exact same hardcoded constant.

## Corpus research: an open decision, not a settled one

The corpus research this session turned up a real tension worth Brad's
explicit call rather than a silent pick. Full comparison researched
(SWE-bench + Lite/Verified/Multimodal/Live, SWE-gym, Multi-SWE-bench,
BugSwarm, BugsInPy, Defects4J, GitBug-Java/Actions):

| Corpus | Cases / lang | License | Diff size | Contamination risk | Integration effort here |
|---|---|---|---|---|---|
| **BugSwarm** | 4,388 (2,422 Java/1,966 Python), live-verified 2026-07-05 | BSD-3 on toolset only; artifact *contents* have no blanket redistribution grant | Favorable: 31% ≤5 lines changed, 54% ≤20 changes | Under-studied for LLM memorization, **but** an independent critique found only ~3.6% of the original pool are clean single-fault cases (rest is CI/env flakiness), disputed but unresolved by the dataset's own authors | **Lowest** — adapter/verifier/spec-generator already exist and are tested (`tools/arena/scripts/bugswarm_*.py`), already the source Brad referenced, already has a (currently-empty) `sources.yaml` entry |
| **GitBug-Java** | 188, Java only | MIT | Best-in-class: mean 24.6 lines / 1.41 files | **Lowest** — deliberately mined from a window chosen to reduce pretraining overlap; independently endorsed (not just by its own authors) as the lower-risk choice vs. Defects4J/BugsInPy | **Highest** — no adapter exists yet; would need a new `gitbug_java_to_arena.py` mirroring the BugSwarm scripts' shape |
| **BugsInPy** | ~493-501, Python only | Inherits per-project license (no blanket grant) | Not separately measured | Moderate — 66% patch-reproduction accuracy in an independent memorization study | High — same as GitBug-Java, no adapter exists |
| **Defects4J** | 854, Java only | MIT | Best mechanical fit: median 4 lines, 92% single-file | **Highest of any candidate** — 80% of its repos are inside a major pretraining corpus, 82% reproduction accuracy; the independent study measuring this states outright it "may not be a reliable dataset for evaluating current LLMs" | Not evaluated further given the contamination finding |
| **SWE-bench (Verified)** | 500, Python only | MIT-family | Poor fit: host repos average ~438K LOC | **Confirmed contaminated** — OpenAI itself stopped reporting Verified scores 2026-02-23 after finding 59.4% of failed tasks had flawed tests and frontier models could reproduce gold patches verbatim from the task ID alone | Ruled out |

Two defensible paths:

- **A — BugSwarm, filtered.** Cheapest path to real data (existing pipeline,
  just needs a curated + verified case list), consistent with what's already
  wired into `sources.yaml` and what Brad referenced starting this
  conversation. Requires an explicit filter (language, diff size, quality
  classification, live RED/GREEN re-verification) to manage the ~3.6%
  suitability critique — i.e., don't trust metadata alone, verify every case
  before it's eligible, the same discipline §5.2's feasibility triage already
  applies to the OSS corpus.
- **B — GitBug-Java, built fresh.** Stronger contamination story and a cleaner
  license, at the cost of a new adapter (real but bounded work — the existing
  BugSwarm scripts are the template: convert → verify → apply-verification →
  spec-generate, ~4 small offline Python scripts). 188 cases is enough for a
  repeatable study; MIT means no license caveat in the write-up.

This proposal's harness fixes (config-driven story, below) are corpus-agnostic
— either path benefits identically, so the harness work isn't blocked on this
choice. The choice only gates which corpus-growth slice (2/3 below) actually
runs. **Recommendation if forced to pick one to start: A (BugSwarm), filtered
to Java + ≤5-files-changed + `classification` excluding flaky/broken** —
because it's real progress this week on infrastructure that already exists
and that Brad has already invested in, with the contamination/suitability
risk mitigated (not eliminated) by mandatory live re-verification of every
case rather than trusting BugSwarm's own metadata. GitBug-Java stays a
documented, ready-to-build follow-up (Path B below) if the study later wants
a second, more rigorously-defensible corpus arm for the same archetype —
`sources.yaml`'s design already treats corpora as independent, addable
sources, so this isn't an either/or fork in the data model, only in which
gets built first.

## Proposed design

### 1. Config-driven target story — DONE

Both hardcoded call sites were the *same* pattern — a literal story path
baked into a natural-language orchestrator prompt, not a CLI/API parameter:
`tools/bugfix-bakeoff/external/drive_cell.sh`'s heredoc prompt (interpolating
`$profile`/`$cand`/`$bug` the same way) and
`tools/arena/lib/paired_task_runner.py`'s `BENCH_BUGFIX_STORY` constant used
in `build_kitsoki_prompt`.

Shipped: a `--story` argument on both `drive_cell.sh` (mirroring its existing
`--project`/`--bug`/`--candidate` flags, default
`stories/bench-bugfix/app.yaml`) and `paired_task_runner.py` (default falls
back to `BENCH_BUGFIX_STORY`, read via `getattr` so hand-built test
`Namespace`s without the attribute don't break). Threaded from
`cell.target.meta.get("story")` (falling back to `cell.variant.meta.get("story")`
for a per-variant override) in both `arena/plugins/paired_task.py`'s and
`arena/plugins/bugfix.py`'s `drive_command` — the identical pattern
`implementation_mode`/`worker_profile`/`capability_preset` already use.
`paired_task_runner.py` also stamps `metrics["story_path"]` so every cell
result records which story it actually drove (matching §6.3's determinism
checklist: "prompt/story bundle version — all stamped into every cell JSON").
No spec in `tools/arena/specs/` needed to change — every existing spec is
byte-identical in behavior (proven by the new `--story` regression checks
below, which assert `"--story" not in argv` when no story meta is set).

Tests: `tools/arena/tests/test_paired_task_codeact.py` (default preserved +
override reaches `build_kitsoki_prompt`'s `story_path:` line) and
`tools/arena/tests/test_arena_skeleton.py` (default preserved + override
reaches `bugfix.py`'s `drive_command` argv, plus a static check that
`drive_cell.sh` no longer hardcodes the literal). Full `tools/arena/tests/test_*.py`
suite run: 28/29 pass; the one failure (`test_glm52_report_gate.py`) is
pre-existing on the unmodified workspace base (confirmed via `git stash`) —
it's the GLM-5.2/BugSwarm report correctly refusing to call itself
publishable while its claims are still `pending`, which is exactly the gap
Slices 2-4 close.

### 2. Corpus growth (Docker-gated — this sandbox can't run it)

`docker version` did not return in this session (hung past 60s with no
daemon response), so the live-verification half of this slice is an
operator-run step. Path A (BugSwarm) is already fully scripted end to end
(`tools/arena/corpus/README.md`'s BugSwarm section):

```bash
# 1. Source a candidate artifact list from BugSwarm's public metadata,
#    filtered per the criteria above — every entry must trace to a real
#    BugSwarm artifact tag, none invented.
# 2. Convert (offline, no Docker/LLM):
python3 tools/arena/scripts/bugswarm_to_arena.py \
    --in .artifacts/bugswarm/candidates.json --out .artifacts/bugswarm/source.yaml
# 3. Verify RED/GREEN (Docker-gated, operator machine):
python3 tools/arena/scripts/bugswarm_verify_source.py \
    --source .artifacts/bugswarm/source.yaml \
    --out .artifacts/bugswarm/verify.json --execute
# 4. Apply verification -> only verified_red=true, verified_green=true survive:
python3 tools/arena/scripts/bugswarm_apply_verification.py \
    --source .artifacts/bugswarm/source.yaml \
    --verification .artifacts/bugswarm/verify.json \
    --out tools/arena/corpus/bugswarm.verified.yaml
```

The output is committed as a **new, separately-versioned file**
(`tools/arena/corpus/bugswarm.verified.yaml`), not merged into the existing
frozen `cost-bench.manifest.yaml` — consistent with the frozen protocol's
own no-silent-edit rule. `sources.yaml`'s existing `bugswarm` entry
(`status: adapter-ready`) flips to `status: active` once this lands with a
committed verification report.

Path B (GitBug-Java), if chosen instead or in addition: mirror the four
BugSwarm scripts' shape against GitBug-Java's own case format (a Zenodo-
archived Docker-image-per-case list, MIT-licensed) — `gitbug_java_to_arena.py`
/ `_verify_source.py` / `_apply_verification.py` / `_to_arena_spec.py`, same
`sources.yaml` pattern, new `id: gitbug-java` entry alongside the existing
`bugswarm` one.

### 3. Generalize the GX10 failure taxonomy into reusable diagnostics

GX10's F1 (submit-without-write), F3 (wrong cost basis), F5 (refine-ring
spend with zero cache hits), and F6 (hand-written-YAML parser fragility) are
not GX10-specific — they're generic risks for *any* mechanism variant on
*any* story. `stories/bugfix/rooms/implementing.yaml:1-13`'s own file header
documents the story already having hit and fixed an F1-shaped bug once
(silent no-op implementer). Rather than re-derive this taxonomy next time,
fold it into `arena/plugins/paired_task.py`'s scoring as optional generic
checks over the completion-state / trace (cache-hit ratio on refine cycles,
cost-basis price-table version stamped per §6.3's own determinism checklist,
artifact-mtime-vs-trace-start check mirroring the GX10 fix's
`RequireArtifact` logic). Scoped as a stretch goal below — valuable, not
blocking the corpus/story work.

## Non-goals

- Not re-litigating GX10's Stage 4 (on-device GB10 hardware purchase
  question) — orthogonal study, orthogonal decision.
- Not editing H1–H4, the metric definitions, or the stop rule in
  `docs/research/cost-efficiency-benchmark.md` — frozen, unchanged.
- Not retroactively merging any new corpus cases into the already-scored
  `cost-bench.manifest.yaml` training split — additive corpus, own file.
- Not building a fully-autonomous "propose the next mechanism" loop. The
  training pass's *patch* step (§7, `docs/research/cost-efficiency-benchmark.md`)
  stays human/agent-judgment-driven; this proposal makes the run → score →
  compare loop cheap and config-driven, not the invention step itself.
- Not replacing round 1's recommended next step (re-run bug9/bug12 under the
  fixed Docker image; widen to the other 3 archetypes,
  `tools/arena/results/round-1/training-packet.md:133-151`) — this is
  additive to that recommendation, not a substitute for it. Both can run in
  parallel; they don't compete for the same corpus slot.

## Tasks

- [x] **Slice 1 — config-driven story.** `--story` on `drive_cell.sh` +
      `paired_task_runner.py`; threaded through `paired_task.py`/`bugfix.py`
      `drive_command`; no-LLM tests proving the default is byte-identical to
      today and an override reaches the generated prompt/argv.
- [ ] **Slice 2 — corpus candidate list + filter** (path A or B per Brad's
      call, see "Corpus research"). Source and document a candidate case
      list against the relevant filter; convert via the corpus's adapter
      script; commit `.artifacts/<corpus>/source.yaml` (unverified) as the
      operator handoff artifact.
- [ ] **Slice 3 — operator verification run packet.** A `glm52_gap_plan.py`-style
      generated packet with the exact verify/apply-verification commands, so
      Brad (or a Docker-equipped workspace) can run verification and land the
      corpus's `*.verified.yaml` without reconstructing the pipeline by hand.
- [ ] **Slice 4 — live round packet.** Generate the paired-task spec plus the
      exact no-spend `arena plan`/arm commands and the gated `--live`
      commands for a corpus-backed `bugfix_test_repair` round, mirroring
      `glm52_gap_plan.py`'s pattern.
- [ ] **Slice 5 (stretch)** — generalized F1/F3/F5/F6-shaped diagnostics in
      `paired_task.py` scoring.
- [ ] Migrate the shipped pieces into `docs/research/cost-efficiency-benchmark.md`
      as a dated amendment section once Slice 2-4 land real verified data;
      trim this proposal to whatever's still in design; delete when Slice 5
      either lands or is explicitly deferred.

## Open questions

- **Corpus path A vs B vs both** (see "Corpus research") — this proposal
  defaults to starting Path A (BugSwarm, filtered) as the cheaper path to
  real data this week, with Path B (GitBug-Java) as a documented follow-up
  for a second, lower-contamination-risk corpus arm — pending Brad's
  confirmation.
- Round 1's own recommendation was to widen to the other 3 archetypes before
  drawing any H1-H4 verdict; this proposal instead deepens `bugfix_test_repair`
  specifically per Brad's ask. Both are legitimate next investments and
  aren't mutually exclusive — worth Brad's explicit call on relative priority
  for the next live-spend round.
- How many corpus cases to target for the first verified slice (proposed:
  8-12, matching the existing archetype's current scale) — open to
  adjustment once the candidate list is actually sourced and some artifacts
  fail verification.
- Whether the Docker-gated verification step (Slice 3) runs on Brad's machine
  interactively, or should be handed to a Docker-equipped dev-workspace/VM per
  `tools/arena/README.md`'s existing VM-placement capability.
