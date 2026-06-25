# gears-rust bugfix marathon

## Fully-autonomous kitsoki dev-story over real merged-fix baselines

**2 / 10 bugs shipped** — each independently verified against the real PR's hidden regression-test oracle.

---

## Method

- Baseline = the real fix's PARENT commit; bug confirmed RED there.
- Drive `stories/bugfix` LIVE via the kitsoki studio MCP (`kitsoki-mcp-driver`); zero human edits.
- Independent verify: the real PR's regression test (HIDDEN from the maker) must turn GREEN.

---

## bug1 — ✅ SHIPPED

**gh-4115: normalize underscore→dash for k8s env-var overrides of dashed gear names**

- baseline RED: `true`  ·  candidate: `gpt-5.5`  ·  exit: `finished/open-PR`

- fix: `516f14bc`  ·  tokens: `2308868`  ·  wall: `854s`

- hidden oracle gh4115 GREEN; maker authored own regression test; 1-file fix

---

## bug4 — ✅ SHIPPED

**errors: support convert for different error types to CanonicalError**

- baseline RED: `true`  ·  candidate: `gpt-5.5`  ·  exit: `finished/open-PR`

- fix: `f9641874`  ·  tokens: `1724703`  ·  wall: `615s`

- hidden oracle (From io/serde_json) GREEN; +12 line fix; own regression test

---

