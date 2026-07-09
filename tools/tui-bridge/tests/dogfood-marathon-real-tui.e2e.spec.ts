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
  shiftChapters,
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
const START_FRAME_SETTLE_MS = 500;
const INTRO_HOLD_MS = 2_000;
const CHOICE_HOLD_MS = 1_300;
const MAX_USEFUL_PREROLL_MS = 3_500;
const CASES = [
  { id: "constructorfabric/Kitsoki#66", title: "raw AskOffPath payload" },
  { id: "constructorfabric/Kitsoki#65", title: "init failure title" },
  { id: "constructorfabric/Kitsoki#64", title: "landing action rows" },
  { id: "constructorfabric/Kitsoki#63", title: "duplicate inbox notification" },
  { id: "constructorfabric/Kitsoki#61", title: "first-run profile scope" },
  { id: "bsacrobatix/Kitsoki#1202", title: "blocked findings accounting" },
  { id: "bsacrobatix/Kitsoki#1201", title: "replay agent-task dispatch" },
  { id: "bsacrobatix/Kitsoki#1200", title: "empty ticket queue" },
  { id: "bsacrobatix/Kitsoki#1199", title: "hosted preflight triage" },
  { id: "bsacrobatix/Kitsoki#1198", title: "HAR method labels" },
  { id: "bsacrobatix/Kitsoki#1197", title: "trace row labels" },
  { id: "bsacrobatix/Kitsoki#1196", title: "mobile tour dead-end" },
  { id: "bsacrobatix/Kitsoki#1194", title: "GitHub label rate limits" },
  { id: "bsacrobatix/Kitsoki#1190", title: "wrong profile after on_error" },
  { id: "local/bugfix-auth-profile", title: "codex profile backend assertion" },
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

async function visibleScreen(page: Page): Promise<string> {
  return page.evaluate(() => (window as any).__dump());
}

async function scrollLines(page: Page, lines: number): Promise<void> {
  await page.evaluate((n) => (window as any).__scrollLines(n), lines);
}

async function scrollUpUntilVisible(page: Page, text: string): Promise<void> {
  for (let i = 0; i < 8; i += 1) {
    if ((await visibleScreen(page)).includes(text)) {
      return;
    }
    await scrollLines(page, -8);
    await dwell(page, 100);
  }
  await expect.poll(() => visibleScreen(page), { timeout: 5_000 }).toContain(text);
}

async function focusTerminal(page: Page): Promise<void> {
  await page.click("#term");
}

async function focusChat(page: Page): Promise<void> {
  await focusTerminal(page);
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

async function choiceCursorLine(page: Page): Promise<string> {
  const screen = await page.evaluate(() => (window as any).__dump());
  return String(screen)
    .split("\n")
    .map((line) => line.trim())
    .find((line) => line.includes("▸")) ?? "";
}

async function waitForChoiceCursor(page: Page): Promise<string> {
  await expect.poll(() => choiceCursorLine(page), { timeout: 10_000 }).not.toBe("");
  return choiceCursorLine(page);
}

async function pressChoiceKeyForCursorChange(
  page: Page,
  key: "ArrowDown" | "ArrowUp",
  before: string,
): Promise<string> {
  await page.keyboard.press(key);
  await expect.poll(() => choiceCursorLine(page), { timeout: 10_000 }).not.toBe(before);
  return choiceCursorLine(page);
}

async function exerciseChoiceWidget(
  page: Page,
  shot: (page: Page, label: string) => Promise<string>,
  labelPrefix: string,
): Promise<void> {
  await focusTerminal(page);
  await waitForScreen(page, "[↑/↓ move");
  const initial = await waitForChoiceCursor(page);
  await shot(page, `${labelPrefix}-choice-initial`);
  await dwell(page, CHOICE_HOLD_MS);

  const down = await pressChoiceKeyForCursorChange(page, "ArrowDown", initial);
  expect(down).not.toEqual(initial);
  await shot(page, `${labelPrefix}-choice-arrow-down`);
  await dwell(page, CHOICE_HOLD_MS);

  await page.keyboard.press("ArrowUp");
  await expect.poll(() => choiceCursorLine(page), { timeout: 10_000 }).toBe(initial);
  await shot(page, `${labelPrefix}-choice-arrow-up`);
  await dwell(page, CHOICE_HOLD_MS);
}

async function acknowledgeExceptionWithChoice(
  page: Page,
  shot: (page: Page, label: string) => Promise<string>,
): Promise<void> {
  await focusTerminal(page);
  await waitForBuffer(page, "! SERIOUS EXCEPTION");
  await waitForScreen(page, "[↑/↓ move");
  const initial = await waitForChoiceCursor(page);
  expect(initial).toContain("acknowledge and continue");
  await shot(page, "exception-choice-initial");
  await dwell(page, CHOICE_HOLD_MS);

  const answer = await pressChoiceKeyForCursorChange(page, "ArrowDown", initial);
  expect(answer).toContain("answer with guidance");
  await shot(page, "exception-choice-answer");
  await dwell(page, CHOICE_HOLD_MS);

  await page.keyboard.press("ArrowUp");
  await expect.poll(() => choiceCursorLine(page), { timeout: 10_000 }).toBe(initial);
  await shot(page, "exception-choice-acknowledge");
  await dwell(page, CHOICE_HOLD_MS);
  await page.keyboard.press("Enter");
}

test("records one continuous real Kitsoki TUI dogfood marathon session", async ({ browser }) => {
  test.setTimeout(240_000);

  prepareVideoDir(VIDEO_DIR);
  const shot = makeShot(ARTIFACT_DIR);
  const port = await freePort();
  const addr = `127.0.0.1:${port}`;
  const bridge = startBridge(addr);

  const context = await browser.newContext(cameraContext({ recordVideoDir: VIDEO_DIR }));
  const page = await context.newPage();
  const chapters = new ChapterRecorder();
  let videoPath: string | null = null;
  let chapterPacingFailures: string[] = [];
  let videoTrimStartMs = 0;
  let firstChapterStartAfterTrimMs = 0;
  try {
    await page.goto(`/player/?ws=ws://${addr}/pty`);
    await page.waitForFunction(() => (window as any).__ready === true);
    await expect
      .poll(() => page.evaluate(() => (window as any).__status()), { timeout: 60_000 })
      .toBe("connected");
    await waitForScreen(page, "free-form workbench", 60_000);
    await dwell(page, START_FRAME_SETTLE_MS);
    videoTrimStartMs = chapters.elapsedMs();
    await shot(page, "connected-kitsoki-dev");
    await dwell(page, INTRO_HOLD_MS);

    chapters.open("kitsoki-dev-choice-widget", "Move the kitsoki-dev TUI choice widget with arrow keys", RECORDING);
    await exerciseChoiceWidget(page, shot, "kitsoki-dev");

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

    chapters.open("dogfood-choice-widget", "Move the dogfood marathon action choice widget with arrow keys", RECORDING);
    await exerciseChoiceWidget(page, shot, "dogfood");

    chapters.open("start", "Start autonomous 15-bug dogfood marathon", RECORDING);
    await focusChat(page);
    await typeLine(page, "start the marathon", shot, "typed-start-marathon");
    if (PACE === 0) {
      await waitForBuffer(page, CASES[0].id, 60_000);
    } else {
      await waitForScreen(page, `Case 1 / ${CASES.length} · driving`, 60_000);
      await waitForScreenPattern(page, /Recorded:\s+0/);
    }
    await shot(page, "backlog-loaded-driving");
    await dwell(page, START_HOLD_MS);

    for (let i = 1; i <= CASES.length; i += 1) {
      const { id: caseId, title } = CASES[i - 1];
      chapters.open(`bug-${String(i).padStart(2, "0")}`, `Process ${caseId}: ${title}`, RECORDING);
      await waitForBuffer(page, caseId, 90_000);
      if (i === 5) {
        await waitForScreen(page, "core.dogfood.exception_review");
        await waitForBuffer(page, "! SERIOUS EXCEPTION");
        await waitForBuffer(page, caseId);
        await scrollUpUntilVisible(page, "! SERIOUS EXCEPTION");
        await dwell(page, COMMAND_HOLD_MS);
        await shot(page, "exception-review");
        await dwell(page, EXCEPTION_HOLD_MS);
        chapters.open("operator-exception", "Acknowledge the serious exception through the choice widget", RECORDING);
        await bottom(page);
        await acknowledgeExceptionWithChoice(page, shot);
      }
      if (i === CASES.length) {
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
    const chapterList = shiftChapters(chapters.list(), videoTrimStartMs);
    firstChapterStartAfterTrimMs = chapterList[0]?.start_ms ?? 0;
    const video = page.video();
    await context.close();
    videoPath = await saveVideoAsMp4(video, ARTIFACT_DIR, "dogfood-marathon-real-tui", {
      trimStartMs: videoTrimStartMs,
    });
    writeChapters(videoPath, chapterList);
    if (PACE !== 0) {
      chapterPacingFailures = chapterList
        .filter((chapter) => chapter.end_ms - chapter.start_ms < READABLE_CHAPTER_MIN_MS)
        .map((chapter) => `${chapter.id}=${chapter.end_ms - chapter.start_ms}ms`);
    }
    stopBridge(bridge);
  }
  expect(firstChapterStartAfterTrimMs).toBeLessThanOrEqual(MAX_USEFUL_PREROLL_MS);
  expect(chapterPacingFailures).toEqual([]);
});
