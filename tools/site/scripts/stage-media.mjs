#!/usr/bin/env node
/**
 * Stage recorded demo media from .artifacts/ into the site's gitignored
 * src/public/media/<featureId>/ tree:
 *
 *   demo.mp4            the recorded demo (full variant only)
 *   chapters.json       the <video>.mp4.chapters.json sidecar, when present
 *   poster.png          the feature's posterStep screenshot (else first shot)
 *   steps/NN-<id>.png   every per-step screenshot (full variant only)
 *
 * Videos are NEVER committed — record them with `make demos` (or
 * `make demo-feature FEATURE=<id>`) first. Missing media is a WARNING, never a
 * failure: the site builds docs-only with poster/placeholder fallbacks.
 *
 * A rrweb-native story-demo (`demo.embed` in features/*.yaml) has no mp4 at
 * all — its committed, pre-bundled Slidey deck html (docs/decks/bundled/) is
 * copied verbatim into the shared src/public/decks/<file>.html tree instead
 * (several features can point at the same bundled deck, each at its own
 * `?scene=` index — see .vitepress/data/features.ts). Unlike mp4s this asset
 * IS committed source, so a missing one is a broken reference, not "not yet
 * recorded" — see check-media.mjs's stricter check for that file.
 *
 * --variant embedded  stage posters only (the binary-embedded help build —
 *                     no MP4s or deck embeds in the binary; pages link out to
 *                     the hosted site).
 */
import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const siteDir = path.resolve(__dirname, "..");
const repoRoot = path.resolve(siteDir, "../..");
const mediaDir = path.join(siteDir, "src", "public", "media");
const decksDir = path.join(siteDir, "src", "public", "decks");
const indexPath = path.join(siteDir, ".vitepress", "gen", "features-index.json");

const embedded = process.argv.includes("--variant")
  ? process.argv[process.argv.indexOf("--variant") + 1] === "embedded"
  : process.env.SITE_VARIANT === "embedded";

if (!fs.existsSync(indexPath)) {
  console.error(`stage-media: ${path.relative(repoRoot, indexPath)} missing — run: make site-data`);
  process.exit(1);
}
const index = JSON.parse(fs.readFileSync(indexPath, "utf8"));

function makeWritableIfExists(file) {
  try {
    const mode = fs.statSync(file).mode;
    fs.chmodSync(file, mode | 0o200);
  } catch (err) {
    if (err?.code !== "ENOENT") throw err;
  }
}

function copyStagedFile(src, dest) {
  makeWritableIfExists(dest);
  fs.copyFileSync(src, dest);
}

fs.rmSync(mediaDir, { recursive: true, force: true });
fs.rmSync(decksDir, { recursive: true, force: true });

let videos = 0;
let embeds = 0;
const missing = [];
const stagedDecks = new Set();
for (const f of index.features) {
  if (!f.demo) continue;
  if (f.demo.external) continue;

  // demo.embed: a rrweb-native story-demo ships a pre-bundled, committed,
  // self-contained Slidey deck html (docs/decks/bundled/) instead of an mp4.
  // Staged once, shared, under src/public/decks/ — several features can point
  // at the same bundled deck at different `?scene=` indices. Excluded from the
  // embedded (binary /help/) variant, same as mp4s — it's a ~17MB asset and
  // the binary build stays posters-only; the embedded build's placeholder
  // links out to the hosted site instead.
  if (f.demo.embed) {
    if (embedded) continue;
    const src = path.join(repoRoot, f.demo.embed.deckHtml);
    const dest = path.join(decksDir, path.basename(f.demo.embed.deckHtml));
    if (fs.existsSync(src)) {
      fs.mkdirSync(decksDir, { recursive: true });
      if (!stagedDecks.has(dest)) {
        copyStagedFile(src, dest);
        stagedDecks.add(dest);
      }
      embeds++;
    } else {
      missing.push(`${f.id}: ${f.demo.embed.deckHtml} (bundle it once: slidey bundle <deck> ${f.demo.embed.deckHtml})`);
    }
    continue;
  }

  const srcDir = path.join(repoRoot, f.demo.artifactDir);
  const out = path.join(mediaDir, f.id);

  const shots = fs.existsSync(srcDir)
    ? fs.readdirSync(srcDir).filter((n) => /^\d+-.+\.png$/.test(n)).sort()
    : [];
  const posterShot = f.demo.posterStep
    ? shots.find((n) => n.endsWith(`-${f.demo.posterStep}.png`)) ?? shots[0]
    : shots[0];

  if (posterShot) {
    fs.mkdirSync(out, { recursive: true });
    copyStagedFile(path.join(srcDir, posterShot), path.join(out, "poster.png"));
  }

  if (embedded) {
    if (!posterShot) missing.push(`${f.id}: no screenshots (poster unavailable)`);
    continue;
  }

  if (shots.length > 0) {
    fs.mkdirSync(path.join(out, "steps"), { recursive: true });
    for (const n of shots) copyStagedFile(path.join(srcDir, n), path.join(out, "steps", n));
  }

  const video = path.join(repoRoot, f.demo.video);
  if (fs.existsSync(video)) {
    fs.mkdirSync(out, { recursive: true });
    copyStagedFile(video, path.join(out, "demo.mp4"));
    videos++;
    const chapters = path.join(repoRoot, f.demo.chapters);
    if (fs.existsSync(chapters)) copyStagedFile(chapters, path.join(out, "chapters.json"));
  } else {
    missing.push(`${f.id}: ${f.demo.video} (record with: make demo-feature FEATURE=${f.id})`);
  }
}

console.log(
  `stage-media: staged ${videos} video(s), ${embeds} embed(s)${embedded ? " [embedded: posters only]" : ""} -> ${path.relative(repoRoot, mediaDir)}`,
);
for (const m of missing) console.warn(`stage-media: missing ${m}`);
