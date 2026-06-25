# gears-rust bugfix marathon — running log

Goal: drive the kitsoki **bugfix** dev-story LIVE over 10 real gears-rust bugs
(each already fixed by a high-quality merged PR = baseline) entirely through the
kitsoki MCP studio (via the `kitsoki-mcp-driver` agent), independently verify each
fix against the real PR's regression test (the HIDDEN oracle), and harden the
generic `@kitsoki/dev-story` + the gears-rust instance so the pipeline solves bugs
reliably with correct project conventions — **fully autonomous, no hand-holding.**

Tracking: `.artifacts/gears-marathon/` — `cases.yaml` (the 10 baselines),
`attempts.jsonl` (append-only), `gen_table.py` → `STATUS.md` (deterministic table),
`verify/` (per-bug oracle harnesses), `traces/`, `slidey/`.

Method: `dogfood-marathon` skill + `.context/bakeoff-learnings.md` gotchas.

---

## 2026-06-25 — Session bootstrap

**Orientation.** Prior work proved the bake-off loop on `query-string` (fast JS
oracle, see `.artifacts/qs-bakeoff/`). The goal demands **gears-rust** specifically,
so the gating risk is Rust build/test time.

**Feasibility (PASS).** Cut a detached worktree at `e3ab3c27` (= `a7080261^`,
bug1's parent) and ran `cargo test -p cf-gears-toolkit --lib bootstrap::config`:
clean build + test in **~54s**. The Rust loop is viable per cell.

**bug1 selection — gh-4115** (`a7080261`, "normalize underscore→dash for k8s env-var
overrides of dashed gear names"). Single-file fix in `cf-gears-toolkit`; oracle test
`test_gh4115_dashed_gear_name_env_override_works` calls only the public API
`AppConfig::load_layered`. Clean, deterministic, behavioural oracle.

**bug1 RED pre-flight (PASS = genuinely RED).** Injected a public-API-only RED-check
integration test (`verify/bug1-oracle.rs`, using the existing `temp-env` dev-dep) into
the baseline worktree: `priority == 100`, expected `50` → the env override is silently
dropped at baseline. Confirms a real behavioural bug, not a test-on-top-of-merged-fix
(gotcha #2, avoids a degenerate cell).

**Candidate pool (10 pinned in `cases.yaml`).** bug1 confirmed-RED; bug2–bug10 are
focused single-package behavioural fixes with regression tests (oagw / errors /
resource-group / account-management / modkit-db), to be pinned + RED-confirmed before
driving. Flaky-timing test fixes deliberately excluded (non-deterministic oracle).

**Infra stood up.** `gen_table.py` (no-dep YAML/JSONL → STATUS.md), seeded
`attempts.jsonl`, slidey deck scaffold under `slidey/`.

## 2026-06-25 — bug1 SHIPPED ✅ (1/10)

Drove bug1 through `stories/bench-bugfix` LIVE via `kitsoki-mcp-driver`, profile
`codex-native` (gpt-5.5), against the prepared worktree
`~/code/gears-rust/.worktrees/marathon-bug1-gpt` (baseline e3ab3c27, `workspace_id:""`
so the maker edits the prepared worktree directly). Proven template adapted from
`.artifacts/qs-bakeoff/drive-prompts/`.

- **Pipeline:** reproduce → propose → implement → (operator accept) → testing →
  reviewing → done → `finished` / status `fixed` / exit `open-PR`. 3 forward turns.
- **Maker work:** RED-first reproducer (`left:100 right:50`, bug_verified=true) →
  1-file fix in `libs/toolkit/src/bootstrap/config/mod.rs` (commit `516f14bc`,
  +166/-13) reconciling underscore env aliases onto the dashed gear key; authored
  its OWN regression test + underscore negative control; 63 tests pass; judge accept 0.93.
- **INDEPENDENT VERIFY = PASS.** Copied the real gh-4115 PR test (`verify/bug1-oracle.rs`,
  public-API only) into the maker's worktree → **GREEN** (was RED at baseline). Removed
  it afterward to keep the maker tree pristine. The fix is correct by the hidden oracle,
  not just the maker's self-report.
- **Cost/tokens:** trace `traces/bug1-gpt-5.5.jsonl` — tokens ~2.31M total (in 2.29M /
  out 21k); gpt/codex provider journals **no `cost_usd`** in the trace (finding).
  Wall ≈ 854s incl. cold Rust builds.

### Findings (bug1)
- **F1 (cost):** codex/gpt traces carry no `payload.meta.cost_usd` → USD cost is
  unrecoverable for that provider; tokens are the only consistent axis. (matrix-comparison cost accounting.)
- **F2 (driver self-verify):** `kitsoki-mcp-driver`'s MCP surface has no trace-by-path
  reader and no git/shell tool, so it cannot capture commit SHAs or run the oracle
  itself — the operator must verify out-of-band. Expected per the "trust independent
  verify, not agent return" discipline, but worth a studio read-only `git_status`/
  `trace_read` affordance so the driver can self-report deliverable existence.
- **F3 (bench ticket-tracker):** terminal `host.local_files.ticket.transition: bug1
  not found` at `done→finished` — non-fatal in bench mode (no ticket file seeded);
  state still reached `finished`. Cosmetic; the bench story should no-op the
  transition when the ticket tracker has no record.

### Next
- bug2 (oagw streaming body-limit pipe). Pin baseline, confirm RED, drive.

---

## 2026-06-25 — bug4 SHIPPED ✅ (2/10)

bug4 = `fix(errors): support convert for different error types to CanonicalError`
(e21d79ab, pkg `cf-modkit-canonical-errors`, baseline c7a95be5). Oracle = the real
PR's `From<io::Error>`/`From<serde_json::Error>` tests; RED at baseline = compile-fail
(`From<serde_json::Error>` not impl, E0277). Same drive template, gpt-5.5.

- Pipeline `finished`/`open-PR`, 3 forward turns. Maker added `From<std::io::Error>`
  (→500/"Internal") + `From<serde_json::Error>` (→400/"Invalid Argument", serde msg as
  detail) in `src/error.rs` (commit `f9641874`, +12), authored its own regression test
  (commit `47c8e00f`); 11 tests pass.
- **INDEPENDENT VERIFY = PASS** — my hidden oracle (`verify/bug4-oracle.rs`) GREEN
  (3/3) against the maker fix (was compile-fail RED at baseline).
- tokens ~1.72M; wall ≈615s.

### Finding (bug4)
- **F4 (Rust CI gate):** the bench `bf__ci_log` build gate ran `go build ./...` in a
  Rust worktree → `directory prefix . does not contain main module`. Non-fatal (the
  scoped cargo `test_cmd` gates are authoritative and passed), but the story's default
  build-check is Go-shaped; for a generic dev-story it should derive the build/test
  command from the project profile (gears-rust-dev's `project-profile.yaml`) or the
  passed `test_cmd`, not hardcode Go. Generic hardening candidate (helps any non-Go repo).
- F3 (`ticket.transition: <id> not found`) recurred — confirmed systematic bench-mode
  cosmetic, not per-case.

### Next
- bug5 (resource-group allowed_memberships RG-prefix). Pin, RED-check, drive.

## 2026-06-25 — bug5 SHIPPED ✅ (3/10)

bug5 = `fix(resource-group): drop RG-prefix requirement for allowed_memberships`
(8737281d, pkg `cyberware-resource-group`, baseline 70d19538). Oracle = the real PR's
`validate_membership_type_code` contract tests; RED at baseline = compile-fail (fn absent).

- Pipeline `finished`/`open-PR`, 3 forward turns, gpt-5.5. Maker added a
  membership-specific validator `validate_membership_type_code` (no RG-prefix; still
  rejects empty/>1024) and wired it into `create_type`/`update_type` ONLY — leaving
  `req.code`/parent-types/add_membership/remove_membership untouched. **This matches the
  real maintainer's fix approach.** Commit `2a7c929d`; own regression tests in
  `type_service_test.rs`; 81 tests pass.
- **INDEPENDENT VERIFY = PASS** — hidden oracle 4/4 GREEN (accepts external + RG-prefixed;
  rejects empty + over-length).
- tokens ~`see table`; wall ≈704s.
- Note: `regression_red_pre_fix=false` (the maker's reproducer commit was a no-op — its
  repro test landed as part of the implement commit, not a separate RED-first commit).
  Independent oracle is authoritative; the gate technicality didn't block ship.

### Next
- bug6 (account-management effective realm + children-list parity). Pin, RED-check, drive.

### (bootstrap) Next
- Drive bug1 through `stories/bugfix` live via `kitsoki-mcp-driver`
  (`harness:live`, explicit `trace:`, `base=e3ab3c27`, scoped `test_cmd`, fresh
  per-case worktree), then independently verify with the bug1 oracle.
