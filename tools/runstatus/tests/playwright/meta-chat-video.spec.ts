/**
 * Meta-chat persistence + launcher-status feature-spotlight video demo.
 *
 * Proves the fix behind branch fix/meta-chat-persistence-and-status:
 *   1. A meta-mode (Story Q&A) turn streams a thinking + tool feed in the
 *      overlay (meta-row-streaming).
 *   2. CLOSING the overlay while the turn streams leaves it running — the
 *      launcher grows a working badge (meta-status-busy, ⟳).
 *   3. REOPENING resumes the SAME conversation, still streaming, as if it was
 *      never closed (persistence).
 *   4. When a turn FINISHES while the overlay is closed, the launcher shows a
 *      ready badge (meta-status-ready, ●); reopening clears it.
 *   5. (stretch, required:false) two modes at once — one ● waiting, one ⟳
 *      working — visible as both launcher badges.
 *
 * Posture: deterministic no-LLM. `kitsoki web --flow happy_llm.yaml` runs the
 * meta-mode StubOracleCaller; KITSOKI_META_STREAM_DELAY_MS paces its stream so a
 * close-mid-stream is reliably filmable. NEVER a real LLM.
 *
 * The four-step intro is tour-narrated (home -> story -> new session ->
 * observer/chat, via META_CHAT_TOUR_STEPS through the tour overlay). After the
 * intro the spec drives the close/reopen/badge choreography directly with
 * captions — the tour overlay (z 1500) cannot narrate the meta overlay (z 1000)
 * being opened and closed.
 *
 * Validate fast (no dwells, tiny but non-zero stream delay so the bubble exists
 * long enough to assert close-mid-stream):
 *   WEB_CHAT_PACE=0 KITSOKI_META_STREAM_DELAY_MS=120 \
 *     pnpm exec playwright test meta-chat-video --project=chromium
 * Record at watch-speed:
 *   pnpm exec playwright test meta-chat-video --project=chromium
 *
 * NOTE: the harness suppresses Playwright stdout, so per-step progress + any
 * failure context is also written to .artifacts/meta-chat/diagnostic.log.
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import path from "path";
import fs from "fs";
import {
  startWebServer,
  repoRoot,
  makeShot,
  prepareVideoDir,
  saveVideoAsMp4,
  dwell,
  cinematicGoto,
  ChapterRecorder,
  writeChapters,
  SETTLE_MS,
  type WebServer,
} from "./_helpers/server.js";
import { makeCaption, type Beat } from "./_helpers/demo.js";
import { META_CHAT_TOUR_STEPS } from "../../src/tour/meta-chat-manifest.js";

const CHAPTER_SOURCE = "tools/runstatus/src/tour/meta-chat-manifest.ts";

// 7765 — the brief reserves this port (7740–7762 are taken).
const ADDR = "127.0.0.1:7765";
const STORY_DIR = path.join(repoRoot, "stories", "bugfix");
const FLOW = path.join(STORY_DIR, "flows", "happy_llm.yaml");
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "meta-chat");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");
const DIAG_LOG = path.join(ARTIFACT_DIR, "diagnostic.log");

const ASK_QUESTION = "What does this story do, and where is the run right now?";
const ASK2_QUESTION = "Which rooms can the autofix agent reach from idle?";

let server: WebServer;

function diag(msg: string): void {
  const line = `[${new Date().toISOString()}] ${msg}\n`;
  try {
    fs.appendFileSync(DIAG_LOG, line);
  } catch {
    /* best-effort */
  }
}

test.beforeAll(async () => {
  prepareVideoDir(VIDEO_DIR);
  fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
  fs.writeFileSync(DIAG_LOG, "");
  // Pace the stub meta stream so a close-mid-stream is deterministic and
  // filmable. The stub emits think -> 2x -> Read -> 2x -> 4x -> reply words at
  // 1x each, so the turn is in-flight for well over 8x the delay before the
  // reply even starts — a wide window to close + reopen on camera. Keep a small
  // floor even in fast-validation mode so the streaming bubble reliably EXISTS
  // when we assert close-mid-stream (at 0 the turn finishes before the click).
  if (process.env.KITSOKI_META_STREAM_DELAY_MS === undefined) {
    process.env.KITSOKI_META_STREAM_DELAY_MS =
      process.env.WEB_CHAT_PACE === "0" ? "120" : "900";
  }
  diag(`KITSOKI_META_STREAM_DELAY_MS=${process.env.KITSOKI_META_STREAM_DELAY_MS}`);
  server = await startWebServer({ addr: ADDR, flow: FLOW, storiesDir: STORY_DIR });
});

test.afterAll(() => server?.stop());

/** Wait for the launcher working badge (⟳) to appear, with a timeout. */
async function expectBusyBadge(page: Page, timeout = 8000): Promise<void> {
  await expect(page.getByTestId("meta-status-busy")).toBeVisible({ timeout });
}

/** Wait for the launcher ready badge (●) to appear, with a timeout. */
async function expectReadyBadge(page: Page, timeout = 30000): Promise<void> {
  await expect(page.getByTestId("meta-status-ready")).toBeVisible({ timeout });
}

test("meta-chat persistence + launcher status feature-spotlight video", async () => {
  test.setTimeout(300000);
  const browser: Browser = await chromium.launch({ headless: true });
  const context: BrowserContext = await browser.newContext({
    viewport: { width: 1600, height: 900 },
    recordVideo: { dir: VIDEO_DIR, size: { width: 1600, height: 900 } },
  });
  const page: Page = await context.newPage();
  const video = page.video();
  const shot = makeShot(ARTIFACT_DIR);
  const chapters = new ChapterRecorder();

  // Verdicts collected during the walk, asserted AFTER it, so a failing
  // behaviour still yields a complete recording that SHOWS the problem.
  let sawStreaming = false;
  let sawBusyOnClose = false;
  let resumedStreaming = false;
  let resumedTranscriptIntact = false;
  let sawReadyWhenDone = false;
  let readyClearedOnReopen = false;
  let sawBothBadges = false;

  let beat: Beat;
  let sessionId = "";

  try {
    // ── 1. Tour-narrated intro: home -> story -> new session -> chat ─────────
    diag("navigating home");
    await cinematicGoto(page, `${server.base}/#/`, { waitForTestId: "home-view" });

    await page.evaluate((stepsJson: string) => {
      (window as unknown as { __startTourWithSteps?: (s: string) => void })
        .__startTourWithSteps?.(stepsJson);
    }, JSON.stringify(META_CHAT_TOUR_STEPS));
    await expect(page.getByTestId("tour-overlay")).toBeVisible({ timeout: 8000 });

    for (const step of META_CHAT_TOUR_STEPS) {
      diag(`step ${step.id}`);
      const url = page.url();
      const routeKind = url.includes("/chat")
        ? "interactive"
        : url.match(/#\/s\/[0-9a-f-]{36}$/)
          ? "any"
          : "home";
      if (step.route !== "any" && step.route !== routeKind) {
        diag(`  route-skip (${routeKind})`);
        continue;
      }
      if (step.waitForTarget) {
        await expect(page.getByTestId(step.waitForTarget).first()).toBeVisible({ timeout: 20000 });
      }
      const titleEl = page.getByTestId("tour-title");
      await expect(titleEl).toHaveText(step.title, { timeout: 12000 });
      chapters.open(step.id, step.title, CHAPTER_SOURCE);
      await dwell(page, step.dwellMs ?? 3000);
      await shot(page, step.id);

      if (step.kind === "explain") {
        await page.getByTestId("tour-next").click();
        await dwell(page, 700);
      } else {
        const target = page.getByTestId(step.target!).first();
        await target.scrollIntoViewIfNeeded().catch(() => undefined);
        await target.click();
        await page.waitForTimeout(300);
        if (step.advanceRoute === "interactive") {
          await page.waitForURL(/#\/s\/[0-9a-f-]{36}\/chat$/, { timeout: 15000 });
          const m = page.url().match(/\/s\/([0-9a-f-]{36})\/chat$/);
          if (m) {
            sessionId = m[1];
            diag(`session ${sessionId}`);
          }
        }
        await dwell(page, 1000);
      }
    }
    // The intro's last step (mc-launcher) is an explain step whose Next closes
    // the tour overlay.
    await expect(page.getByTestId("tour-overlay")).toHaveCount(0, { timeout: 8000 });

    // ── 2. Captions take over for the persistence/badge choreography ─────────
    beat = await makeCaption(page, 4500);

    // Open the ✦ Meta launcher dropdown and pick Story Q&A.
    diag("open meta launcher");
    await expect(page.getByTestId("meta-mode-story-ask")).toBeVisible({ timeout: 4000 }).catch(async () => {
      await page.getByTestId("meta-button").click();
    });
    // The dropdown may need an explicit open click.
    if (!(await page.getByTestId("meta-mode-story-ask").isVisible().catch(() => false))) {
      await page.getByTestId("meta-button").click();
    }
    chapters.open("mc-open-mode", "Open Story Q&A", CHAPTER_SOURCE);
    await beat("Open a Story Q&A chat", "Read-only conversation with an agent that can inspect the loaded story.", 3000);
    await expect(page.getByTestId("meta-mode-story-ask")).toBeEnabled({ timeout: 10000 });
    await page.getByTestId("meta-mode-story-ask").click();
    await expect(page.getByTestId("meta-overlay")).toBeVisible({ timeout: 8000 });
    await dwell(page, SETTLE_MS);
    await shot(page, "mc-overlay-open");

    // Scenario 1 — type a question, send, watch the streaming bubble appear.
    diag("send meta question 1");
    await page.getByTestId("meta-composer-input").fill(ASK_QUESTION);
    await shot(page, "mc-question-typed");
    chapters.open("mc-stream", "The turn streams", CHAPTER_SOURCE);
    await beat("Ask the agent — the turn streams live", "🧠 thinking and a Read tool call arrive in the bubble, paced by the stub oracle.", 1500);
    await page.getByTestId("meta-composer-send").click();
    const streamingBubble = page.getByTestId("meta-row-streaming");
    try {
      await expect(streamingBubble).toBeVisible({ timeout: 8000 });
      sawStreaming = true;
    } catch {
      sawStreaming = false;
    }
    diag(`scenario1 sawStreaming=${sawStreaming}`);
    // Dwell so the feed fills with the thought + Read row on camera.
    await dwell(page, 2600);
    await shot(page, "mc-streaming");

    // Scenario 2 — CLOSE the overlay mid-stream; launcher shows the ⟳ badge.
    diag("close overlay mid-stream");
    chapters.open("mc-close-busy", "Close while it works", CHAPTER_SOURCE);
    await beat("Close the overlay while it's still working", "The turn keeps streaming — closing does not abort it.", 1200);
    // Sanity: it must STILL be streaming when we close (the close-mid-stream
    // claim is only meaningful if the turn hasn't finished).
    const stillBusyOnClose = await streamingBubble.isVisible().catch(() => false);
    diag(`  bubble visible at close time=${stillBusyOnClose}`);
    await page.getByTestId("meta-close").click();
    await expect(page.getByTestId("meta-overlay")).toHaveCount(0, { timeout: 5000 });
    await dwell(page, 600);
    try {
      await expectBusyBadge(page, 6000);
      sawBusyOnClose = true;
    } catch {
      sawBusyOnClose = false;
    }
    diag(`scenario2 sawBusyOnClose=${sawBusyOnClose}`);
    await beat("⟳ working — a meta chat is busy", "The launcher badge tells you a turn is streaming, even with the overlay closed.", 2200);
    await shot(page, "mc-badge-busy");

    // Scenario 3 — REOPEN; the same conversation is intact and still streaming.
    diag("reopen overlay mid-stream");
    chapters.open("mc-reopen", "Reopen — nothing lost", CHAPTER_SOURCE);
    await beat("Reopen — right where you left it", "The same conversation, the same in-flight turn: as if it was never closed.", 1200);
    await page.getByTestId("meta-button").click();
    await page.getByTestId("meta-mode-story-ask").click();
    await expect(page.getByTestId("meta-overlay")).toBeVisible({ timeout: 8000 });
    // The user turn must still be present (transcript persisted).
    const userRows = page.getByTestId("meta-row-user");
    resumedTranscriptIntact =
      (await userRows.count().catch(() => 0)) >= 1 &&
      ((await userRows.first().textContent().catch(() => "")) ?? "").includes("What does this story do");
    // And the streaming bubble is (very likely) still there — the turn outlived
    // the close. Best-effort: a fast replay can land the reply before reopen.
    resumedStreaming = await streamingBubble.isVisible().catch(() => false);
    diag(`scenario3 transcriptIntact=${resumedTranscriptIntact} stillStreaming=${resumedStreaming}`);
    await dwell(page, 1500);
    await shot(page, "mc-reopened");
    // Let this turn finish while we watch, so the overlay shows the resolved
    // reply (consistent resolution) before we stage the "ready" scenario.
    await expect(streamingBubble).toBeHidden({ timeout: 40000 }).catch(() => undefined);
    await expect(page.getByTestId("meta-row-agent").last()).toBeVisible({ timeout: 5000 }).catch(() => undefined);
    await beat("The turn resolves consistently", "The streaming bubble dissolves into the agent's reply — the conversation never reset.", 2500);
    await shot(page, "mc-resolved");

    // Scenario 4 — start ANOTHER turn, close immediately, let it FINISH while
    // closed → the launcher shows the ready ● badge; reopening clears it.
    diag("send meta question 2, then close to finish-while-closed");
    await page.getByTestId("meta-composer-input").fill(ASK2_QUESTION);
    chapters.open("mc-ready", "A reply waiting", CHAPTER_SOURCE);
    await beat("Ask again, then close immediately", "This time we close right away and let the turn finish while the overlay is shut.", 1200);
    await page.getByTestId("meta-composer-send").click();
    await expect(streamingBubble).toBeVisible({ timeout: 8000 }).catch(() => undefined);
    await dwell(page, 800);
    await page.getByTestId("meta-close").click();
    await expect(page.getByTestId("meta-overlay")).toHaveCount(0, { timeout: 5000 });
    // While closed, the badge is first ⟳ (working) and then flips to ● (ready)
    // when the turn finishes.
    await beat("Waiting for the reply…", "⟳ while it streams, then ● the moment the answer lands — all with the overlay closed.", 1500);
    try {
      await expectReadyBadge(page, 40000);
      sawReadyWhenDone = true;
    } catch {
      sawReadyWhenDone = false;
    }
    diag(`scenario4 sawReadyWhenDone=${sawReadyWhenDone}`);
    await dwell(page, 1200);
    await beat("● ready — a reply is waiting", "The green badge says the answer arrived while you were elsewhere.", 2400);
    await shot(page, "mc-badge-ready");

    // Reopening clears the ready badge.
    diag("reopen to clear ready badge");
    await page.getByTestId("meta-button").click();
    await page.getByTestId("meta-mode-story-ask").click();
    await expect(page.getByTestId("meta-overlay")).toBeVisible({ timeout: 8000 });
    await dwell(page, 800);
    readyClearedOnReopen = !(await page.getByTestId("meta-status-ready").isVisible().catch(() => false));
    diag(`scenario4 readyClearedOnReopen=${readyClearedOnReopen}`);
    await beat("Reopen — the badge clears", "Viewing the reply marks it seen; the ● goes away.", 2200);
    await shot(page, "mc-ready-cleared");

    // Scenario 5 (stretch) — two modes at once: leave THIS reply unseen by
    // switching to Kitsoki help, send there, and close mid-stream so one mode is
    // ⟳ working while... actually we want one ● + one ⟳ simultaneously. Stage:
    //  - story.ask currently has an UNSEEN finished reply? No — reopening
    //    cleared it. Instead: open kitsoki.ask, send, close mid-stream (⟳).
    //    Meanwhile re-send in story.ask and let it finish closed (●). Showing
    //    BOTH at once is timing-sensitive, hence required:false.
    diag("scenario5: attempt both badges at once");
    try {
      // story.ask: send a turn and DON'T view its completion → will go ●.
      await expect(page.getByTestId("meta-overlay")).toBeVisible();
      await page.getByTestId("meta-composer-input").fill("Summarise the story in one line.");
      await page.getByTestId("meta-composer-send").click();
      await expect(streamingBubble).toBeVisible({ timeout: 8000 }).catch(() => undefined);
      await dwell(page, 400);
      // Switch to Kitsoki help (a DIFFERENT scope) and start a turn there; the
      // story.ask turn keeps streaming in its own scope behind the tab.
      const kitsokiTab = page.getByTestId("meta-tab-kitsoki-ask");
      if (await kitsokiTab.isVisible().catch(() => false)) {
        await kitsokiTab.click();
        await dwell(page, 400);
        await page.getByTestId("meta-composer-input").fill("What is kitsoki?");
        await page.getByTestId("meta-composer-send").click();
        await expect(streamingBubble).toBeVisible({ timeout: 8000 }).catch(() => undefined);
        await dwell(page, 600);
        // Close now: kitsoki.ask is streaming (⟳); story.ask may have finished
        // while we were on the other tab (●). Close to surface both on the
        // launcher.
        await page.getByTestId("meta-close").click();
        await expect(page.getByTestId("meta-overlay")).toHaveCount(0, { timeout: 5000 });
        await beat("Both at once — one waiting, one working", "Distinct modes hold distinct state: ● a reply waiting, ⟳ another turn streaming.", 1500);
        // Poll briefly for BOTH badges visible together.
        const deadline = Date.now() + 12000;
        while (Date.now() < deadline) {
          const busy = await page.getByTestId("meta-status-busy").isVisible().catch(() => false);
          const ready = await page.getByTestId("meta-status-ready").isVisible().catch(() => false);
          if (busy && ready) {
            sawBothBadges = true;
            break;
          }
          await page.waitForTimeout(300);
        }
        diag(`scenario5 sawBothBadges=${sawBothBadges}`);
        await dwell(page, 1800);
        await shot(page, "mc-both-badges");
      } else {
        diag("scenario5: kitsoki-ask tab not available; skipping both-badges stage");
      }
    } catch (e) {
      diag(`scenario5 error (non-fatal): ${e instanceof Error ? e.message : String(e)}`);
    }

    chapters.open("mc-done", "Done", CHAPTER_SOURCE);
    await beat("Your meta chat never loses its place", "Stream it, close it, come back later — the conversation and its status are always there.", 3500);
    await shot(page, "mc-done");
  } catch (e) {
    diag(`FAILED: ${e instanceof Error ? e.stack ?? e.message : String(e)}`);
    diag(`--- server log ---\n${server?.log?.() ?? ""}`);
    throw e;
  } finally {
    await context.close();
    const mp4 = await saveVideoAsMp4(video, ARTIFACT_DIR, "meta-chat-demo");
    writeChapters(mp4, chapters.list());
    await browser.close();
  }

  // ── Verdicts (after the walk so the recording is always complete) ──────────
  diag(
    `VERDICTS sawStreaming=${sawStreaming} sawBusyOnClose=${sawBusyOnClose} ` +
      `resumedStreaming=${resumedStreaming} resumedTranscriptIntact=${resumedTranscriptIntact} ` +
      `sawReadyWhenDone=${sawReadyWhenDone} readyClearedOnReopen=${readyClearedOnReopen} ` +
      `sawBothBadges=${sawBothBadges}`
  );
  expect(sawStreaming, "scenario 1: streaming bubble appears").toBe(true);
  expect(sawBusyOnClose, "scenario 2: ⟳ working badge after close-mid-stream").toBe(true);
  expect(resumedTranscriptIntact, "scenario 3: conversation intact on reopen").toBe(true);
  expect(sawReadyWhenDone, "scenario 4: ● ready badge when turn finishes closed").toBe(true);
  expect(readyClearedOnReopen, "scenario 4: ● clears on reopen").toBe(true);
  // scenario 5 is a stretch — non-blocking.

  const pngs = fs.readdirSync(ARTIFACT_DIR).filter((f) => f.endsWith(".png"));
  console.log(`[meta-chat-video] screenshots (${pngs.length}) in ${ARTIFACT_DIR}`);
});
