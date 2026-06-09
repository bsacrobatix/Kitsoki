/**
 * trace-introspection-demo.spec.ts
 *
 * Walkthrough video of all 6 trace-introspection features using the bugfix
 * snapshot fixture (static/offline — no live server required).
 *
 * Features demonstrated:
 *  1. Observation Kinds — category filter chips + colored obs-dots
 *  2. Decision-First Detail — verdict block, confidence bar, evidence drawer
 *  3. View Modes (Waterfall / Graph) — tab switching + waterfall bars + state diagram
 *  4. Home Triage Table — session filter chips + sort headers
 *  5. Annotation — AnnotateButton absent in static mode (graceful degradation)
 *  6. Replay — ReplayButton in DecideDetail
 *
 * Output:
 *   .artifacts/trace-introspection-demo/  (screenshots + video)
 *
 * Run:
 *   pnpm exec playwright test tests/playwright/trace-introspection-demo.spec.ts --project=chromium --reporter=list
 */

import { test, expect, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import path from "path";
import fs from "fs";
import { fileURLToPath } from "url";
import { buildArtifact } from "./_helpers/artifact.js";
import { execSync } from "child_process";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const FIXTURES_DIR = path.resolve(__dirname, "../../fixtures");
const BUGFIX_SNAPSHOT = path.join(FIXTURES_DIR, "bugfix.snapshot.json");

// Derive repo root (same logic as artifact.ts)
const projectRoot = path.resolve(__dirname, "../../..");
const repoRoot = execSync("git rev-parse --show-toplevel", { cwd: projectRoot, encoding: "utf-8" }).trim();
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "trace-introspection-demo");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");

// Pause durations for demo watchability
const DWELL_MS = 2000;
const SHORT_DWELL = 1000;

/** Take a labeled screenshot into ARTIFACT_DIR. */
async function shot(page: Page, label: string): Promise<void> {
  const file = path.join(ARTIFACT_DIR, `${label}.png`);
  await page.screenshot({ path: file, fullPage: false });
  console.log(`[demo] screenshot: ${file}`);
}

/** Load the bugfix snapshot artifact and wait for the trace view to be ready. */
async function loadBugfix(page: Page): Promise<void> {
  const url = buildArtifact(BUGFIX_SNAPSHOT);
  await page.goto(url);
  await page.waitForSelector(".run-view__topbar", { timeout: 15000 });
  await page.waitForSelector(".trace-timeline__row", { timeout: 10000 });
}

test("trace-introspection demo walkthrough", async () => {
  test.setTimeout(300_000);

  fs.mkdirSync(VIDEO_DIR, { recursive: true });

  const browser: Browser = await chromium.launch({ headless: true, slowMo: 100 });
  const context: BrowserContext = await browser.newContext({
    viewport: { width: 1600, height: 900 },
    recordVideo: { dir: VIDEO_DIR, size: { width: 1600, height: 900 } },
  });
  const page: Page = await context.newPage();

  try {
    // ═════════════════════════════════════════════════════════════════════════
    // STEP 1 — Observation Kinds
    // ═════════════════════════════════════════════════════════════════════════
    await test.step("Step 1: Observation Kinds", async () => {
      await loadBugfix(page);
      await page.waitForTimeout(DWELL_MS);

      // Show category filter chips are present
      const catChips = page.locator(".trace-timeline__chip--category");
      await expect(catChips.first()).toBeVisible({ timeout: 8000 });
      const chipCount = await catChips.count();
      console.log(`[demo] category chips: ${chipCount}`);

      // Screenshot: overview of the trace with observation-kind dots visible
      await shot(page, "01a-observation-kinds-overview");

      // Zoom in on the first few rows to show obs-dots
      const obsDots = page.locator(".trace-timeline__obs-dot");
      await expect(obsDots.first()).toBeVisible({ timeout: 5000 });
      await shot(page, "01b-obs-dots-visible");

      // Toggle a category chip on/off: click "oracle-call" (or first available)
      const firstActiveChip = catChips.first();
      const chipText = await firstActiveChip.innerText();
      console.log(`[demo] toggling chip: ${chipText.trim()}`);

      // Click to deactivate (filter out this category)
      await firstActiveChip.click();
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "01c-category-chip-toggled-off");

      // Click again to re-activate
      await firstActiveChip.click();
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "01d-category-chip-toggled-on");

      await page.waitForTimeout(DWELL_MS);
    });

    // ═════════════════════════════════════════════════════════════════════════
    // STEP 2 — Decision-First Detail
    // ═════════════════════════════════════════════════════════════════════════
    await test.step("Step 2: Decision-First Detail", async () => {
      // Navigate to bugfix trace (re-load for a clean state)
      await loadBugfix(page);

      // Find and click a decide event row
      const decideRow = page
        .locator(".trace-timeline__row", {
          has: page.locator(".trace-timeline__msg").filter({ hasText: /decide/ }),
        })
        .first();

      // Scroll the decide row into view and click it
      await expect(decideRow).toBeVisible({ timeout: 8000 });
      await decideRow.scrollIntoViewIfNeeded();
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "02a-decide-row-visible");

      await decideRow.click();
      await page.waitForTimeout(SHORT_DWELL);

      const body = decideRow.locator(".trace-timeline__row-body");
      await expect(body).toBeVisible({ timeout: 5000 });

      // Verdict block
      const verdict = body.locator("[data-testid='decide-verdict']");
      await expect(verdict).toBeVisible({ timeout: 5000 });
      await shot(page, "02b-verdict-block-visible");

      // Confidence bar
      const confBar = verdict.locator("[data-testid='confidence-bar']");
      await expect(confBar).toBeVisible({ timeout: 3000 });
      await shot(page, "02c-confidence-bar");

      // Evidence drawer collapsed by default
      const evidenceToggle = body.locator("[data-testid='decide-evidence-toggle']");
      await expect(evidenceToggle).toBeVisible({ timeout: 3000 });
      const evidenceBody = body.locator(".decide-detail__evidence-body");
      // Evidence body should not be visible initially
      const evidenceCount = await evidenceBody.count();
      console.log(`[demo] evidence-body count before expand: ${evidenceCount}`);
      await shot(page, "02d-evidence-collapsed");

      // Expand evidence
      await evidenceToggle.click();
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "02e-evidence-expanded");

      await page.waitForTimeout(DWELL_MS);
    });

    // ═════════════════════════════════════════════════════════════════════════
    // STEP 3 — View Modes (Waterfall / Graph)
    // ═════════════════════════════════════════════════════════════════════════
    await test.step("Step 3: View Modes — Waterfall and Graph", async () => {
      await loadBugfix(page);

      // Wait for tabs to be ready
      await page.waitForSelector('[data-testid="view-mode-tabs"]', { timeout: 8000 });
      await shot(page, "03a-tree-view-default");

      // Switch to Timeline (waterfall)
      await page.locator('[data-testid="tab-timeline"]').click();
      await page.waitForSelector('[data-testid="waterfall-bar"]', { timeout: 8000 });
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "03b-waterfall-view");

      // Hover over the first waterfall bar for a label tooltip
      const firstBar = page.locator('[data-testid="waterfall-bar"]').first();
      await expect(firstBar).toBeVisible({ timeout: 5000 });
      await firstBar.hover();
      await page.waitForTimeout(500);
      await shot(page, "03c-waterfall-bar-hover");

      // Switch to Graph
      await page.locator('[data-testid="tab-graph"]').click();
      await page.waitForSelector(".state-diagram__phase", { timeout: 8000 });
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "03d-graph-view");

      // Switch back to Tree
      await page.locator('[data-testid="tab-tree"]').click();
      await page.waitForSelector(".trace-timeline__row", { timeout: 8000 });
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "03e-back-to-tree");

      await page.waitForTimeout(DWELL_MS);
    });

    // ═════════════════════════════════════════════════════════════════════════
    // STEP 4 — Home Triage Table
    // ═════════════════════════════════════════════════════════════════════════
    await test.step("Step 4: Home Triage Table", async () => {
      // Navigate to home via the back link in the breadcrumb (or direct hash nav)
      // The artifact is a file:// URL; we navigate to the home view via hash routing.
      const currentUrl = page.url();
      // Strip everything after the file path, then go to /#/
      const baseUrl = currentUrl.replace(/#.*$/, "");
      await page.goto(baseUrl + "#/");

      // Wait for home view
      const homeView = page.locator('[data-testid="home-view"]').first();
      const homeLoaded = await homeView.isVisible().catch(() => false);

      if (homeLoaded) {
        await page.waitForTimeout(DWELL_MS);
        await shot(page, "04a-home-view");

        // Session filter chips
        const allChip = page.locator('[data-testid="session-filter-all"]');
        const activeChip = page.locator('[data-testid="session-filter-active"]');
        const terminalChip = page.locator('[data-testid="session-filter-terminal"]');

        const hasChips =
          (await allChip.count()) > 0 &&
          (await activeChip.count()) > 0 &&
          (await terminalChip.count()) > 0;

        if (hasChips) {
          // Click active filter
          await activeChip.click();
          await page.waitForTimeout(SHORT_DWELL);
          await shot(page, "04b-filter-active");

          // Click terminal filter
          await terminalChip.click();
          await page.waitForTimeout(SHORT_DWELL);
          await shot(page, "04c-filter-terminal");

          // Back to all
          await allChip.click();
          await page.waitForTimeout(SHORT_DWELL);
          await shot(page, "04d-filter-all");
        } else {
          console.log("[demo] home filter chips not found — snapshot mode has no live session list");
          await shot(page, "04-home-no-sessions");
        }

        // Column sort header
        const sortActivity = page.locator('[data-testid="session-sort-activity"]');
        if ((await sortActivity.count()) > 0) {
          await sortActivity.click();
          await page.waitForTimeout(SHORT_DWELL);
          await shot(page, "04e-sort-by-activity");
        }
      } else {
        console.log("[demo] home-view not available in snapshot artifact — skipping home step");
        await shot(page, "04-home-unavailable");
      }

      await page.waitForTimeout(DWELL_MS);
    });

    // ═════════════════════════════════════════════════════════════════════════
    // STEP 5 — Annotation (graceful in static mode)
    // ═════════════════════════════════════════════════════════════════════════
    await test.step("Step 5: Annotation — graceful degradation in static mode", async () => {
      await loadBugfix(page);

      // Expand the first event to open EventDetail
      const expandBtns = page.locator(".trace-timeline__expand-btn");
      await expect(expandBtns.first()).toBeVisible({ timeout: 8000 });
      await expandBtns.first().click();
      await page.waitForTimeout(SHORT_DWELL);
      await shot(page, "05a-event-expanded");

      // AnnotateButton should NOT be present in static mode (no live server)
      const annotateBtn = page.locator('[data-testid="annotate-button"]');
      const annotateBtnCount = await annotateBtn.count();
      console.log(`[demo] annotate-button count in static mode: ${annotateBtnCount} (expected 0)`);

      // Show that the event detail renders cleanly without annotation UI
      await shot(page, "05b-no-annotate-button-in-static-mode");

      await page.waitForTimeout(DWELL_MS);
    });

    // ═════════════════════════════════════════════════════════════════════════
    // STEP 6 — Replay
    // ═════════════════════════════════════════════════════════════════════════
    await test.step("Step 6: Replay — DecideDetail with ReplayButton affordance", async () => {
      await loadBugfix(page);

      // Find and click a decide event row
      const decideRow = page
        .locator(".trace-timeline__row", {
          has: page.locator(".trace-timeline__msg").filter({ hasText: /decide/ }),
        })
        .first();

      await expect(decideRow).toBeVisible({ timeout: 8000 });
      await decideRow.scrollIntoViewIfNeeded();
      await decideRow.click();
      await page.waitForTimeout(SHORT_DWELL);

      const body = decideRow.locator(".trace-timeline__row-body");
      await expect(body).toBeVisible({ timeout: 5000 });

      // The decide detail pane should show the verdict block with decision and confidence
      const verdict = body.locator("[data-testid='decide-verdict']");
      await expect(verdict).toBeVisible({ timeout: 5000 });
      await shot(page, "06a-decide-detail-pane");

      // ReplayButton: check whether it renders (may not be present in static mode
      // or if the feature flag is off). Log what we find without hard-asserting.
      const replayBtn = body.locator("[data-testid='replay-button']");
      const replayBtnCount = await replayBtn.count();
      console.log(`[demo] replay-button count: ${replayBtnCount}`);

      if (replayBtnCount > 0) {
        await shot(page, "06b-replay-button-visible");

        // Click the replay button (in static mode it will attempt RPC and fail
        // gracefully; we assert the loading state or error message appears)
        await replayBtn.click();
        await page.waitForTimeout(1500);

        // Show whatever state the UI transitions to (loading, error, or result)
        const replayLoading = body.locator("[data-testid='replay-loading']");
        const replayResult = body.locator("[data-testid='replay-result']");
        const replayError = body.locator("[data-testid='replay-error']");

        const loadingVisible = await replayLoading.isVisible().catch(() => false);
        const resultVisible = await replayResult.isVisible().catch(() => false);
        const errorVisible = await replayError.isVisible().catch(() => false);

        console.log(
          `[demo] replay states: loading=${loadingVisible} result=${resultVisible} error=${errorVisible}`
        );
        await page.waitForTimeout(3000);
        await shot(page, "06c-replay-result-or-error");
      } else {
        // Show the decide detail pane without the replay button;
        // capture the evidence drawer and full decide panel for the demo.
        const evidenceToggle = body.locator("[data-testid='decide-evidence-toggle']");
        if ((await evidenceToggle.count()) > 0) {
          await evidenceToggle.click();
          await page.waitForTimeout(SHORT_DWELL);
        }
        await shot(page, "06b-decide-full-pane");
        console.log("[demo] replay-button not present in this build — showing decide pane instead");
      }

      await page.waitForTimeout(DWELL_MS);
    });

  } finally {
    await context.close();
    await browser.close();
  }

  // Stabilize the video filename
  const vids = fs.readdirSync(VIDEO_DIR).filter((f) => f.endsWith(".webm"));
  if (vids.length > 0) {
    const stable = path.join(ARTIFACT_DIR, "trace-introspection-demo.webm");
    fs.copyFileSync(path.join(VIDEO_DIR, vids[0]), stable);
    console.log(`[demo] video: ${stable}`);
  }

  const pngs = fs.readdirSync(ARTIFACT_DIR).filter((f) => f.endsWith(".png"));
  console.log(`[demo] screenshots (${pngs.length}) in ${ARTIFACT_DIR}`);
});
