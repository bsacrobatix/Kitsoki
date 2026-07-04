# tools/swarm — swarm tier 1 (scripted replay users)

Puts **N>=24 concurrent, scripted, no-LLM Playwright browser contexts** on
**ONE shared `kitsoki web --flow ...` server**, to soak the multi-session
registry (`cmd/kitsoki/registry.go`) the way real concurrent users would —
something nothing in the repo did before this change (see
`docs/goals/ui-qa-scale/plan.md` and `decomposition.yaml`'s `swarm-tier1`
entry for the full design rationale).

## What's here

- `personas.ts` — loads the shared `tools/product-journey/personas.json`
  catalogue and derives the one behavioral lens tier 1 uses per persona
  (`watchesTrace`).
- `journey.ts` — `openUserSession` (mint + open, phase 1) and
  `driveUserJourney` (the scripted clicks, phase 2) drive one user's page
  through the PRD story's `happy_path` flow fixture, calling an injected
  `audit` callback once per distinct FSM state reached. `assertIsolated`
  is the per-user isolation check.
- `isolation.ts` — the strict, trace-based isolation assertion + marker
  helper (see its doc comment for why the session TRACE, not the rendered
  page, is the ground truth here).
- `audit.ts` — thin policy layer over `tools/runstatus`'s `ui-audit.ts`
  (severity gating + a `SharedAxeGate` so axe's expensive pass runs once per
  distinct page state, not once per user per state).
- `rss.ts` — informational server-RSS watcher (no cap yet — that's the
  separate `swarm-session-cap` change; this only records a baseline).
- `retry.ts` — bounded retry-with-backoff for RPC calls that hit a known
  transient store-busy error (see "Known findings" below).
- `concurrency.ts` — a tiny in-process concurrency limiter used to throttle
  how many users' INTERACTIVE steps run at once (see "Known findings").
- `results.ts` — the per-run results JSON schema + writer
  (`.artifacts/swarm/results-<run_id>.json`).

The actual Playwright spec that wires these together lives at
`tools/runstatus/tests/playwright/swarm-replay-users.spec.ts` (Playwright specs
must live under `tools/runstatus/tests/playwright/`; the driver library lives
here so it can be reused by later tiers/changes without duplicating it into
that directory).

## Why no dependencies (just a `{"type": "module"}` marker) here

`package.json` here is a **one-line ESM marker only** (`{"type": "module"}`) —
without it, Node's ESM loader walks up looking for the nearest `package.json`
to decide whether a `.js` file is CommonJS or ESM, and this repo has none at
the root, so it would default to CommonJS and reject `tools/swarm`'s named
exports. It carries no `dependencies`/`devDependencies` and there is no
`node_modules` here — the package is TypeScript-only, imported via relative
paths from
`tools/runstatus/tests/playwright/swarm-replay-users.spec.ts`, which is where
`npx playwright test` actually runs (`cd tools/runstatus && npx playwright
test tests/playwright/swarm-replay-users.spec.ts`). Node resolves each
module's bare-specifier imports (`@playwright/test`, `@axe-core/playwright`,
etc.) relative to **that module's own location**, not the entry file's — so a
file physically outside `tools/runstatus/` can still safely `import` a
`tools/runstatus` helper (e.g. `_helpers/server.ts`) at runtime, because that
helper's own imports resolve from its own directory chain, which DOES have
`tools/runstatus/node_modules`.

The one thing this repo would NOT let `tools/swarm/` do safely is import an
npm package directly (e.g. `@axe-core/playwright`) — `tools/swarm/` has no
`node_modules` of its own and isn't an ancestor of `tools/runstatus/node_modules`.
That's why the axe-core (`AxeBuilder`) and Playwright-runtime calls stay in the
spec file itself, while `tools/swarm/audit.ts` only takes a **type-only**
import of `ui-audit.ts`'s `RawFinding`/`Severity` (erased at build time, never
resolved at runtime) and accepts the actual finding arrays as plain data.

If a later tier needs real npm dependencies of its own, promote this to a real
workspace with its own `package.json` at that point — don't add one
speculatively now.

## What tier 1 asserts, per user

1. **Completion** — the scripted journey reaches "drafting", the last state
   this flow fixture can genuinely reach live (see "Known findings" —
   the flow's own `accept` step has a pre-existing gap, not a swarm concern).
2. **Strict session isolation** — a unique per-user marker (sent in the first
   free-text message) must never appear in another session's TRACE. See
   `isolation.ts` for why the trace, not the rendered page, is the ground
   truth.
3. **Zero console errors** — no `console.error` / uncaught page errors during
   the user's journey.
4. **UI audit clean** — `ui-audit.ts`'s `geometryProbe` (per user, per
   distinct state) plus a shared axe-core pass (once per distinct state,
   across the whole run — see `audit.ts`'s `SharedAxeGate` doc comment) report
   zero `error`-severity findings.

Server RSS is watched across the whole run (`rss.ts`) and recorded in the
results JSON — informational only; there is no cap or RSS assertion in this
tier (a future change, `swarm-session-cap`, adds the actual bound).

## Negative control

`swarm-replay-users.spec.ts` includes a dedicated test that seeds a real
cross-talk fault: two browser pages are deliberately pointed at the SAME
minted session id (simulating the exact registry bug this tier's isolation
gate exists to catch), and asserts `isolation.ts`'s `checkIsolation` correctly
flags the leak (found in the shared session's own trace). This proves the
gate can go red for the right reason without making the standing gate flaky —
the fault is seeded, not incidental.

## Known findings (out of this change's scope to fix — tools/swarm/** and the
spec file only; recorded here so they aren't lost)

1. **PRD `accept` never reaches `@exit:done` under `kitsoki web --flow`.**
   The `drafting` room's `on_enter` writes `world.prd_artifact` via the
   STUBBED `host.agent.task` (canned metadata only — no real file lands on
   disk), but the `accept` arc's real (unstubbed) `host.starlark.run`
   "publish" call reads that file back off disk and fails with `no draft at
   .artifacts/prd/example-cli/004-prd.md`. Reproduced with a bare RPC driver
   (no browser, no concurrency at all — a single `runstatus.session.submit`
   call), so it is not a swarm-scale artifact; the pre-existing
   `multi-story.spec.ts` drives this exact same `accept` step and hits the
   identical failure on this branch. `kitsoki test flows` passes the same
   fixture because `test_kind: flow`'s `starlark_inspect_cassette` masks the
   consequence in a way `kitsoki web --flow` does not. Tier 1's journey ends
   at "drafting" (the last state genuinely reachable live) rather than
   papering over this with a fake completion.
2. **Per-session `store.Open()` under a mint burst.** `cmd/kitsoki`'s runtime
   opens a fresh `*sql.DB` per minted session against the SAME shared SQLite
   file rather than sharing one connection/pool across the registry. A true
   burst of concurrent `session.new` calls can hit a transient
   `SQLITE_BUSY`/"database is locked" on that connection's very first pragma
   before its own `busy_timeout` is in effect. `retry.ts` absorbs this with a
   bounded client-side retry (realistic client behavior under overload), but
   the underlying design — one connection per session against one file — is
   a real scalability limitation worth its own fix.
3. **Turn-processing throughput under a true 24-way burst.** With all 24
   sessions minted and live, letting every user's remaining scripted clicks
   fire with no throttling causes some users' turns to queue for well over a
   minute; a live snapshot during a run showed the `kitsoki` server process
   near-idle on CPU while wall-clock latency still ballooned — consistent
   with SQLite single-writer lock contention (threads blocking, not
   spinning) rather than a CPU bottleneck. `concurrency.ts`'s
   `INTERACTIVE_CONCURRENCY` (default 3, override with
   `SWARM_INTERACTIVE_CONCURRENCY`) throttles the harness's OWN request
   concurrency to what this environment's turn-processing path can sustain —
   every session is still minted and its page kept open for the whole run,
   so "24 concurrently live" is never compromised, only how many are
   simultaneously mid-turn.

## Running it

```
cd tools/runstatus
npx playwright test tests/playwright/swarm-replay-users.spec.ts
```

Requires `make web && make embed-stories` once beforehand (the go:embed'd SPA
+ story catalogue) and a Playwright chromium install
(`npx playwright install chromium`). No LLM, no docker, no network egress.

Override the user count with `SWARM_USERS` (defaults to 24; the gate requires
>= 24) and the interactive-turn throttle with `SWARM_INTERACTIVE_CONCURRENCY`
(defaults to 3 — see "Known findings" #3 for why this isn't higher by
default).
