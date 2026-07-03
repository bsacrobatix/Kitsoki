/**
 * Session Media Workbench feature tour.
 *
 * Drives the mockup-video story to a produced media artifact behind a curtain,
 * then records the workbench-specific tour:
 * media pinned beside chat, graph/trace devtools, horizontal layout, floating
 * devtools, and the popout affordance. The story flow is deterministic no-LLM.
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page, type Locator } from "@playwright/test";
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
  demoAddr,
  type WebServer,
} from "./_helpers/server.js";
import { installCurtain, liftCurtain } from "./_helpers/demo.js";
import { cameraContext } from "./_helpers/camera.js";
import {
  SESSION_MEDIA_WORKBENCH_TOUR_STEPS,
  type TourStep,
} from "../../src/tour/generated/session-media-workbench.js";

const ADDR = demoAddr(7766);
const STORY_DIR = path.join(repoRoot, "stories", "mockup-video");
const FLOW = path.join(STORY_DIR, "flows", "demo_web.yaml");
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "session-media-workbench");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");
const CHAPTER_SOURCE = "features/session-media-workbench.yaml";
const DIAG_LOG = path.join(ARTIFACT_DIR, "diagnostic.log");
const ERROR_TXT = path.join(ARTIFACT_DIR, "ERROR.txt");
const REAL_VIDEO = path.join(repoRoot, ".artifacts", "review-video", "render", "walkthrough.mp4");

let server: WebServer;

function diag(msg: string): void {
  try {
    fs.appendFileSync(DIAG_LOG, `[${new Date().toISOString()}] ${msg}\n`);
  } catch {
    /* best-effort */
  }
}

async function resolveTarget(page: Page, step: TourStep): Promise<Locator> {
  return page.getByTestId(step.target!).first();
}

async function dragSplitter(page: Page, target: Locator, deltaX: number, deltaY: number): Promise<void> {
  const box = await target.boundingBox();
  if (!box) throw new Error("splitter target has no bounding box");
  const startX = box.x + box.width / 2;
  const startY = box.y + box.height / 2;
  await page.mouse.move(startX, startY);
  await page.mouse.down();
  await page.mouse.move(startX + deltaX, startY + deltaY, { steps: 8 });
  await page.mouse.up();
}

async function workbenchGrid(page: Page): Promise<string> {
  return page.locator(".iv__main--workbench").evaluate((el) => {
    const style = getComputedStyle(el as HTMLElement);
    return `${style.gridTemplateColumns} / ${style.gridTemplateRows}`;
  });
}

test.beforeAll(async () => {
  if (!fs.existsSync(REAL_VIDEO) || !fs.existsSync(REAL_VIDEO + ".chapters.json")) {
    throw new Error(
      `missing real render at ${REAL_VIDEO}(.chapters.json) — run the review-video render setup first.`,
    );
  }
  fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
  prepareVideoDir(VIDEO_DIR);
  fs.writeFileSync(DIAG_LOG, "");
  if (fs.existsSync(ERROR_TXT)) fs.rmSync(ERROR_TXT);
  server = await startWebServer({ addr: ADDR, flow: FLOW, storiesDir: STORY_DIR });
});

test.afterAll(() => server?.stop());

test("session media workbench tour video", async () => {
  test.setTimeout(300000);
  const browser: Browser = await chromium.launch({ headless: true });
  const context: BrowserContext = await browser.newContext(
    cameraContext({ recordVideoDir: VIDEO_DIR }),
  );
  const page: Page = await context.newPage();
  const video = page.video();
  const shot = makeShot(ARTIFACT_DIR);
  const chapters = new ChapterRecorder();

  try {
    await installCurtain(page, "Session Media Workbench");
    diag("navigating home behind curtain");
    await cinematicGoto(page, `${server.base}/#/`, { waitForTestId: "home-view" });
    await page.getByTestId("new-session-btn").first().click();
    await page.waitForURL(/#\/s\/[0-9a-f-]{36}\/chat$/, { timeout: 15000 });

    diag("driving mockup-video to review media");
    await expect(page.getByTestId("current-state")).toContainText("intake", { timeout: 15000 });
    await page.getByTestId("intent-btn-ready").click();
    await expect(page.getByTestId("intent-btn-accept")).toBeVisible({ timeout: 30000 });
    await page.getByTestId("intent-btn-accept").click();
    await expect(page.getByTestId("media-video").first()).toBeVisible({ timeout: 30000 });
    await expect(page.getByTestId("media-workbench-toggle")).toBeVisible({ timeout: 15000 });

    await page.evaluate((stepsJson: string) => {
      (window as unknown as { __startTourWithSteps?: (s: string) => void })
        .__startTourWithSteps?.(stepsJson);
    }, JSON.stringify(SESSION_MEDIA_WORKBENCH_TOUR_STEPS));
    await expect(page.getByTestId("tour-overlay")).toBeVisible({ timeout: 8000 });
    await liftCurtain(page);

    for (const step of SESSION_MEDIA_WORKBENCH_TOUR_STEPS) {
      diag(`step ${step.id}`);
      if (step.waitForTarget) {
        await expect(page.getByTestId(step.waitForTarget).first()).toBeVisible({ timeout: 15000 });
      }

      await expect(page.getByTestId("tour-title")).toHaveText(step.title, { timeout: 12000 });
      chapters.open(step.id, step.title, CHAPTER_SOURCE);
      await dwell(page, step.dwellMs ?? 3000);
      await shot(page, step.id);

      if (step.kind === "explain") {
        await page.getByTestId("tour-next").click();
        await dwell(page, 700);
      } else {
        const target = await resolveTarget(page, step);
        await target.scrollIntoViewIfNeeded().catch(() => undefined);
        if (step.id === "smw-resize-media") {
          const before = await workbenchGrid(page);
          await dragSplitter(page, target, 160, 0);
          await expect.poll(() => workbenchGrid(page)).not.toBe(before);
        } else if (step.id === "smw-resize-bottom") {
          const before = await workbenchGrid(page);
          await dragSplitter(page, target, 0, -80);
          await expect.poll(() => workbenchGrid(page)).not.toBe(before);
        } else {
          await target.evaluate((el) => (el as HTMLElement).click());
        }
        await dwell(page, 1000);
      }
    }

    await expect(page.getByTestId("tour-overlay")).toHaveCount(0, { timeout: 5000 });
  } catch (e) {
    const msg = e instanceof Error ? e.stack ?? e.message : String(e);
    diag(`FAILED: ${msg}`);
    diag(`--- server log ---\n${server?.log?.() ?? ""}`);
    try {
      fs.writeFileSync(ERROR_TXT, `${msg}\n\n--- server log ---\n${server?.log?.() ?? ""}\n`);
    } catch {
      /* best-effort */
    }
    throw e;
  } finally {
    await context.close();
    const mp4 = await saveVideoAsMp4(video, ARTIFACT_DIR, "session-media-workbench-demo");
    writeChapters(mp4, chapters.list());
    await browser.close();
  }

  const pngs = fs.readdirSync(ARTIFACT_DIR).filter((f) => f.endsWith(".png"));
  console.log(`[session-media-workbench] screenshots (${pngs.length}) in ${ARTIFACT_DIR}`);
});
