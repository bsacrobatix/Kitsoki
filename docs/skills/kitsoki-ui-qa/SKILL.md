---
name: kitsoki-ui-qa
description: Validate a UI demo video against a feature description and usage scenarios — the inverse of kitsoki-ui-demo. Extracts deterministic frames, has a read-only `claude` vision agent judge each scenario against cited frames, adversarially re-checks every pass, and emits a gated qa-report.md + verdict.json. Use when asked to QA / review / validate / sign off on a demo or walkthrough video, or to gate one in CI.
---

# Kitsoki UI demo QA

The **inverse** of [[kitsoki-ui-demo]]: that skill *produces* a demo video; this
one *validates* one. Given a **feature description**, a list of **usage
scenarios**, and the **video** (or pre-extracted frames), it decides — with
cited evidence — whether the demo actually demonstrates each scenario, and exits
non-zero if a required one doesn't, so it can gate a release.

> This is an **LLM-driven review tool by design** (it needs vision). It is *not*
> a no-LLM flow test and must never be wired into the automated test suite
> (CLAUDE.md, [[feedback_no_llm_tests]]). It uses the local `claude` CLI, so —
> like the engine's oracle — there's no API key and no per-call cost
> ([[project_oracle_uses_claude_cli]]). The two deterministic stages
> (`extract-frames.sh`, `report.sh`) are testable on their own without any LLM.

## Why it's reliable (read this first)

Video QA is unreliable when a model free-associates about UI it never saw. The
pipeline removes that failure mode structurally, not by hoping the model behaves:

1. **Deterministic evidence.** `extract-frames.sh` is pure ffmpeg — scene-change
   detection (the meaningful moments in a UI demo are state transitions) plus a
   periodic floor so static dwells aren't missed. Same video + flags → same
   frames + same `frames.json`. The frames are the *only* admissible evidence.
2. **Grounded verdicts.** Every `pass` MUST cite a frame filename and quote what
   is **literally visible**. A claim with no citable frame is `unsupported`
   (never silently `pass`); a frame that contradicts it is `fail`.
3. **Adversarial re-check.** A second `claude` pass plays skeptic and may **only
   downgrade** — it re-reads each cited frame and demotes any `pass` whose frame
   doesn't actually show the claim. (`--no-adversary` to skip.)
4. **Authoritative gate.** `report.sh` recomputes pass/fail from the per-scenario
   status in `verdict.json` (it does *not* trust the model's own `overall`) and
   sets the exit code.

## Prerequisites

`ffmpeg`, `jq`, and the `claude` CLI on PATH (all already present in this repo's
dev env). No `make build` needed — this consumes an existing video/frames.

## The loop

1. **Describe what the demo should show.** Copy the templates and edit:
   ```bash
   D=docs/skills/kitsoki-ui-qa
   cp $D/templates/feature.example.md   .context/qa-feature.md
   cp $D/templates/scenarios.example.yaml .context/qa-scenarios.yaml
   ```
   Scenarios are **observable claims** ("the state badge advances", "three story
   cards are listed") — not internal behaviour the camera can't see. Mark a
   scenario `required: false` to keep it non-blocking.

2. **Run the QA gate** (one shot: extract → contact sheet → review → report):
   ```bash
   docs/skills/kitsoki-ui-qa/scripts/qa.sh \
     .artifacts/multi-story/multi-story.mp4 \
     --feature   .context/qa-feature.md \
     --scenarios .context/qa-scenarios.yaml
   echo "gate exit: $?"          # 0 pass · 1 blocking failure · 2 pipeline error
   ```
   Artifacts land in `.artifacts/ui-qa/<video-stem>/`
   ([[feedback_artifacts_dir]]): `frames/`, `frames.json`, `contact-sheet.png`,
   `verdict.json`, `qa-report.md`.

3. **Prefer ground-truth frames when you have them.** The recorder already emits
   labeled per-scene `NN-<scene>.png`. Point `--frames` at that dir to QA those
   exact captures instead of re-extracting (highest fidelity, skips ffmpeg):
   ```bash
   docs/skills/kitsoki-ui-qa/scripts/qa.sh .artifacts/multi-story/multi-story.mp4 \
     --frames .artifacts/multi-story --feature .context/qa-feature.md \
     --scenarios .context/qa-scenarios.yaml --strict
   ```

4. **Read `qa-report.md`.** Per-scenario table with the cited evidence frame for
   each step. Open the cited PNGs (or `contact-sheet.png`) to confirm. If a
   scenario is `unsupported`, the demo didn't cover it — usually a gap in the
   *demo*, occasionally a vague scenario step to sharpen.

## The tools (`scripts/`)

| Script | Does | LLM? |
|---|---|---|
| `qa.sh <video> --feature F --scenarios S [--frames D] [--out D] [--model M] [--max-frames N] [--no-adversary] [--strict]` | One-shot wrapper; exit code is the gate | via review |
| `extract-frames.sh <video> <out-dir> [--scene TH] [--interval S] [--dedup MS] [--max N] [--width W]` | Deterministic scene-change + periodic-floor frames + `frames.json` | no |
| `qa-review.sh --frames D --feature F --scenarios S --out V [--model M] [--no-adversary]` | Read-only vision agent → evidence-cited `verdict.json` + adversarial re-check | **yes** |
| `report.sh <verdict.json> [--out report.md] [--strict]` | `verdict.json` → `qa-report.md`; recomputes the gate exit code | no |

Defaults: review model `claude-opus-4-8` (override `--model claude-sonnet-4-6`
for faster/cheaper); `--max-frames 48`; `--strict` makes every scenario blocking.

## verdict.json shape

```json
{ "overall":"pass|fail",
  "summary":{"scenarios_total":0,"passed":0,"failed":0,"unsupported":0},
  "frames_reviewed":["0001-0ms.png"],
  "scenarios":[
    {"id":"drive","title":"…","required":true,"status":"pass|fail|unsupported",
     "steps":[{"text":"…","status":"pass|fail|unsupported",
               "evidence":[{"frame":"0007-5200ms.png","observation":"<literal>"}],
               "confidence":0.0}]}]}
```

## Pointers

- The recorder this inverts: [[kitsoki-ui-demo]] (`docs/skills/kitsoki-ui-demo/`)
  — its `NN-<scene>.png` output is the ideal `--frames` input here, and its
  `contact-sheet.sh` is reused for the storyboard.
- Oracle = local `claude` CLI: `internal/host/oracle_runner.go`.

## Maintenance

Exposed to Claude Code via a symlink (skills under `docs/` aren't auto-discovered):

```
ln -s "$(pwd)/docs/skills/kitsoki-ui-qa" ~/.claude/skills/kitsoki-ui-qa
```
