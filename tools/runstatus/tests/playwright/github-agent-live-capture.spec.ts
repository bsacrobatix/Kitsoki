/**
 * Live GitHub-agent POC capture harness.
 *
 * This spec is intentionally gated and skipped by default. It records REAL
 * GitHub and kitsoki-test pages after the live POC cases have been created:
 *
 *   KITSOKI_GH_AGENT_LIVE_CAPTURE=1 \
 *   KITSOKI_GH_AGENT_LIVE_CAPTURE_PLAN=.artifacts/github-agent-live/capture-plan.json \
 *   pnpm -C tools/runstatus exec playwright test github-agent-live-capture --project=chromium
 *
 * The capture plan lives under .artifacts because it names real throwaway
 * issue/PR/run URLs. Generated media also stays under .artifacts.
 */
import { test, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import fs from "fs";
import path from "path";
import {
  repoRoot,
  makeShot,
  prepareVideoDir,
  saveVideoAsMp4,
  ChapterRecorder,
  writeChapters,
  dwell,
  SETTLE_MS,
} from "./_helpers/server.js";
import { cameraContext } from "./_helpers/camera.js";
import { captureDiagnostics, installCurtain, liftCurtain, makeCaption } from "./_helpers/demo.js";

type CaptureStep = {
  id: string;
  title: string;
  url: string;
  caption?: string;
  waitForText?: string;
  dwellMs?: number;
};

type CapturePlan = {
  artifactDir?: string;
  videoName?: string;
  curtainTitle?: string;
  steps: CaptureStep[];
};

const DEFAULT_PLAN = path.join(repoRoot, ".artifacts", "github-agent-live", "capture-plan.json");
const SPEC_REF = "tools/runstatus/tests/playwright/github-agent-live-capture.spec.ts";

function loadPlan(): CapturePlan {
  const planPath = process.env.KITSOKI_GH_AGENT_LIVE_CAPTURE_PLAN || DEFAULT_PLAN;
  const raw = fs.readFileSync(planPath, "utf8");
  const plan = JSON.parse(raw) as CapturePlan;
  if (!Array.isArray(plan.steps) || plan.steps.length === 0) {
    throw new Error(`capture plan ${planPath} must contain a non-empty steps array`);
  }
  for (const [idx, step] of plan.steps.entries()) {
    if (!step.id || !step.title || !step.url) {
      throw new Error(`capture plan step ${idx + 1} must include id, title, and url`);
    }
    if (!/^https?:\/\//.test(step.url)) {
      throw new Error(`capture plan step ${step.id} must use an http(s) URL, got ${step.url}`);
    }
  }
  return plan;
}

async function tryInstallCurtain(page: Page, title: string): Promise<void> {
  try {
    await installCurtain(page, title);
  } catch (e) {
    console.warn(`[live-capture] curtain disabled: ${String(e).slice(0, 240)}`);
  }
}

async function tryLiftCurtain(page: Page): Promise<void> {
  try {
    await liftCurtain(page);
  } catch (e) {
    console.warn(`[live-capture] curtain lift skipped: ${String(e).slice(0, 240)}`);
  }
}

async function tryRearmCurtain(page: Page): Promise<void> {
  try {
    await page.evaluate(() => {
      sessionStorage.removeItem("kd-curtain-lifted");
      document.getElementById("kd-curtain")?.remove();
    });
  } catch (e) {
    console.warn(`[live-capture] curtain re-arm skipped: ${String(e).slice(0, 240)}`);
  }
}

async function tryMakeCaption(page: Page): Promise<(title: string, sub?: string, holdMs?: number) => Promise<void>> {
  try {
    return await makeCaption(page);
  } catch (e) {
    console.warn(`[live-capture] captions disabled: ${String(e).slice(0, 240)}`);
    return async (_title, _sub, holdMs = 5000) => {
      await dwell(page, holdMs);
    };
  }
}

async function tryStyleAPIProof(page: Page): Promise<void> {
  try {
    await page.addStyleTag({
      content:
        `html,body{margin:0;min-height:100%;background:#0b1220!important;color:#dbeafe!important;` +
        `font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace!important}` +
        `body{display:flex;align-items:center;justify-content:center;padding:56px!important}` +
        `body::before{content:"Live /api/run job state";position:fixed;top:24px;left:50%;` +
        `transform:translateX(-50%);font:700 22px ui-sans-serif,system-ui,sans-serif;` +
        `color:#f8fafc;background:#111827;border:1px solid #334155;border-left:4px solid #38bdf8;` +
        `border-radius:10px;padding:12px 18px;box-shadow:0 12px 32px rgba(0,0,0,.35)}` +
        `pre{box-sizing:border-box;width:min(1180px,88vw);max-height:70vh;overflow:auto;` +
        `white-space:pre-wrap;overflow-wrap:anywhere;background:#111827!important;color:#dbeafe!important;` +
        `border:1px solid #334155;border-radius:14px;padding:28px 32px!important;` +
        `font-size:18px!important;line-height:1.55!important;box-shadow:0 24px 70px rgba(0,0,0,.45)}`,
    });
  } catch (e) {
    console.warn(`[live-capture] API proof styling skipped: ${String(e).slice(0, 240)}`);
  }
}

test("capture live GitHub-agent evidence", async () => {
  test.skip(
    process.env.KITSOKI_GH_AGENT_LIVE_CAPTURE !== "1",
    "live capture is gated; set KITSOKI_GH_AGENT_LIVE_CAPTURE=1 with a capture plan",
  );

  test.setTimeout(420000);

  const plan = loadPlan();
  const artifactDir = path.resolve(repoRoot, plan.artifactDir || ".artifacts/github-agent-live/capture");
  const videoDir = path.join(artifactDir, "video");
  const videoName = plan.videoName || "github-agent-live";

  prepareVideoDir(videoDir);
  fs.mkdirSync(artifactDir, { recursive: true });

  const browser: Browser = await chromium.launch({ headless: true });
  const context: BrowserContext = await browser.newContext(cameraContext({ recordVideoDir: videoDir }));
  const page: Page = await context.newPage();
  const video = page.video();
  const shot = makeShot(artifactDir);
  const diag = captureDiagnostics(page, artifactDir);
  const chapters = new ChapterRecorder();

  try {
    diag.mark("install-curtain");
    await tryInstallCurtain(page, plan.curtainTitle || "Live @kitsoki GitHub App POC");

    for (const [idx, step] of plan.steps.entries()) {
      diag.mark(`step ${step.id}: goto`);
      if (idx > 0) {
        diag.mark(`step ${step.id}: re-arm curtain`);
        await tryRearmCurtain(page);
      }
      await page.goto(step.url, { waitUntil: "domcontentloaded", timeout: 45000 });
      if (step.waitForText) {
        diag.mark(`step ${step.id}: wait ${step.waitForText}`);
        await page.getByText(step.waitForText, { exact: false }).first().waitFor({ timeout: 30000 });
      }
      if (step.id === "run-api") {
        diag.mark(`step ${step.id}: style-api-proof`);
        await tryStyleAPIProof(page);
      }
      await dwell(page, SETTLE_MS);
      diag.mark(`step ${step.id}: lift-curtain`);
      await tryLiftCurtain(page);
      chapters.open(step.id, step.title, SPEC_REF);
      diag.mark(`step ${step.id}: caption`);
      const caption = await tryMakeCaption(page);
      await caption(step.title, step.caption || step.url, step.dwellMs ?? 5000);
      diag.mark(`step ${step.id}: screenshot`);
      await shot(page, step.id);
    }
  } catch (e) {
    diag.onThrow(e);
    throw e;
  } finally {
    await context.close();
    const mp4 = await saveVideoAsMp4(video, artifactDir, videoName);
    if (mp4) writeChapters(mp4, chapters.list());
    await browser.close();
  }
});
