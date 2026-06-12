/**
 * Harness-profile picker feature-spotlight video.
 *
 * Drives the runstatus web header's provider/model picker against a real
 * `kitsoki web` server in the deterministic NO-LLM posture (bugfix story under
 * the happy_llm flow). The picker is fed by a harness_profiles fixture
 * (.kitsoki.yaml) declaring the three profiles the feature ships for:
 * claude-native, synthetic-claude (an env retarget with a model catalog), and
 * synthetic-codex (the codex backend). No real key is needed — the flow posture
 * never forks a backend, so SYNTHETIC_API_KEY is a dummy that only satisfies the
 * fixture's ${VAR} load-time expansion.
 *
 * The whole turn-driving is unnecessary: this feature lives entirely in the
 * observer header, so the video opens on the observer and narrates the picker
 * switching backend (claude → synthetic-claude → synthetic-codex) and the
 * dependent model dropdown appearing/repopulating.
 *
 * Validate fast (assertions only, no dwells):
 *   WEB_CHAT_PACE=0 pnpm exec playwright test harness-picker-video --project=chromium
 * Record at watch-speed:
 *   pnpm exec playwright test harness-picker-video --project=chromium
 *
 * The harness suppresses Playwright stdout — per-step context + any failure is
 * written to .artifacts/harness-picker/ERROR.txt and the NN-*.png screenshots.
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import path from "path";
import {
  startWebServer,
  repoRoot,
  makeShot,
  prepareVideoDir,
  saveVideoAsMp4,
  cinematicGoto,
  pacedClick,
  dwell,
  SETTLE_MS,
  type WebServer,
} from "./_helpers/server.js";
import { makeCaption, captureDiagnostics } from "./_helpers/demo.js";

const ADDR = "127.0.0.1:7752";
const STORY_DIR = path.join(repoRoot, "stories", "bugfix");
const FLOW = path.join(STORY_DIR, "flows", "happy_llm.yaml");
const CONFIG = path.join(repoRoot, "tools", "runstatus", "tests", "playwright", "fixtures", "harness.kitsoki.yaml");
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "harness-picker");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");

let server: WebServer;

test.beforeAll(async () => {
  prepareVideoDir(VIDEO_DIR);
  server = await startWebServer({
    addr: ADDR,
    flow: FLOW,
    storiesDir: STORY_DIR,
    config: CONFIG,
    extraEnv: { SYNTHETIC_API_KEY: "demo-key-not-real" },
  });
});

test.afterAll(() => server?.stop());

test("harness profile + model picker feature-spotlight video", async () => {
  test.setTimeout(300000);
  const browser: Browser = await chromium.launch({ headless: true });
  const context: BrowserContext = await browser.newContext({
    viewport: { width: 1600, height: 900 },
    deviceScaleFactor: 2,
    recordVideo: { dir: VIDEO_DIR, size: { width: 1600, height: 900 } },
  });
  const page: Page = await context.newPage();
  const video = page.video();
  const shot = makeShot(ARTIFACT_DIR);
  const diag = captureDiagnostics(page, ARTIFACT_DIR);

  const provider = () => page.getByTestId("provider-select");
  const model = () => page.getByTestId("model-select");

  try {
    // Create the run off-camera so the camera opens straight on the observer.
    const { session_id: sid } = await server.rpc<{ session_id: string }>(
      "runstatus.session.new",
      { story_path: path.join(STORY_DIR, "app.yaml") },
    );

    diag.mark("open-observer");
    await cinematicGoto(page, `${server.base}/#/s/${sid}`, { waitForTestId: "breadcrumb" });
    const beat = await makeCaption(page, 4200);
    // The picker lives in the TOP-RIGHT header; move the caption to the bottom so
    // it never overlaps the element the video is demonstrating.
    await page.addStyleTag({
      content:
        `#demo-caption{top:auto !important;bottom:30px !important;}` +
        // Spotlight the picker so the eye lands on the feature under demo.
        `[data-testid="harness-picker"]{outline:2px solid #fbbf24;outline-offset:4px;border-radius:6px;}`,
    });

    // ── Scene 1: the picker, default profile active ──────────────────────────
    diag.mark("scene-default");
    await expect(page.getByTestId("harness-picker")).toBeVisible({ timeout: 15000 });
    await expect(provider()).toHaveValue("claude-native");
    // claude-native declares no model catalog → the model dropdown is hidden.
    await expect(model()).toHaveCount(0);
    await beat(
      "Pick the harness live",
      "Every session opens on its default profile — here, claude-native (your Anthropic subscription).",
    );
    await shot(page, "default-claude-native");

    // ── Scene 2: switch provider → synthetic-claude, model dropdown appears ───
    diag.mark("scene-synthetic-claude");
    await beat("Switch the provider", "One dropdown swaps the whole backend + endpoint. Takes effect next turn.");
    await dwell(page, SETTLE_MS);
    await provider().selectOption("synthetic-claude");
    // The dependent model dropdown repopulates from this profile's catalog.
    await expect(model()).toBeVisible({ timeout: 8000 });
    await expect(provider()).toHaveValue("synthetic-claude");
    await beat(
      "synthetic-claude",
      "claude-code pointed at synthetic.new — and a model catalog appears beside it.",
    );
    await shot(page, "synthetic-claude");

    // ── Scene 3: pick a model from the catalog ───────────────────────────────
    diag.mark("scene-model");
    await beat("Pick the model", "The model dropdown lists this profile's catalog.");
    await dwell(page, SETTLE_MS);
    await model().selectOption("hf:meta-llama/Llama-3.3-70B-Instruct");
    await expect(model()).toHaveValue("hf:meta-llama/Llama-3.3-70B-Instruct");
    await beat("Llama-3.3-70B", "Selected — the next oracle call uses it.");
    await shot(page, "model-llama");

    // ── Scene 4: switch to a different backend entirely (codex) ──────────────
    diag.mark("scene-codex");
    await beat("A different backend", "synthetic-codex forks the codex CLI instead of claude.");
    await dwell(page, SETTLE_MS);
    await provider().selectOption("synthetic-codex");
    await expect(provider()).toHaveValue("synthetic-codex");
    // synthetic-codex declares no catalog → model dropdown hides again.
    await expect(model()).toHaveCount(0);
    await beat("synthetic-codex", "Codex backend, synthetic.new endpoint — no catalog, so it uses the backend default.");
    await shot(page, "synthetic-codex");

    // ── Scene 5: back home to the native profile ─────────────────────────────
    diag.mark("scene-back");
    await provider().selectOption("claude-native");
    await expect(provider()).toHaveValue("claude-native");
    await beat("Back to native", "Switch back any time — the active selection always shows in the header.");
    await shot(page, "back-to-native");
    await dwell(page, SETTLE_MS);
  } catch (err) {
    diag.onThrow(err);
    throw err;
  } finally {
    await context.close();
    await saveVideoAsMp4(video, ARTIFACT_DIR, "harness-picker-demo");
    await browser.close();
  }
});
