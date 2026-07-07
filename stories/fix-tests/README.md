# fix-tests — auto-fix failing tests

A self-driving, operator-facing maintenance story. It runs the project's test
suite and, if anything fails, uses **claude (sonnet)** to fix the failures —
re-running the suite to confirm — then runs an adversarial read-only review gate
that rejects weakened tests or lost functionality before writing a Markdown
report.

```
make fix-tests
```

## What it does

```
idle ──start──▶ starting ──▶ running_executing ──green──▶ reviewing_executing
                       │ red
                       ▼
                 fixing_executing ──needs_decision──▶ blocked   (exit ≠0)
                       │ fixed
                       ▼
                 verifying_executing ──green──▶ reviewing_executing
                       │ red, budget left          ▲
                       └──────────▶ fixing_executing ┘
                       │ red, budget exhausted
                       ▼
                 done_exhausted                                 (exit ≠0)

                 reviewing_executing ──pass──▶ done_clean       (exit 0)
                        │ fail, budget left
                        └──────────▶ fixing_executing
                        │ fail, budget exhausted
                        ▼
                 done_review_failed                             (exit ≠0)
```

1. **starting** — resets per-run child state, then auto-enters the first
   background command run. This is the shared entry for standalone and imported
   use.
2. **running_executing** — runs `make test` (background) and branches on the
   exit code: green → review; red → `fixing_executing`.
3. **fixing_executing** — the `fixer` agent (claude / `claude-sonnet-4-6`, with
   `Read/Grep/Glob/Edit/Write/Bash`) reads the failure output and applies the
   minimal fix. It returns a structured artifact
   ([`schemas/fix_artifact.json`](./schemas/fix_artifact.json)). If a failure
   needs a product/architecture decision it must not make alone, it sets
   `needs_decision: true`, makes no edits, and routes to `blocked`.
4. **verifying_executing** — re-runs `make test`. Green → review; red with
   budget left → another `fixing_executing` cycle; red with the budget
   exhausted → `done_exhausted`.
5. **reviewing_executing** — the read-only `reviewer` decides whether the green
   diff preserves existing and in-progress functionality and keeps tests
   meaningful. A failed review loops back into the fixer with the review reason
   as feedback until the cycle budget is exhausted.
6. The terminal states write a report to `.artifacts/fix-tests/report-*.md`
   (via [`scripts/write_report.star`](./scripts/write_report.star)) and mirror a
   checkpoint into the operator inbox.

It "one-shots" by default — there is no human in the loop. The single point
where it stops and *asks* is a real, decision-requiring question: it surfaces
that in the report (and exits nonzero) rather than guessing.

## Architecture notes

- **Background jobs + `on_complete`.** Every test run and the fix are
  `background: true` invokes whose `on_complete:` targets the next room. Each
  completion is a fresh synthetic turn, so the engine's synchronous
  post-bind-emit recursion cap (4) never applies and the retry loop can run a
  real 3 cycles. `kitsoki session continue` drains the `*_executing` chain to a
  terminal in one invocation (its drain loop iterates while the state ends in
  `_executing`, up to 8 passes — the 3-cycle worst case is 7 jobs).
- **`max_cycles` defaults to 3 and should stay ≤ 3** for that drain budget.
- **The fixer edits your working tree.** The review gate must pass before the
  story exits successfully. The fixer is forbidden (by prompt) from weakening
  tests, touching git, or making network calls; `external_side_effect: false`.

## Configuration

`world.test_cmd` (default `make test`) and `world.max_cycles` (default `3`) can
be overridden via the `start` intent's slots, e.g. with a custom driver:

```
kitsoki session continue --app stories/fix-tests/app.yaml --id <sid> \
  --intent start --slots '{"test_cmd":"go test ./internal/foo/...","max_cycles":2}'
```

## Standalone and imported use

Standalone, drive the `start` intent as shown above. As an imported child, use
the exits:

| Exit | Meaning |
|---|---|
| `achieved` | The command is green and review passed. |
| `exhausted` | The deterministic command is still red after the cycle budget. |
| `needs-human` | The fixer blocked on a human decision, or review rejected the green diff after the budget. |

`dev-story` imports this story as `tests` and exposes it through `go_fix_tests`
from the landing workbench. `kitsoki-dev` projects `test_cmd: make test` into
that import.

Hosts used: `host.run`, `host.starlark.run`, `host.agent.task`,
`host.agent.decide`, `host.inbox.add`.

## Tests

Deterministic, no-LLM flow fixtures under [`flows/`](./flows/) cover every
branch (stubbed host results, `advance_clock` to drain the background jobs):

| Flow | Path |
|---|---|
| Suite green on first run | [`clean.yaml`](./flows/clean.yaml) |
| One fix cycle then green | [`fix_then_pass.yaml`](./flows/fix_then_pass.yaml) |
| Review rejects a weakened test, then passes after another cycle | [`review_rejects_test_weakening.yaml`](./flows/review_rejects_test_weakening.yaml) |
| Still red after 3 cycles | [`exhausted.yaml`](./flows/exhausted.yaml) |
| Fixer needs a decision | [`blocked.yaml`](./flows/blocked.yaml) |

```
kitsoki test flows stories/fix-tests/app.yaml
```
