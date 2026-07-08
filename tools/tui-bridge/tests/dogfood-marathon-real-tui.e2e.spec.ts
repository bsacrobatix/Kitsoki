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
      "stories/dogfood-marathon/app.yaml",
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

async function waitForScreen(page: Page, text: string, timeout = 30_000): Promise<void> {
  await expect.poll(() => bottom(page), { timeout }).toContain(text);
}

async function waitForScreenPattern(page: Page, pattern: RegExp, timeout = 30_000): Promise<void> {
  await expect.poll(() => bottom(page), { timeout }).toMatch(pattern);
}

async function typeLine(page: Page, text: string): Promise<void> {
  await page.click("#term");
  await page.keyboard.type(text, { delay: PACE === 0 ? 0 : 12 });
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
  try {
    await page.goto(`/player/?ws=ws://${addr}/pty`);
    await page.waitForFunction(() => (window as any).__ready === true);
    await expect
      .poll(() => page.evaluate(() => (window as any).__status()), { timeout: 60_000 })
      .toBe("connected");
    await waitForScreen(page, "Dogfood marathon", 60_000);
    await shot(page, "connected-idle");

    chapters.open("start", "Start 15-bug dogfood marathon", RECORDING);
    await typeLine(page, "start the 15 bug marathon");
    await waitForScreenPattern(page, /Processed:\s+0/);
    await shot(page, "backlog-loaded");
    await dwell(page, 700);

    for (let i = 1; i <= 15; i += 1) {
      chapters.open(`bug-${String(i).padStart(2, "0")}`, `Process bug ${i}`, RECORDING);
      await typeLine(page, `continue bug ${i}`);
      if (i === 5) {
        await waitForScreen(page, "exception review");
        await shot(page, "exception-review");
        await dwell(page, 950);
        chapters.open("operator-exception", "Operator acknowledges serious question", RECORDING);
        await typeLine(page, "acknowledge and continue");
      }
      await waitForScreenPattern(page, new RegExp(`Processed:\\s+${i}`));
      if (i === 5) {
        await shot(page, "exception-acknowledged");
      }
      if (i === 10 || i === 15) {
        await shot(page, `processed-${String(i).padStart(2, "0")}`);
      }
      await dwell(page, 950);
    }

    chapters.open("report", "Aggregate and render slidey decks", RECORDING);
    await typeLine(page, "finish the report");
    await waitForScreen(page, "15 case(s)", 60_000);
    await waitForScreen(page, "15 per-bug deck(s)", 60_000);
    const finalScreen = await bottom(page);
    expect(finalScreen).toContain("15");
    await shot(page, "done-report");
    await dwell(page, 1_200);
  } finally {
    chapters.close();
    const video = page.video();
    await context.close();
    videoPath = await saveVideoAsMp4(video, ARTIFACT_DIR, "dogfood-marathon-real-tui");
    writeChapters(videoPath, chapters.list());
    stopBridge(bridge);
  }
});
