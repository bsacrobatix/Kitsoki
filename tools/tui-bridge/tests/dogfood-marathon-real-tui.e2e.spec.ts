import { test, expect, type Page } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import {
  ChapterRecorder,
  PACE,
  cameraContext,
  dwell,
  makeShot,
  prepareVideoDir,
  saveVideoAsMp4,
  writeChapters,
} from "./_helpers/recording.js";

const REPO_ROOT = path.resolve(import.meta.dirname, "..", "..", "..");
const ARTIFACT_DIR = path.join(REPO_ROOT, ".artifacts", "tui-bridge", "dogfood-marathon-real-tui");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");
const DB_PATH = path.join(ARTIFACT_DIR, "sessions.db");
const RECORDING = "tools/tui-bridge/fixtures/dogfood-marathon-recording.yaml";
const HOST_CASSETTE = "tools/tui-bridge/fixtures/dogfood-marathon.host.cassette.yaml";
const TYPE_DELAY_MS = PACE === 0 ? 0 : 45;
const COMMAND_HOLD_MS = 900;
const START_HOLD_MS = 2_500;
const CASE_HOLD_MS = 3_200;
const EXCEPTION_HOLD_MS = 4_000;
const REPORT_HOLD_MS = 5_000;
const READABLE_CHAPTER_MIN_MS = 3_000;
const CASE_IDS = [
  "constructorfabric/Kitsoki#66",
  "constructorfabric/Kitsoki#65",
  "constructorfabric/Kitsoki#64",
  "constructorfabric/Kitsoki#63",
  "constructorfabric/Kitsoki#61",
  "bsacrobatix/Kitsoki#1202",
  "bsacrobatix/Kitsoki#1201",
  "bsacrobatix/Kitsoki#1200",
  "bsacrobatix/Kitsoki#1199",
  "bsacrobatix/Kitsoki#1198",
  "bsacrobatix/Kitsoki#1197",
  "bsacrobatix/Kitsoki#1196",
  "bsacrobatix/Kitsoki#1194",
  "bsacrobatix/Kitsoki#1190",
  "local/bugfix-auth-profile",
];

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      srv.close(() => resolve(port));
    });
  });
}

function startBridge(addr: string): ChildProcess {
  fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
  fs.rmSync(DB_PATH, { force: true });
  const log = fs.createWriteStream(path.join(ARTIFACT_DIR, "bridge.log"), { flags: "w" });
  const proc = spawn(
    "go",
    [
      "run",
      "./cmd/kitsoki",
      "tui-serve",
      "--addr",
      addr,
      "--",
      "run",
      ".kitsoki/stories/kitsoki-dev/app.yaml",
      "--harness",
      "replay",
      "--recording",
      RECORDING,
      "--host-cassette",
      HOST_CASSETTE,
      "--db",
      DB_PATH,
    ],
    { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"], detached: true },
  );
  proc.stdout?.pipe(log, { end: false });
  proc.stderr?.pipe(log, { end: false });
  proc.once("exit", (code, signal) => {
    log.write(`\n[bridge exit] code=${code ?? ""} signal=${signal ?? ""}\n`);
    log.end();
  });
  return proc;
}

function stopBridge(proc: ChildProcess): void {
  if (!proc.pid) return;
  try {
    process.kill(-proc.pid, "SIGKILL");
  } catch {
    proc.kill("SIGKILL");
  }
}

async function bottom(page: Page): Promise<string> {
  return page.evaluate(() => (window as any).__scrollToBottom());
}

async function fullBuffer(page: Page): Promise<string> {
  return page.evaluate(() => (window as any).__dumpBuffer());
}

async function scrollLines(page: Page, lines: number): Promise<void> {
  await page.evaluate((n) => (window as any).__scrollLines(n), lines);
}

async function focusChat(page: Page): Promise<void> {
  await page.click("#term");
  await page.keyboard.press("Tab");
  await dwell(page, 300);
}

async function waitForScreen(page: Page, text: string, timeout = 30_000): Promise<void> {
  await expect.poll(() => bottom(page), { timeout }).toContain(text);
}

async function waitForScreenPattern(page: Page, pattern: RegExp, timeout = 30_000): Promise<void> {
  await expect.poll(() => bottom(page), { timeout }).toMatch(pattern);
}

async function waitForBuffer(page: Page, text: string, timeout = 30_000): Promise<void> {
  await expect.poll(() => fullBuffer(page), { timeout }).toContain(text);
}

async function typeLine(
  page: Page,
  text: string,
  shot?: (page: Page, label: string) => Promise<string>,
  shotLabel?: string,
): Promise<void> {
  await page.click("#term");
  await page.keyboard.type(text, { delay: TYPE_DELAY_MS });
  await dwell(page, COMMAND_HOLD_MS);
  if (shot && shotLabel) {
    await shot(page, shotLabel);
  }
  await page.keyboard.press("Enter");
}

test("records one continuous real Kitsoki TUI dogfood marathon session", async ({ browser }) => {
  test.setTimeout(240_000);

  prepareVideoDir(VIDEO_DIR);
  const shot = makeShot(ARTIFACT_DIR);
  const chapters = new ChapterRecorder();
  const port = await freePort();
  const addr = `127.0.0.1:${port}`;
  const bridge = startBridge(addr);

  const context = await browser.newContext(cameraContext({ recordVideoDir: VIDEO_DIR }));
  const page = await context.newPage();
  let videoPath: string | null = null;
  let chapterPacingFailures: string[] = [];
  try {
    await page.goto(`/player/?ws=ws://${addr}/pty`);
    await page.waitForFunction(() => (window as any).__ready === true);
    await expect
      .poll(() => page.evaluate(() => (window as any).__status()), { timeout: 60_000 })
      .toBe("connected");
    await waitForScreen(page, "free-form workbench", 60_000);
    await shot(page, "connected-kitsoki-dev");

    chapters.open("kitsoki-dev-request", "Request the dogfood marathon from kitsoki-dev", RECORDING);
    await focusChat(page);
    await typeLine(page, "I want to do a dogfood marathon", shot, "typed-dev-request");
    await waitForScreen(page, "Dogfood marathon", 60_000);
    const idleScreen = await bottom(page);
    expect(idleScreen).toContain("Drive a backlog of cases");
    expect(await fullBuffer(page)).toContain("Drive a backlog of cases");
    await shot(page, "dogfood-idle-full-message");
    await dwell(page, START_HOLD_MS);
    await bottom(page);

    chapters.open("start", "Start autonomous 15-bug dogfood marathon", RECORDING);
    await focusChat(page);
    await typeLine(page, "start the marathon", shot, "typed-start-marathon");
    await waitForScreen(page, "Dogfood marathon · driving", 60_000);
    await waitForScreenPattern(page, /Recorded:\s+0/);
    await shot(page, "backlog-loaded-driving");
    await dwell(page, START_HOLD_MS);

    for (let i = 1; i <= CASE_IDS.length; i += 1) {
      const caseId = CASE_IDS[i - 1];
      chapters.open(`bug-${String(i).padStart(2, "0")}`, `Autonomously process ${caseId}`, RECORDING);
      await waitForBuffer(page, caseId, 90_000);
      if (i === 5) {
        await waitForScreen(page, "core.dogfood.exception_review");
        await waitForBuffer(page, caseId);
        await scrollLines(page, -8);
        await dwell(page, COMMAND_HOLD_MS);
        await shot(page, "exception-review");
        await dwell(page, EXCEPTION_HOLD_MS);
        chapters.open("operator-exception", "Operator acknowledges serious question", RECORDING);
        await bottom(page);
        await focusChat(page);
        await typeLine(page, "acknowledge and continue", shot, "typed-exception-ack");
      }
      if (i === CASE_IDS.length) {
        await waitForBuffer(page, "15 case(s)", 60_000);
      }
      await shot(page, i === 5 ? "exception-acknowledged" : `processed-${String(i).padStart(2, "0")}`);
      await dwell(page, CASE_HOLD_MS);
    }

    chapters.open("report", "Aggregate and render slidey decks", RECORDING);
    await waitForBuffer(page, "15 case(s)", 60_000);
    await waitForBuffer(page, "15 per-bug deck(s)", 60_000);
    const finalScreen = await fullBuffer(page);
    expect(finalScreen).toContain("15");
    await scrollLines(page, -14);
    await dwell(page, COMMAND_HOLD_MS);
    await shot(page, "done-summary");
    await dwell(page, START_HOLD_MS);
    await bottom(page);
    await shot(page, "done-report");
    await dwell(page, REPORT_HOLD_MS);
  } finally {
    const chapterList = chapters.list();
    const video = page.video();
    await context.close();
    videoPath = await saveVideoAsMp4(video, ARTIFACT_DIR, "dogfood-marathon-real-tui");
    writeChapters(videoPath, chapterList);
    if (PACE !== 0) {
      chapterPacingFailures = chapterList
        .filter((chapter) => chapter.end_ms - chapter.start_ms < READABLE_CHAPTER_MIN_MS)
        .map((chapter) => `${chapter.id}=${chapter.end_ms - chapter.start_ms}ms`);
    }
    stopBridge(bridge);
  }
  expect(chapterPacingFailures).toEqual([]);
});
