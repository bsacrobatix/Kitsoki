#!/usr/bin/env node
// Sync the product-site feature specs (features/*.yaml) into the federated
// POG member catalog (pog/catalog.yaml), replacing everything below the
// generated-block marker in place. Run from anywhere:
//
//   node pog/sync-product-site.mjs
//
// Taxonomy (Brad, 2026-07-12): the TOUR-DRIVEN SPEC is the key persistent
// piece — the playwright demo/tour spec deterministically produces the rrweb
// recording and the rendered mp4, so those are ARTIFACTS of the spec, not
// independent deliverables. Each feature's demo node is therefore modeled
// spec-first: the spec is listed as the source evidence, the rrweb replay
// (playable in the portal through the slidey rrweb player) is the preferred
// artifact, the site-published mp4 + poster the rendered fallback. Statuses
// stay derived from what actually exists on disk in the member root:
// verified when a produced artifact is real, planned when only specced.
//
// Disk bridges (gitignored symlinks under .artifacts/, safe on the staging
// branch because .artifacts is ignored):
//   .artifacts/site-media  -> <main>/tools/site/src/public/media      (mp4+poster)
//   .artifacts/site-tours  -> <main>/tools/runstatus/src/tour/generated (tour manifests)
//   .artifacts/demo-specs  -> <main>/tools/runstatus/tests/playwright  (the specs)
import { readFileSync, readdirSync, writeFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const CATALOG = join(ROOT, "pog/catalog.yaml");
const SPECS = join(ROOT, "features");
const MARKER = "  # --- product-site features & deliverables";

// YAML parser: whichever package root has one installed (`yaml` rides with
// tools/runstatus, `js-yaml` with the POG portal). No hard dependency here.
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
  throw new Error("no YAML parser found (need `yaml` in tools/runstatus or js-yaml in POG portal)");
}
const parseYaml = loadYamlParser();

const specs = readdirSync(SPECS)
  .filter((f) => f.endsWith(".yaml"))
  .map((f) => parseYaml(readFileSync(join(SPECS, f), "utf8")));

// The product site's own generated feature index (VitePress build input) is
// the inclusion source of truth: everything it lists IS on the site today.
const SITE_INDEX_CANDIDATES = [
  join(ROOT, "tools/site/.vitepress/gen/features-index.json"),
  join(ROOT, "../../tools/site/.vitepress/gen/features-index.json"),
];
const siteIndexPath = SITE_INDEX_CANDIDATES.find((p) => existsSync(p));
const siteIndex = siteIndexPath ? JSON.parse(readFileSync(siteIndexPath, "utf8")) : [];
const siteEntries = Array.isArray(siteIndex) ? siteIndex : (siteIndex.features ?? Object.values(siteIndex));
const siteIds = new Set(siteEntries.map((e) => e.id));

// Persisted rrweb recordings that predate the per-feature capture specs —
// the slidey case-study clips live under .artifacts/slidey-hybrid/clips/.
const CLIP_MAP = {
  "slidey-decomposition": "decomposition",
  "slidey-architect-design": "architect-design",
  "slidey-bugfix": "slidey-bugfix",
  "slidey-open-pr": "open-pr",
  "slidey-dev-prd-design": "pm-idea",
};

function findRrweb(spec) {
  const clip = CLIP_MAP[spec.id];
  if (clip && existsSync(join(ROOT, ".artifacts/slidey-hybrid/clips", `${clip}.rrweb.json`))) {
    return `.artifacts/slidey-hybrid/clips/${clip}.rrweb.json`;
  }
  const dir = spec.demo?.artifactDir ?? spec.id;
  for (const base of [join(ROOT, ".artifacts", dir), join(ROOT, ".artifacts/rrweb-eval", spec.id)]) {
    if (!existsSync(base)) continue;
    try {
      const hit = readdirSync(base, { recursive: true }).find((f) => String(f).endsWith(".rrweb.json"));
      if (hit) return join(base, String(hit)).slice(ROOT.length + 1);
    } catch {
      /* unreadable — skip */
    }
  }
  return "";
}

// Where a capture spec WILL write its recording — parsed from the spec's own
// ARTIFACT_DIR/EVENTS_JSON constants (the shared convention across the
// *-rrweb-capture specs). Lets the catalog declare the rrweb artifact before
// it exists on a given machine: the recording is gitignored and
// deterministically produced, so a fresh clone renders it on demand via
// pog/render-demo.mjs (the portal's render-on-demand flow).
function expectedRrweb(specEvidencePath) {
  if (!specEvidencePath || !specEvidencePath.includes("-rrweb-capture.spec.ts")) return "";
  let text = "";
  try {
    text = readFileSync(join(ROOT, specEvidencePath), "utf8");
  } catch {
    return "";
  }
  const dirMatch = text.match(/ARTIFACT_DIR = path\.join\(\s*repoRoot,\s*([^)]+)\)/);
  const eventsMatch = text.match(/EVENTS_JSON = path\.join\(\s*ARTIFACT_DIR,\s*"([^"]+)"\s*\)/);
  const segs = dirMatch ? [...dirMatch[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]) : [];
  if (!eventsMatch || segs[0] !== ".artifacts") return "";
  return [...segs, eventsMatch[1]].join("/");
}

// The tour-driven spec itself (the persistent source). Prefer the dedicated
// rrweb-capture spec when one exists; fall back to the video spec.
function findSpecFile(spec) {
  const names = [];
  for (const stem of [spec.id, spec.demo?.artifactDir, spec.demo?.videoBase?.replace(/-demo$/, "")]) {
    if (stem) names.push(`${stem}-rrweb-capture.spec.ts`);
  }
  if (spec.demo?.spec) names.push(spec.demo.spec.split("/").pop());
  for (const name of names) {
    if (name && existsSync(join(ROOT, ".artifacts/demo-specs", name))) return `.artifacts/demo-specs/${name}`;
  }
  return "";
}

const catalogText = readFileSync(CATALOG, "utf8");
const markerAt = catalogText.indexOf(`\n${MARKER}`);
const prefix = markerAt >= 0 ? catalogText.slice(0, markerAt).replace(/\n+$/, "\n") : catalogText.replace(/\n+$/, "\n");
const existingFeatureIds = new Set([...prefix.matchAll(/^    id: (feature-[a-z0-9-]+)$/gm)].map((m) => m[1]));
const existingIds = new Set([...prefix.matchAll(/^    id: ([a-z0-9-]+)$/gm)].map((m) => m[1]));

const q = (s) => JSON.stringify(String(s ?? "").replace(/\s+/g, " ").trim());
const lines = [];
const stats = { features: 0, demos: 0, docs: 0, tutorials: 0, verified: 0, planned: 0, rrweb: 0, renderable: 0 };

lines.push("");
lines.push(`${MARKER} ---------------------------------`);
lines.push("  # GENERATED by pog/sync-product-site.mjs — edit specs in features/*.yaml");
lines.push("  # and rerun; everything below this marker is replaced in place.");
lines.push("  # Spec-first model: the tour-driven spec is the persistent source; the");
lines.push("  # rrweb replay and rendered mp4 are its deterministically produced");
lines.push("  # artifacts. Statuses derive from what exists on disk right now.");

for (const spec of specs) {
  if (!spec?.id) continue;
  const featureId = `feature-${spec.id}`;
  const inCatalog = existingFeatureIds.has(featureId);
  if (!siteIds.has(spec.id) && !spec.promo && !inCatalog) continue; // not on the product site

  if (!inCatalog) {
    stats.features++;
    lines.push(`  - schema: kitsoki/feature/v0`);
    lines.push(`    id: ${featureId}`);
    lines.push(`    title: ${q(spec.title)}`);
    lines.push(`    status: shipped`);
    lines.push(`    visibility: public`);
    lines.push(`    summary: ${q(spec.summary ?? spec.tagline)}`);
    lines.push(`    sources: [source-feature-specs]`);
    lines.push(`    edges:`);
    lines.push(`      part_of: [product-kitsoki]`);
  }

  if (spec.demo) {
    const rrwebPath = findRrweb(spec);
    const specPath = findSpecFile(spec);
    // No persisted recording, but a capture spec that can produce one: still
    // declare the rrweb evidence at the spec's output path. The portal marks
    // it unavailable and offers render-on-demand (pog/render-demo.mjs).
    const rrwebExpected = !rrwebPath ? expectedRrweb(specPath) : "";
    const posterPath = existsSync(join(ROOT, ".artifacts/site-media", spec.id, "poster.png"))
      ? `.artifacts/site-media/${spec.id}/poster.png`
      : "";
    let mp4Path = "";
    if (existsSync(join(ROOT, ".artifacts/site-media", spec.id, "demo.mp4"))) {
      mp4Path = `.artifacts/site-media/${spec.id}/demo.mp4`;
    } else {
      const dir = spec.demo.artifactDir ?? spec.id;
      const base = spec.demo.videoBase ?? `${spec.id}-demo`;
      const artDir = join(ROOT, ".artifacts", dir);
      if (existsSync(artDir)) {
        const files = readdirSync(artDir).filter((f) => f.startsWith(base) && f.endsWith(".mp4") && !f.includes("BUG"));
        const video = files.find((f) => f === `${base}.mp4`) ?? files[0] ?? "";
        if (video) mp4Path = `.artifacts/${dir}/${video}`;
      }
    }
    const produced = Boolean(rrwebPath || mp4Path);
    const status = produced ? "verified" : "planned";
    stats.demos++;
    stats[produced ? "verified" : "planned"]++;
    if (rrwebPath) stats.rrweb++;
    if (rrwebExpected) stats.renderable++;
    const id = `demo-${spec.id}`;
    if (!existingIds.has(id)) {
      const specRel = spec.demo.spec ?? "";
      lines.push(`  - schema: kitsoki/demo/v0`);
      lines.push(`    id: ${id}`);
      lines.push(`    title: ${q(`Demo — ${spec.title}`)}`);
      lines.push(`    status: ${status}`);
      lines.push(`    visibility: public`);
      lines.push(
        `    summary: ${q(
          produced
            ? `Deterministically produced from the tour-driven spec ${specRel || "(spec pending)"} — ${[
                rrwebPath && "rrweb replay persisted",
                mp4Path && (mp4Path.startsWith(".artifacts/site-media/") ? "video published on the product site" : "video rendered, not yet published"),
              ]
                .filter(Boolean)
                .join("; ")}.`
            : rrwebExpected
              ? `Tour-driven spec ${specRel || "(spec pending)"} authored; the rrweb recording is deterministically producible on demand (node pog/render-demo.mjs demo-${spec.id}).`
              : `Tour-driven spec ${specRel || "(spec pending)"} authored; rrweb + video artifacts not yet produced — rerun the capture to materialize them.`,
        )}`,
      );
      lines.push(`    sources: [source-feature-specs]`);
      const evidence = [];
      if (rrwebPath) evidence.push({ kind: "video", title: `${spec.title} — rrweb replay`, path: rrwebPath, poster: posterPath });
      else if (rrwebExpected) evidence.push({ kind: "video", title: `${spec.title} — rrweb replay (render on demand)`, path: rrwebExpected, poster: posterPath });
      if (mp4Path) evidence.push({ kind: "video", title: `${spec.title} — demo video`, path: mp4Path, poster: posterPath });
      if (specPath) evidence.push({ kind: "doc", title: `${spec.title} — tour-driven spec (source)`, path: specPath, poster: "" });
      if (evidence.length) {
        lines.push(`    evidence:`);
        for (const item of evidence) {
          lines.push(`      - kind: ${item.kind}`);
          lines.push(`        title: ${q(item.title)}`);
          lines.push(`        path: ${item.path}`);
          if (item.poster) lines.push(`        poster: ${item.poster}`);
        }
      }
      lines.push(`    edges:`);
      lines.push(`      part_of: [product-kitsoki]`);
      lines.push(`      demonstrates: [${featureId}]`);
    }
  }

  if (Array.isArray(spec.docs) && spec.docs.length) {
    const present = spec.docs.filter((p) => existsSync(join(ROOT, p)));
    const status = present.length === spec.docs.length ? "verified" : "planned";
    stats.docs++;
    stats[status === "verified" ? "verified" : "planned"]++;
    const id = `doc-${spec.id}`;
    if (!existingIds.has(id)) {
      lines.push(`  - schema: kitsoki/doc/v0`);
      lines.push(`    id: ${id}`);
      lines.push(`    title: ${q(`Docs — ${spec.title}`)}`);
      lines.push(`    status: ${status}`);
      lines.push(`    visibility: public`);
      lines.push(
        `    summary: ${q(
          status === "verified"
            ? `Authored documentation: ${spec.docs.join(", ")}.`
            : `Doc set partially authored (${present.length}/${spec.docs.length} present): ${spec.docs.join(", ")}.`,
        )}`,
      );
      lines.push(`    sources: [source-feature-specs]`);
      if (present.length) {
        lines.push(`    evidence:`);
        for (const p of present) {
          lines.push(`      - kind: doc`);
          lines.push(`        title: ${q(p)}`);
          lines.push(`        path: ${p}`);
        }
      }
      lines.push(`    edges:`);
      lines.push(`      part_of: [product-kitsoki]`);
      lines.push(`      documents: [${featureId}]`);
    }
  }

  // A tour is VERIFIED when its codegenned manifest actually ships (the tour
  // steps in the feature spec are its source; the generated manifest is the
  // deterministic artifact the live in-product overlay runs).
  const steps = spec.tour?.steps?.length ?? 0;
  const manifest = existsSync(join(ROOT, ".artifacts/site-tours", `${spec.id}.ts`));
  if (steps || manifest) {
    const status = manifest ? "verified" : "planned";
    stats.tutorials++;
    stats[manifest ? "verified" : "planned"]++;
    const id = `tutorial-${spec.id}`;
    if (!existingIds.has(id)) {
      lines.push(`  - schema: kitsoki/tutorial/v0`);
      lines.push(`    id: ${id}`);
      lines.push(`    title: ${q(`Guided tour — ${spec.title}`)}`);
      lines.push(`    status: ${status}`);
      lines.push(`    visibility: public`);
      lines.push(
        `    summary: ${q(
          manifest
            ? `In-product guided tour, ${steps} steps, shipped with the app (generated manifest ${spec.id}.ts).`
            : `Tour authored in the feature spec (${steps} steps) but its generated manifest hasn't shipped yet.`,
        )}`,
      );
      lines.push(`    sources: [source-feature-specs]`);
      if (manifest) {
        lines.push(`    evidence:`);
        lines.push(`      - kind: doc`);
        lines.push(`        title: ${q(`${spec.title} — generated tour manifest`)}`);
        lines.push(`        path: .artifacts/site-tours/${spec.id}.ts`);
      }
      lines.push(`    edges:`);
      lines.push(`      part_of: [product-kitsoki]`);
      lines.push(`      teaches: [${featureId}]`);
    }
  }
}

writeFileSync(CATALOG, `${prefix}${lines.join("\n")}\n`);
console.log(JSON.stringify(stats));
