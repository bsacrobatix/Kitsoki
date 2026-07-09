import { expect, test } from "@playwright/test";
import { spawnSync } from "child_process";
import fs from "fs";
import path from "path";
import { pathToFileURL } from "url";
import { makeCaption, makeSpotlight, DEMO_VIEWPORT } from "./_helpers/demo.js";
import {
  ChapterRecorder,
  makeShot,
  prepareVideoDir,
  repoRoot,
  saveVideoAsMp4,
  writeChapters,
} from "./_helpers/server.js";

const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "arena-showdown-demo");
const VIDEO_DIR = path.join(ARTIFACT_DIR, ".video");
const RUN_DIR = path.resolve(
  repoRoot,
  process.env.ARENA_SHOWDOWN_RUN_DIR ?? ".artifacts/arena/codeact-showdown",
);
const FEATURE = path.join(ARTIFACT_DIR, "qa-feature.md");
const SCENARIOS = path.join(ARTIFACT_DIR, "qa-scenarios.yaml");
const HTML = path.join(ARTIFACT_DIR, "arena-showdown.html");
const SPEC_PATH = "tools/runstatus/tests/playwright/arena-showdown-demo.spec.ts";

test.describe("arena showdown demo", () => {
  test.setTimeout(90000);

  test.beforeAll(() => {
    fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
    prepareVideoDir(VIDEO_DIR);
  });

  test("records a tour over a real arena run bundle", async ({ browser }) => {
    expect(fs.existsSync(path.join(RUN_DIR, "summary.json")) || fs.existsSync(path.join(RUN_DIR, "rollup.json")),
      `missing arena run bundle at ${RUN_DIR}; run python3 tools/arena/arena.py run --spec tools/arena/specs/codex-codeact-action-surface.yaml --out ${RUN_DIR}`,
    ).toBeTruthy();

    const generated = spawnSync(
      "python3",
      [
        path.join(repoRoot, "tools/arena/scripts/render_showdown_demo.py"),
        "--run-dir",
        RUN_DIR,
        "--out-dir",
        ARTIFACT_DIR,
      ],
      { cwd: repoRoot, encoding: "utf8" },
    );
    expect(generated.status, generated.stderr || generated.stdout).toBe(0);
    expect(fs.existsSync(HTML)).toBeTruthy();
    expect(fs.existsSync(FEATURE)).toBeTruthy();
    expect(fs.existsSync(SCENARIOS)).toBeTruthy();

    const context = await browser.newContext({
      viewport: DEMO_VIEWPORT,
      deviceScaleFactor: 1,
      recordVideo: { dir: VIDEO_DIR, size: DEMO_VIEWPORT },
    });
    const page = await context.newPage();
    const video = page.video();
    const shot = makeShot(ARTIFACT_DIR);
    const chapters = new ChapterRecorder();

    try {
      await page.goto(pathToFileURL(HTML).toString());
      await expect(page.getByTestId("arena-demo-hero")).toBeVisible();
      const caption = await makeCaption(page, 3200);
      const spotlight = await makeSpotlight(page);

      await step("01-run-identity", "Real arena run bundle", "arena-demo-hero",
        "The tour starts from the generated run page, including run id, mode, and cell count.");
      await step("02-status", "Outcome and health are first-class", "arena-status-cards",
        "The dashboard distinguishes wins, permission compliance, and infrastructure health.");
      await step("03-treatments", "CodeAct vs raw Codex treatments", "arena-treatment-table",
        "The leaderboard is rendered directly from the arena summary, so it cannot drift from the run.");
      await step("04-permissions", "CodeAct launch proof", "arena-permission-evidence",
        "CodeAct-specific cells carry restricted-surface compliance alongside normal arena metrics.");
      await step("05-health", "Honest blocker reporting", "arena-infra-note",
        "If Docker or another harness prerequisite fails, the demo shows the blocker instead of a fake model loss.");
      await step("06-cells", "Per-cell evidence", "arena-cell-table",
        "Every treatment arm keeps its own verdict, health class, surface, and notes.");
      await step("07-artifacts", "Reusable output bundle", "arena-artifacts",
        "The same run directory carries summary JSON, Markdown, Slidey source, rollup files, and cell JSON.");

      await spotlight(null);
      chapters.close();

      async function step(id: string, title: string, testId: string, body: string): Promise<void> {
        chapters.open(id, title, SPEC_PATH);
        const selector = `[data-testid="${testId}"]`;
        await expect(page.locator(selector)).toBeVisible({ timeout: 8000 });
        await spotlight(selector);
        await caption(title, body, 3600);
        await shot(page, id);
      }
    } finally {
      await context.close();
      const mp4 = await saveVideoAsMp4(video, ARTIFACT_DIR, "arena-showdown-demo");
      writeChapters(mp4, chapters.list());
    }
  });
});
