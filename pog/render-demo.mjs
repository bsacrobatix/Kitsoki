#!/usr/bin/env node
// render-demo.mjs — on-demand rrweb rendering for a demo node (the render
// half of the spec-first taxonomy: the tour-driven playwright capture spec
// is the committed, persistent source; the rrweb recording is its
// deterministically produced, gitignored artifact — so a fresh clone can see
// the spec in the catalog and materialize the recording locally).
//
//   node pog/render-demo.mjs demo-<feature-id>
//
// This is the well-known runner contract the POG portal's POST /api/render
// invokes (it only ever runs THIS file with a validated node id — never a
// command taken from catalog data). Progress goes to stderr; the final
// stdout line is JSON: {"ok":true,"paths":["<repo-relative rrweb path>"]}.
//
// What it does: resolve the feature's *-rrweb-capture.spec.ts, run it via
// playwright in whichever tools/runstatus checkout has it installed (this
// worktree, else the main checkout two levels up), then bridge the produced
// .artifacts/<dir> back under this member root with a gitignored symlink so
// the portal's evidence route can serve it.
import { existsSync, mkdirSync, readFileSync, readdirSync, symlinkSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));

function fail(msg) {
  console.error(`[render-demo] ${msg}`);
  console.log(JSON.stringify({ ok: false, error: msg }));
  process.exit(1);
}

const nodeId = process.argv[2] ?? "";
if (!/^demo-[a-z0-9-]+$/.test(nodeId)) fail(`usage: node pog/render-demo.mjs demo-<feature-id> (got ${JSON.stringify(nodeId)})`);
const featureId = nodeId.replace(/^demo-/, "");

// YAML parser: same candidate chain as pog/sync-product-site.mjs.
function loadYamlParser() {
  const candidates = [
    { pkg: join(ROOT, "tools/runstatus/package.json"), name: "yaml", parse: (m, s) => m.parse(s) },
    { pkg: join(ROOT, "../../tools/runstatus/package.json"), name: "yaml", parse: (m, s) => m.parse(s) },
    { pkg: "/Users/brad/code/POG/portal/package.json", name: "js-yaml", parse: (m, s) => m.load(s) },
  ];
  for (const c of candidates) {
    try {
      const mod = createRequire(c.pkg)(c.name);
      return (s) => c.parse(mod, s);
    } catch {
      /* try next */
    }
  }
  fail("no YAML parser found (need `yaml` in tools/runstatus or js-yaml in the POG portal)");
}
const parseYaml = loadYamlParser();

const spec = readdirSync(join(ROOT, "features"))
  .filter((f) => f.endsWith(".yaml"))
  .map((f) => parseYaml(readFileSync(join(ROOT, "features", f), "utf8")))
  .find((s) => s?.id === featureId);
if (!spec) fail(`no feature spec with id ${featureId} under features/`);

// The capture spec (rrweb is the only thing this runner produces — mp4
// rendering is a separate, ignored pipeline per the spec-first ruling).
const stems = [...new Set([spec.id, spec.demo?.artifactDir, spec.demo?.videoBase?.replace(/-demo$/, "")].filter(Boolean))];
const specNames = stems.map((stem) => `${stem}-rrweb-capture.spec.ts`);

// Run in whichever runstatus checkout actually has the spec + node_modules:
// this member worktree first, then the main checkout it was cut from.
const runstatusDirs = [join(ROOT, "tools/runstatus"), resolve(ROOT, "../../tools/runstatus")];
let runstatusDir = "";
let specName = "";
for (const dir of runstatusDirs) {
  const hit = specNames.find((name) => existsSync(join(dir, "tests/playwright", name)));
  if (hit && existsSync(join(dir, "node_modules"))) {
    runstatusDir = dir;
    specName = hit;
    break;
  }
}
if (!runstatusDir) fail(`no runnable rrweb capture spec for ${featureId} (looked for ${specNames.join(", ")} under ${runstatusDirs.join(" and ")})`);

// Where the spec will write: parse its ARTIFACT_DIR/EVENTS_JSON constants —
// the shared convention across the *-rrweb-capture specs.
const specPath = join(runstatusDir, "tests/playwright", specName);
const specText = readFileSync(specPath, "utf8");
const dirMatch = specText.match(/ARTIFACT_DIR = path\.join\(\s*repoRoot,\s*([^)]+)\)/);
const eventsMatch = specText.match(/EVENTS_JSON = path\.join\(\s*ARTIFACT_DIR,\s*"([^"]+)"\s*\)/);
const dirSegs = dirMatch ? [...dirMatch[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]) : [];
if (!eventsMatch || dirSegs[0] !== ".artifacts") fail(`${specName} doesn't follow the ARTIFACT_DIR/EVENTS_JSON convention — can't locate its output`);
const producedRel = [...dirSegs, eventsMatch[1]].join("/");
const specRepoRoot = resolve(runstatusDir, "../..");

// Run with a generated minimal config instead of tools/runstatus's own:
// the default config's globalSetup rebuilds the SPA and stages the Go embed
// asset, and its outputDir/html reporter write INSIDE tools/runstatus — all
// of which fail when the checkout is locked read-only (the main tree's
// clobber protection). The captures only need the prebuilt bin/kitsoki, so
// every write goes under .artifacts instead. Plain object export — no
// defineConfig import, so the config resolves from .artifacts too.
const renderDir = join(specRepoRoot, ".artifacts", "render-demo");
mkdirSync(renderDir, { recursive: true });
const configPath = join(renderDir, "playwright.render.config.mjs");
writeFileSync(
  configPath,
  `export default {
  testDir: ${JSON.stringify(join(runstatusDir, "tests/playwright"))},
  workers: 1,
  use: { actionTimeout: 15000, navigationTimeout: 15000 },
  expect: { timeout: 5000 },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
  outputDir: ${JSON.stringify(join(renderDir, "test-results"))},
  reporter: [["list"]],
};
`,
);

console.error(`[render-demo] ${nodeId}: running ${specName} in ${runstatusDir} (writes ${producedRel})`);
const run = spawnSync("pnpm", ["exec", "playwright", "test", "-c", configPath, specName, "--project=chromium"], {
  cwd: runstatusDir,
  stdio: ["ignore", 2, 2], // playwright chatter goes to stderr; stdout stays clean for the JSON contract
  timeout: 15 * 60 * 1000,
  // Default the story server to go-run mode: it compiles current source into
  // GOCACHE (no writes into a possibly locked checkout) instead of trusting a
  // prebuilt bin/kitsoki that may predate the spec. Callers can override.
  env: { ...process.env, KITSOKI_WEB_GO_RUN: process.env.KITSOKI_WEB_GO_RUN ?? "1" },
});
if (run.status !== 0) fail(`playwright run failed (exit ${run.status ?? "signal"}) — see log above`);

const producedAbs = join(specRepoRoot, producedRel);
if (!existsSync(producedAbs)) fail(`spec passed but expected output ${producedRel} is missing under ${specRepoRoot}`);

// Bridge the artifact dir back under this member root (gitignored symlink,
// same pattern as .artifacts/site-media and .artifacts/demo-specs) so the
// portal's evidence route — which resolves paths against the member root —
// can serve the recording.
if (specRepoRoot !== ROOT && !existsSync(join(ROOT, producedRel))) {
  // Link the shallowest missing directory along the artifact path (a real
  // .artifacts/rrweb-eval dir may already exist here — then link one level
  // deeper, the spec's own leaf dir).
  for (let depth = 1; depth < dirSegs.length; depth += 1) {
    const rel = dirSegs.slice(0, depth + 1).join("/");
    if (existsSync(join(ROOT, rel))) continue;
    mkdirSync(dirname(join(ROOT, rel)), { recursive: true });
    symlinkSync(join(specRepoRoot, rel), join(ROOT, rel));
    console.error(`[render-demo] linked ${rel} -> ${specRepoRoot}/${rel}`);
    break;
  }
  // Every directory level already exists as a real dir here (e.g. this
  // member root has its own old .artifacts/rrweb-eval runs) — link the
  // produced file itself.
  if (!existsSync(join(ROOT, producedRel))) {
    mkdirSync(dirname(join(ROOT, producedRel)), { recursive: true });
    symlinkSync(join(specRepoRoot, producedRel), join(ROOT, producedRel));
    console.error(`[render-demo] linked ${producedRel} -> ${specRepoRoot}/${producedRel}`);
  }
  if (!existsSync(join(ROOT, producedRel))) fail(`could not bridge ${producedRel} under the member root`);
}

console.error(`[render-demo] done: ${producedRel}`);
console.log(JSON.stringify({ ok: true, paths: [producedRel] }));
