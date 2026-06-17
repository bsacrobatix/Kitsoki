/**
 * Spatial-oracle feature-spotlight video demo.
 *
 * Records a deterministic, NO-LLM tour of the spatial capture feature — the
 * /review spatial picker (click a frame → resolve the DOM element under the
 * click → {selector, role, text} chip), the per-flag chat that rides the
 * {frame, point, element} bundle to a STUBBED read-only off-path oracle, the
 * inline thumbnail + chip render on the answer, and the chrome-less /point
 * handoff window — into .artifacts/spatial-oracle/.
 *
 * POSTURE: this feature lives on the /review and /point routes, NOT the
 * story-library/observer surface the agent-actions tour walks, so it is NOT
 * driven by the kitsoki tour overlay. It reuses spatial-capture.spec.ts's
 * deterministic posture: the built dist/index.html is served by a tiny static
 * server WITHOUT an inlined snapshot (so createDataSource() returns LiveSource
 * and issues real JSON-RPC), and every RPC — including the offpath oracle — is
 * STUBBED via page.route with canned, reproducible answers. No live kitsoki
 * server, no LLM, no cost. Narration is the PORTABLE makeCaption + makeSpotlight
 * helpers (the gh-issue-review external-act posture), and each step's title is
 * asserted against SPATIAL_ORACLE_TOUR_STEPS — a drift guard.
 *
 * Validate fast (no dwells):
 *   WEB_CHAT_PACE=0 pnpm exec playwright test spatial-oracle-video --project=chromium
 * Record at watch-speed:
 *   pnpm exec playwright test spatial-oracle-video --project=chromium
 *
 * The harness suppresses Playwright stdout, so per-step progress + any failure
 * context is written to .artifacts/spatial-oracle/ERROR.txt and the NN-*.png
 * breadcrumbs.
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import { fileURLToPath } from "url";
import path from "path";
import fs from "fs";
import http from "http";
import type { AddressInfo } from "net";
import {
  makeShot,
  prepareVideoDir,
  saveVideoAsMp4,
  dwell,
  ChapterRecorder,
  writeChapters,
  SETTLE_MS,
} from "./_helpers/server.js";
import { makeCaption, makeSpotlight, captureDiagnostics, DEMO_VIEWPORT } from "./_helpers/demo.js";
import { SPATIAL_ORACLE_TOUR_STEPS, type TourStep } from "../../src/tour/spatial-oracle-manifest.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
// _helpers → playwright → tests → runstatus (project root for dist/)
const projectRoot = path.resolve(__dirname, "../..");
// _helpers → playwright → tests → runstatus → tools → kitsoki (repo root)
const repoRoot = path.resolve(__dirname, "../../../..");

const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "spatial-oracle");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");
const CHAPTER_SOURCE = "tools/runstatus/src/tour/spatial-oracle-manifest.ts";

const SID = "sess-spatial";
const VIDEO = "demo_video#ab12cd34";

// A valid 1×1 red PNG (same bytes as spatial-capture.spec.ts / the Go fixture).
const ONE_PX_PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNgYGAAAAAEAAHzwAAAAABJRU5ErkJggg==",
  "base64",
);

const CHAPTERS = [
  {
    index: 0,
    id: "intro",
    label: "Intro",
    start_ms: 0,
    end_ms: 10000,
    source_ref: { kind: "slidey", spec_path: "deck.json", scene_id: "intro" },
  },
];

// The STUBBED oracle answer — deterministic + reproducible, no LLM.
const STUB_ANSWER =
  "That's the video player control. It's disabled here because no source clip resolved for this scene.";

/** A tiny static server: serves the built SPA on GET and stubs the /point/return
 *  POST so the chrome-less window's send() resolves. Mirrors spatial-capture's
 *  startStaticServer, extended for the /point return endpoint. */
function startStaticServer(html: string): Promise<{ origin: string; close: () => void }> {
  return new Promise((resolve) => {
    const server = http.createServer((req, res) => {
      if (req.method === "POST" && (req.url ?? "").startsWith("/point/return")) {
        res.setHeader("Content-Type", "application/json");
        res.end(JSON.stringify({ ok: true }));
        return;
      }
      // Any GET path (/, /point, …) serves the SPA shell; the SPA reads the
      // hash route or ?chromeless flag to decide what to render.
      res.setHeader("Content-Type", "text/html");
      res.end(html);
    });
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address() as AddressInfo;
      resolve({ origin: `http://127.0.0.1:${port}`, close: () => server.close() });
    });
  });
}

/** Install the page.route RPC + artifact stubs for the no-LLM posture. */
async function installStubs(page: Page): Promise<void> {
  await page.route("**/rpc", async (route) => {
    const body = route.request().postDataJSON() as { method: string; params: Record<string, unknown> };
    let result: unknown = {};
    switch (body.method) {
      case "runstatus.video.chapters":
        result = { chapters: CHAPTERS };
        break;
      case "runstatus.video.frame":
        result = { handle: "frame#deadbeef", mime: "image/png", kind: "image" };
        break;
      case "runstatus.session.offpath":
        result = { answer: STUB_ANSWER };
        break;
      default:
        result = {};
    }
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ jsonrpc: "2.0", id: 1, result }),
    });
  });
  // The captured still + frame media resolve to the 1×1 PNG.
  await page.route("**/artifact/**", async (route) => {
    await route.fulfill({ contentType: "image/png", body: ONE_PX_PNG });
  });
}

/** Look up a manifest step by id (throws if a referenced step ever drifts). */
function step(id: string): TourStep {
  const s = SPATIAL_ORACLE_TOUR_STEPS.find((x) => x.id === id);
  if (!s) throw new Error(`spatial-oracle manifest has no step "${id}"`);
  return s;
}

test("spatial oracle feature-spotlight video", async () => {
  test.setTimeout(300000);

  const distIndex = path.join(projectRoot, "dist", "index.html");
  if (!fs.existsSync(distIndex)) {
    throw new Error(`dist/index.html not found — run the build (globalSetup) first`);
  }
  const html = fs.readFileSync(distIndex, "utf-8");
  const { origin, close } = await startStaticServer(html);

  prepareVideoDir(VIDEO_DIR);
  fs.mkdirSync(ARTIFACT_DIR, { recursive: true });

  const browser: Browser = await chromium.launch({ headless: true });
  const context: BrowserContext = await browser.newContext({
    viewport: DEMO_VIEWPORT,
    deviceScaleFactor: 2,
    recordVideo: { dir: VIDEO_DIR, size: DEMO_VIEWPORT },
  });
  const page: Page = await context.newPage();
  const video = page.video();
  const shot = makeShot(ARTIFACT_DIR);
  const chapters = new ChapterRecorder();
  const { mark, onThrow } = captureDiagnostics(page, ARTIFACT_DIR);

  await installStubs(page);

  try {
    // ── Stage /review behind a settle so the camera arrives on a composed view ─
    mark("goto /review");
    const reviewUrl = `${origin}/#/review/${SID}?video=${encodeURIComponent(VIDEO)}`;
    await page.goto(reviewUrl);
    await expect(page.getByTestId("review-page")).toBeVisible({ timeout: 15000 });

    // Portable narration: caption banner + spotlight box (both pointer-events:none).
    const caption = await makeCaption(page);
    const spotlight = await makeSpotlight(page);
    await dwell(page, SETTLE_MS);

    /** Narrate a manifest step: assert title-drift, caption it, spotlight its
     *  target (if any), open the chapter, dwell, and screenshot. */
    async function beat(id: string, opts: { spotlightFor?: string } = {}): Promise<TourStep> {
      const s = step(id);
      mark(s.id);
      // Drift guard: the spec must narrate the exact title the manifest ships.
      expect(s.title, `manifest step "${id}" title`).toBeTruthy();
      const sel = opts.spotlightFor ?? (s.target ? `[data-testid="${s.target}"]` : null);
      await spotlight(sel);
      chapters.open(s.id, s.title, CHAPTER_SOURCE);
      await caption(s.title, s.body, s.dwellMs ?? 4000);
      await shot(page, s.id);
      return s;
    }

    // ── 1. Intro ───────────────────────────────────────────────────────────────
    await beat("so-intro");

    // ── 2. The review surface ────────────────────────────────────────────────
    await beat("so-review");

    // ── 3. Flag a scene → selects a flag → mounts the picker ──────────────────
    await beat("so-flag");
    mark("flag the scene");
    await page.getByTestId("ct-marker-intro").click();
    await page.getByTestId("ct-flag-btn").click();
    await expect(page.getByTestId("flag-detail")).toBeVisible();
    await expect(page.getByTestId("spatial-picker")).toBeVisible();
    await dwell(page, SETTLE_MS);

    // ── 4. The picker overlay ─────────────────────────────────────────────────
    await beat("so-picker");

    // ── 5. Click the frame → pin a crosshair + resolve the element ────────────
    mark("click the frame");
    await page.getByTestId("spatial-picker").click({ position: { x: 120, y: 90 } });
    await expect(page.getByTestId("sp-point")).toBeVisible();
    await expect(page.getByTestId("fd-element")).toBeVisible();
    await expect(page.getByTestId("fd-element")).toContainText("rp-player");
    await dwell(page, SETTLE_MS);
    await beat("so-point");
    await beat("so-element");

    // ── 6. Ask a question → stubbed oracle answer renders with thumbnail+chip ──
    await beat("so-ask");
    mark("ask the question");
    await page.getByTestId("fd-chat-box").fill("what is this control?");
    await page.getByTestId("fd-chat-send").click();
    // The stubbed answer renders in the chat transcript.
    await expect(page.getByTestId("fd-chat")).toContainText(STUB_ANSWER, { timeout: 8000 });
    // The captured frame thumbnail + the element chip stay alongside it.
    await expect(page.getByTestId("fd-still")).toBeVisible();
    await expect(page.getByTestId("fd-element")).toBeVisible();
    await dwell(page, SETTLE_MS);
    await beat("so-answer");

    // ── 7. The chrome-less /point handoff window ──────────────────────────────
    mark("goto /point chromeless");
    // Lift the spotlight before navigating away (it lives on the old DOM).
    await spotlight(null);
    const pointUrl =
      `${origin}/point?chromeless=1&token=tok-demo` +
      `&media_handle=${encodeURIComponent("frame#deadbeef")}&t_ms=0` +
      `&route=${encodeURIComponent(`/review/${SID}`)}` +
      `&prompt=${encodeURIComponent("Point at what you mean, then send.")}`;
    await page.goto(pointUrl);
    await expect(page.getByTestId("point-page")).toBeVisible({ timeout: 15000 });
    // Re-install narration: injected DOM does not survive a navigation.
    const caption2 = await makeCaption(page);
    const spotlight2 = await makeSpotlight(page);
    await dwell(page, SETTLE_MS);

    // Pin a point in the handoff window's picker so its crosshair shows on camera.
    mark("point in handoff window");
    await page.getByTestId("spatial-picker").click({ position: { x: 120, y: 90 } });
    await expect(page.getByTestId("sp-point")).toBeVisible();
    await page.getByTestId("pp-input").fill("why is this disabled here?");
    await dwell(page, SETTLE_MS);

    {
      const s = step("so-point-window");
      mark(s.id);
      await spotlight2(s.target ? `[data-testid="${s.target}"]` : null);
      chapters.open(s.id, s.title, CHAPTER_SOURCE);
      await caption2(s.title, s.body, s.dwellMs ?? 4000);
      await shot(page, s.id);
    }

    // ── 8. Done ────────────────────────────────────────────────────────────────
    {
      const s = step("so-done");
      mark(s.id);
      await spotlight2(null);
      chapters.open(s.id, s.title, CHAPTER_SOURCE);
      await caption2(s.title, s.body, s.dwellMs ?? 4000);
      await shot(page, s.id);
    }
  } catch (e) {
    onThrow(e);
    fs.appendFileSync(
      path.join(ARTIFACT_DIR, "ERROR.txt"),
      `\n${e instanceof Error ? e.stack ?? e.message : String(e)}\n`,
    );
    throw e;
  } finally {
    await context.close();
    const mp4 = await saveVideoAsMp4(video, ARTIFACT_DIR, "spatial-oracle-demo");
    writeChapters(mp4, chapters.list());
    await browser.close();
    close();
  }

  const pngs = fs.readdirSync(ARTIFACT_DIR).filter((f) => f.endsWith(".png"));
  console.log(`[spatial-oracle-video] screenshots (${pngs.length}) in ${ARTIFACT_DIR}`);
});
