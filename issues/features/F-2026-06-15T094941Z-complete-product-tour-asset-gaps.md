---
title: "Complete Product Tour: close the demo-asset gaps that block the differentiating beats"
target: kitsoki
feature: true
status: open
severity: P1
assignee: ""
component: features/demo
filed_at: "2026-06-15T09:49:41Z"
kitsoki_rev: 55aa25a
trace_ref: ""
proposal: ".context/complete-product-tour-proposal.md"
review: ".context/complete-product-tour-skeptic-review.md"
external: {}
---

> **Note:** `issues/` is a deprecated, frozen archive — new tickets are meant to
> live as GitHub Issues on `constructorfabric/Kitsoki`. Filed here on explicit
> request; mirror to GitHub (`kitsoki bug create --github` / migrate) if this
> should be tracked live.

## Body

Two adversarial reviews of the **Complete Product Tour** proposal
(`.context/complete-product-tour-proposal.md`, full critique in
`.context/complete-product-tour-skeptic-review.md`) verified its gap analysis
against source. The proposal's differentiating beats — the ones that defeat a
"kitsoki is just a YAML wrapper over things my coding agent already does"
skeptic — depend on **demo assets that do not exist or are broken today.** This
ticket enumerates the missing/broken features so the persuasive cut is buildable.

Severity rationale: the four irrefutable on-screen proofs (host-rejects-the-model,
zero-oracle-call routing, live FSM self-edit, film-is-a-CI-test) each have **no
analog in a general coding agent**, and three of them are currently un-filmable.
That makes these P1 for the demo, not cosmetic.

---

### Missing / broken assets (verified against source on 2026-06-15)

**G1 — `features/meta-mode.yaml` does not exist (P1).**
`tools/runstatus/tests/playwright/meta-mode.spec.ts` (~381 lines, built
end-to-end, hot-reloads `prd/flows/happy_path.yaml`) has **no feature manifest**,
so the single most viscerally-differentiating beat — a running FSM editing its
own YAML and reloading, with the edit recorded as a `story.changed` trace event
(`docs/tracing/trace-format.md:139–143`) — has zero promo presence.
*Fix:* NEW `features/meta-mode.yaml` wrapping the built spec; record + QA.

**G2 — `features/harness-picker.yaml` does not exist (P1).**
`harness-picker-video.spec.ts` is built and byte-deterministic but uncatalogued,
so live provider/model/effort switching with per-call provenance
(`oracle.call.complete.meta`) never reaches the promo grid.
*Fix:* NEW `features/harness-picker.yaml`. Open question: it films on
`testdata/apps/oracle_probe` (synthetic) — consider a first-party story that
declares `harness_profiles` so the differentiator isn't shown on a throwaway.

**G3 — `WorldDiffViewer.vue` is orphaned + `world-diff` step exists in no
manifest (P1, real frontend work — NOT a caption).**
`tools/runstatus/src/components/WorldDiffViewer.vue` renders Before/Diff/After
but is **imported nowhere** (grep-verified, zero importers); the `world-diff`
step id appears in **no** `features/*.yaml`. The proposal files this under
"extend" / "only incidental today" — it is actually unwired. The "how the turn
mutated the world" beat (a no-agent-log-analog proof) requires wiring the
component to a `world.update`/`machine.transition` event first, then adding the
manifest step.
*Fix:* wire `WorldDiffViewer` into the observer detail surface; add a `world-diff`
step to `features/trace-features.yaml`.

**G4 — `multi-story` recording is broken; spec↔flow desync (P1, blocks
persistence beat).**
On-disk `.artifacts/multi-story/ERROR.txt` confirms 34× "Expected: brief,
Received: clarifying". Root cause verified: `multi-story.spec.ts:293` sends
`submit_answers` with answer text in **one** turn, but
`stories/prd/flows/happy_path.yaml:104–122` requires **two** turns —
`answer{text}` (stays clarifying, `answered_count→2`) then `submit_answers{}`
with empty slots → `brief`. The spec skips the `answer` turn.
*Fix:* correct the **spec** to the intentional two-turn flow (per
`stories/AGENTS.md`: never paper over by loosening the flow). Re-record; capture
reload-survival + active-sessions roll-up.

**G5 — `aa-diff` has no real cross-operator drift to show (P2).**
`TranscriptDiff.vue:18–26` honestly renders "No live run to compare — replay is
byte-identical." On screen this reads as *empty feature*. The differentiating
"two operators, same prompt, here's the drift" beat needs a NEW two-cassette
fixture.
*Fix:* author a two-cassette fixture so `aa-diff` renders a real verdict diff;
pull from Phase 4 into v1 if the diff beat stays in the cut.

---

### Under-exploited capabilities that already exist (no code work, framing only)

These are NOT missing — they ship today and are the sharpest weapons, but the
proposal buries them. Captured here so the demo work surfaces them:

- **U1 — Zero-oracle-call routing / "the LLM was never called."** `turn.start`
  carries `direct:true` / `routed_by: deterministic|semantic|turncache`
  (`trace-format.md:90–99`), surfaced by `RoutingDetail.vue`'s `routed_by` badge.
  ~78% of turns never call the model (`README.md:44–50`). This defeats the
  "structured output already does this" conflation; the proposal exiles it to a
  Phase-4 maybe.
- **U2 — Host rejects the model mid-call (decide guardrail arc).** The host
  injects synthetic `_kitsoki` lines for validator-rejection → nudge → re-submit
  → accept (`trace-format.md:300–306`), visible via `aa-decide-guardrail` /
  `aa-nudge` in `features/agent-actions.yaml`. No coding agent surfaces a host
  overruling the model. Buried at bullet 5/6 of `pt-actions`.
- **U3 — operator-ask exists because headless `AskUserQuestion` silently
  auto-resolves *empty*** (`docs/architecture/operator-ask.md:11–20`; hard-denied
  at `internal/host/agents.go:392–406`). The differentiator is the silent-failure
  it routes around, not "the agent asks a question."
- **U4 — `trace-category-chips` is `kind: explain` today** — convert to action
  (click a chip) for the `pt-inside` filter beat. Small, correctly-scoped extend.
- **U5 — host allow-list caption** — the allow-list is real and load-bearing
  (`internal/host/agents.go:392–406`, `internal/host/host.go:142–157`) but
  `HookDetail.vue` has no security-boundary caption today; add one in
  `features/story-editor.yaml`.

---

### Suggested order (cheapest gap closures first)

1. G4 repair (unblocks the persistence beat; pure spec fix).
2. G1, G2 (promote built specs — manifest + record only).
3. G3 (real frontend wiring — largest single item).
4. U1–U5 framing/extends folded into the relevant section re-records.
5. G5 (new fixture) — only if the cross-operator diff beat stays in v1.

## Source

Filed from the Complete Product Tour proposal review. The proposal carries the
full per-section production plan; the skeptic review carries the
verified-against-source evidence (file:line refs) for every gap above. Read both
before starting.
