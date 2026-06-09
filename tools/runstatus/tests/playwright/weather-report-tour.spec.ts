/**
 * Weather & Climate — guided-tour video of the host.starlark.run example story.
 *
 * Drives a real `kitsoki web` server in the deterministic no-LLM posture
 * (`--flow tour.yaml`) and records a video + per-scene screenshots to
 * .artifacts/weather-report-tour/. The flow's starlark_http_cassette: makes the
 * REAL host.starlark.run handler run with its ctx.http GETs replayed from a
 * cassette — so the trace shows genuine host.starlark.run invocations and the
 * __http_exchanges summary, with no LLM and no socket.
 *
 * The tour: free-text "forecast Tokyo" → the 5-day report + the trace lighting
 * up with two GETs → "climate Oslo" (a second look-up in the other mode, from
 * the report room) → the 2023 monthly profile → "forecast Zzqxville" (fails
 * cleanly on an unknown place) → back → quit.
 *
 * Validate fast (no dwells):
 *   WEB_CHAT_PACE=0 pnpm exec playwright test weather-report-tour --project=chromium
 * Record at watch-speed:
 *   pnpm exec playwright test weather-report-tour --project=chromium
 *
 * Requires a fresh binary: `make build && cp ./kitsoki bin/kitsoki`.
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import path from "path";
import fs from "fs";
import { startWebServer, repoRoot, makeShot, waitForState, PACE, type WebServer } from "./_helpers/server.js";

const ADDR = "127.0.0.1:7749";
const STORY_DIR = path.join(repoRoot, "stories", "weather-report");
const FLOW = path.join(STORY_DIR, "flows", "tour.yaml");
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "weather-report-tour");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");

let server: WebServer;

test.beforeAll(async () => {
  fs.mkdirSync(VIDEO_DIR, { recursive: true });
  // Clear stale recordings so the stabilize step below can't pick an old run's
  // webm (Playwright names each capture by a random hash).
  for (const f of fs.readdirSync(VIDEO_DIR)) {
    if (f.endsWith(".webm")) fs.rmSync(path.join(VIDEO_DIR, f));
  }
  server = await startWebServer({ addr: ADDR, flow: FLOW, storiesDir: STORY_DIR });
});

test.afterAll(() => server?.stop());

const dwell = (page: Page, ms: number) => page.waitForTimeout(Math.round(ms * PACE));

/** Fill a `choice:`-param free-text field for `intent` and submit it. */
async function submitParam(page: Page, intent: string, value: string): Promise<void> {
  const form = page.locator(`form[data-intent="${intent}"]`);
  await expect(form).toBeVisible({ timeout: 8000 });
  await form.locator("input").fill(value);
  await dwell(page, 700);
  await form.locator('button[type="submit"]').click();
}

test("weather-report tour video (no-LLM, real host.starlark.run replay)", async () => {
  test.setTimeout(300000);
  const browser: Browser = await chromium.launch({ headless: true });
  const context: BrowserContext = await browser.newContext({
    viewport: { width: 1600, height: 900 },
    recordVideo: { dir: VIDEO_DIR, size: { width: 1600, height: 900 } },
  });
  const page: Page = await context.newPage();
  const shot = makeShot(ARTIFACT_DIR);

  try {
    // ── Scene 1: Home ───────────────────────────────────────────────────────
    await page.goto(`${server.base}/#/`);
    await expect(page.getByTestId("home-view")).toBeVisible({ timeout: 15000 });
    await dwell(page, 1500);
    await shot(page, "home");

    // ── Scene 2: Start the weather-report session ───────────────────────────
    const card = page.locator("[data-testid='story-card']").filter({ hasText: /weather/i });
    await expect(card).toBeVisible({ timeout: 8000 });
    await dwell(page, 800);
    await card.getByTestId("new-session-btn").click();
    await page.waitForURL(/#\/s\/[0-9a-f-]{36}\/chat$/, { timeout: 15000 });
    await waitForState(page, "lobby");
    await dwell(page, 1500);
    await shot(page, "lobby");

    // ── Scene 3: forecast Tokyo (free-text input → 5-day report) ────────────
    await submitParam(page, "forecast", "Tokyo");
    await waitForState(page, "report");
    // Assert on text unique to the rendered report (the resolved place); the
    // bare "5-day forecast" string also appears as an intent-picker option.
    await expect(page.getByText("Tokyo, Japan")).toBeVisible({ timeout: 10000 });
    await dwell(page, 2000);
    await shot(page, "forecast-tokyo");

    // ── Scene 4: the trace — real host.starlark.run + the two GETs ──────────
    // The interactive view keeps a live trace timeline + mermaid diagram beside
    // the chat. Bring them into frame and dwell so the recording shows the
    // host.starlark.run invocations and their __http_exchanges.
    const timeline = page.getByTestId("trace-timeline");
    await expect(timeline).toBeVisible({ timeout: 8000 });
    await timeline.scrollIntoViewIfNeeded();
    await dwell(page, 1500);
    // Expand a host.starlark.run trace row to reveal its payload (the
    // {method,url,status} __http_exchanges summary the script's GETs rode).
    const hostRow = timeline.locator('.trace-timeline__row:has([data-subsystem="host"])').first();
    if (await hostRow.count()) {
      await hostRow.scrollIntoViewIfNeeded();
      await hostRow.click();
      await dwell(page, 2500);
    }
    await shot(page, "trace-forecast");

    // ── Scene 5: climate Oslo (second look-up, other mode, from the report) ─
    await submitParam(page, "climate", "Oslo");
    await expect(page.getByText("2023 climate profile")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("Oslo, Norway")).toBeVisible({ timeout: 10000 });
    await dwell(page, 2000);
    await shot(page, "climate-oslo");
    await timeline.scrollIntoViewIfNeeded();
    await dwell(page, 2000);
    await shot(page, "trace-climate");

    // ── Scene 6: forecast Zzqxville (fails cleanly on an unknown place) ──────
    await submitParam(page, "forecast", "Zzqxville");
    await waitForState(page, "failed");
    await expect(page.getByText("no place found matching")).toBeVisible({ timeout: 10000 });
    await dwell(page, 2000);
    await shot(page, "unknown-place");

    // ── Scene 7: back → lobby, then quit ────────────────────────────────────
    await page.getByTestId("intent-btn-back").click();
    await waitForState(page, "lobby");
    await dwell(page, 1200);
    await shot(page, "back-to-lobby");
    await page.getByTestId("intent-btn-quit").click();
    await waitForState(page, "ended", 8000);
    await dwell(page, 1200);
    await shot(page, "ended");
  } finally {
    await context.close();
    await browser.close();
  }

  // Stabilize the recorded video name for any downstream render scripts. Pick
  // the NEWEST webm by mtime (the run just finished) — never readdir order,
  // which is alphabetical by Playwright's random hash and would grab a stale
  // capture if any survived.
  const vids = fs
    .readdirSync(VIDEO_DIR)
    .filter((f) => f.endsWith(".webm"))
    .map((f) => ({ f, m: fs.statSync(path.join(VIDEO_DIR, f)).mtimeMs }))
    .sort((a, b) => b.m - a.m);
  if (vids.length > 0) {
    const stable = path.join(ARTIFACT_DIR, "weather-report-tour.webm");
    fs.copyFileSync(path.join(VIDEO_DIR, vids[0].f), stable);
    console.log(`[weather-tour] demo: ${stable}`);
  }
  const pngs = fs.readdirSync(ARTIFACT_DIR).filter((f) => f.endsWith(".png"));
  console.log(`[weather-tour] screenshots (${pngs.length}) in ${ARTIFACT_DIR}`);
});
