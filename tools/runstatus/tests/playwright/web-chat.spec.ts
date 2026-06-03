/**
 * web-chat.spec.ts — drives the LIVE interactive web UI through the full PRD
 * happy-path chat scenario against a real `kitsoki web` server running the
 * deterministic flow (stories/prd/flows/happy_path.yaml — host responses
 * stubbed, no LLM).
 *
 * Unlike the snapshot/artifact specs (which load file:// fixtures), this spec
 * spawns the actual binary, gets the live session id over JSON-RPC, navigates
 * the SPA to the /#/s/<id>/chat interactive view, and drives the conversation
 * SCENE BY SCENE — typing into the composer for text-slot intents and clicking
 * intent buttons for slot-less intents — asserting the current-state badge
 * after each step and recording a full MacBook-resolution video + per-scene
 * screenshots for visual verification.
 *
 * Acceptance bar: the scenario must drive cleanly end-to-end to the terminal
 * @exit:done state, with the trace timeline accumulating events.
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import { fileURLToPath } from "url";
import path from "path";
import fs from "fs";
import os from "os";
import { spawn, type ChildProcess } from "child_process";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// repoRoot = .../kitsoki (tools/runstatus/tests/playwright -> 4 up)
const repoRoot = path.resolve(__dirname, "../../../..");
const BIN = path.join(repoRoot, "bin", "kitsoki");
const APP = path.join(repoRoot, "stories", "prd", "app.yaml");
const FLOW = path.join(repoRoot, "stories", "prd", "flows", "happy_path.yaml");

const ADDR = "127.0.0.1:7720";
const BASE = `http://${ADDR}`;
const RPC = `${BASE}/rpc`;

const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "web-chat");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");

// ── server lifecycle ────────────────────────────────────────────────────────

let server: ChildProcess | null = null;
let serverLog = "";
let tmpDbDir = "";

async function rpc<T>(method: string, params: Record<string, unknown>): Promise<T> {
  const res = await fetch(RPC, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }),
  });
  const body = (await res.json()) as { result?: T; error?: { message: string } };
  if (body.error) throw new Error(`${method} failed: ${body.error.message}`);
  return body.result as T;
}

async function waitForHealthy(timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastErr = "";
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${BASE}/`, { method: "GET" });
      if (res.status === 200) return;
      lastErr = `status ${res.status}`;
    } catch (e) {
      lastErr = e instanceof Error ? e.message : String(e);
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`server not healthy after ${timeoutMs}ms (last: ${lastErr})\n--- server log ---\n${serverLog}`);
}

test.beforeAll(async () => {
  for (const p of [APP, FLOW, BIN]) {
    if (!fs.existsSync(p)) throw new Error(`missing required path: ${p}`);
  }
  fs.mkdirSync(VIDEO_DIR, { recursive: true });
  // Clean stale screenshots from a prior run so the artifact set is exact.
  for (const f of fs.readdirSync(ARTIFACT_DIR)) {
    if (f.endsWith(".png")) fs.rmSync(path.join(ARTIFACT_DIR, f));
  }

  tmpDbDir = fs.mkdtempSync(path.join(os.tmpdir(), "kitsoki-web-chat-"));
  const dbPath = path.join(tmpDbDir, "s.db");

  server = spawn(
    BIN,
    ["web", APP, "--flow", FLOW, "--addr", ADDR, "--db", dbPath],
    { cwd: repoRoot, stdio: ["ignore", "pipe", "pipe"] },
  );
  server.stdout?.on("data", (d) => (serverLog += d.toString()));
  server.stderr?.on("data", (d) => (serverLog += d.toString()));
  server.on("exit", (code, sig) => {
    serverLog += `\n[server exited code=${code} sig=${sig}]\n`;
  });

  await waitForHealthy(20000);
});

test.afterAll(async () => {
  if (server && !server.killed) {
    server.kill("SIGTERM");
    await new Promise((r) => setTimeout(r, 500));
    if (!server.killed) server.kill("SIGKILL");
  }
  if (tmpDbDir) fs.rmSync(tmpDbDir, { recursive: true, force: true });
});

// ── the scenario ──────────────────────────────────────────────────────────

// Each scene maps a happy_path turn to a UI action + the state it should land in.
// noAgentView marks a turn whose landed state renders no room view (a terminal
// exit), so the UI correctly pushes no agent bubble — the state badge is the
// signal the turn landed, not a new transcript row.
type Scene =
  | { kind: "text"; intent: string; slot: string; text: string; expectState: string; label: string; noAgentView?: boolean }
  | { kind: "action"; intent: string; expectState: string; label: string; noAgentView?: boolean };

const SCENES: Scene[] = [
  { kind: "text", intent: "discuss", slot: "message", text: "I want a CLI for X", expectState: "idle", label: "discuss" },
  { kind: "action", intent: "start", expectState: "clarifying", label: "start" },
  { kind: "text", intent: "submit_answers", slot: "answers", text: "1) developers 2) time-to-first-success", expectState: "brief", label: "submit_answers" },
  { kind: "action", intent: "confirm", expectState: "references", label: "confirm-brief" },
  { kind: "action", intent: "confirm", expectState: "drafting", label: "confirm-references" },
  { kind: "action", intent: "accept", expectState: "__exit__done", label: "accept", noAgentView: true },
];

// Pacing: the recorded video is the deliverable, so by default we drive at a
// human-watchable speed — slow typing, a beat before each action, and a dwell
// on each settled scene so a viewer can read the room before the next turn.
// Set WEB_CHAT_PACE=0 to collapse all delays for a fast assertion-only CI run.
const PACE = process.env.WEB_CHAT_PACE === "0" ? 0 : 1;
const TYPE_DELAY = 55 * PACE; // per-keystroke, so typing is visible
const BEFORE_ACT = 900 * PACE; // beat before send/click so the control is seen
const DWELL = 2600 * PACE; // read the landed room before the next turn
const OPEN_DWELL = 3200 * PACE; // first frame
const FINAL_DWELL = 5000 * PACE; // linger on the completed chat + trace

const screenshotPaths: string[] = [];

async function shot(page: Page, idx: number, state: string): Promise<void> {
  const safe = state.replace(/[^a-zA-Z0-9_]+/g, "_");
  const name = `${String(idx).padStart(2, "0")}-${safe}.png`;
  const p = path.join(ARTIFACT_DIR, name);
  await page.screenshot({ path: p, fullPage: true });
  screenshotPaths.push(p);
}

test.describe("PRD happy-path interactive chat (live, no-LLM flow)", () => {
  test("drives idle → clarifying → brief → references → drafting → @exit:done", async () => {
    test.setTimeout(120000);

    // Live session id from the running server.
    const sessions = await rpc<Array<{ session_id: string; current_state: string }>>(
      "runstatus.sessions.list",
      {},
    );
    expect(sessions.length, "expected exactly one live session").toBeGreaterThan(0);
    const sid = sessions[0].session_id;
    expect(sessions[0].current_state).toBe("idle");

    const browser: Browser = await chromium.launch();
    const context: BrowserContext = await browser.newContext({
      viewport: { width: 1440, height: 900 }, // MacBook (13") logical resolution
      deviceScaleFactor: 2, // retina
      recordVideo: { dir: VIDEO_DIR, size: { width: 1440, height: 900 } },
    });
    const page = await context.newPage();

    try {
      await page.goto(`${BASE}/#/s/${sid}/chat`);

      // Interactive view mounted: chat section + state badge + opening agent turn.
      await expect(page.getByTestId("chat-section")).toBeVisible({ timeout: 15000 });
      await expect(page.getByTestId("current-state")).toHaveText("idle");
      await expect(
        page.getByTestId("chat-transcript").getByTestId("chat-row-agent").first(),
      ).toBeVisible();

      // Scene 0: opening state before any input. Linger so the viewer can read
      // the opening room.
      await shot(page, 0, "idle-open");
      await page.waitForTimeout(OPEN_DWELL);

      for (let i = 0; i < SCENES.length; i++) {
        const scene = SCENES[i];
        const prevAgentCount = await page
          .getByTestId("chat-transcript")
          .getByTestId("chat-row-agent")
          .count();

        if (scene.kind === "text") {
          // If multiple text intents are offered, pick the right one in the select.
          const select = page.getByTestId("composer-select");
          if ((await select.count()) > 0) {
            await select.selectOption(scene.intent);
          }
          // Type visibly so the viewer watches the message being composed.
          const input = page.getByTestId("composer-input");
          await input.click();
          await input.fill("");
          await input.pressSequentially(scene.text, { delay: TYPE_DELAY });
          await page.waitForTimeout(BEFORE_ACT);
          await page.getByTestId("composer-send").click();
        } else {
          // Beat before the click so the menu of action buttons is visible.
          await page.waitForTimeout(BEFORE_ACT);
          await page.getByTestId(`intent-btn-${scene.intent}`).click();
        }

        // State badge reflecting the landed state is the hard signal the turn
        // applied (assert it first; it holds for every scene).
        await expect(page.getByTestId("current-state")).toHaveText(scene.expectState, {
          timeout: 15000,
        });

        // A new agent turn must land for any scene whose state renders a room
        // view; a terminal exit (noAgentView) correctly pushes no bubble.
        if (scene.noAgentView) {
          await expect(
            page.getByTestId("chat-transcript").getByTestId("chat-row-agent"),
          ).toHaveCount(prevAgentCount);
        } else {
          await expect(
            page.getByTestId("chat-transcript").getByTestId("chat-row-agent"),
          ).toHaveCount(prevAgentCount + 1, { timeout: 15000 });
        }

        await shot(page, i + 1, scene.expectState);
        // Dwell on the landed room so the scene is readable in the recording.
        await page.waitForTimeout(DWELL);
      }

      // Terminal: live→done badge flips, input replaced by the done note.
      await expect(page.getByTestId("state-badge")).toHaveAttribute("data-terminal", "true");
      await expect(page.locator(".iv__done-note")).toBeVisible();
      // Linger on the completed chat + the accumulated trace.
      await page.waitForTimeout(FINAL_DWELL);

      // The trace timeline must have accumulated events across the run.
      const traceRows = page.locator(".iv__panel--timeline .trace-timeline__row");
      expect(await traceRows.count(), "expected accumulated trace rows").toBeGreaterThan(0);

      // Final scene: full chat + trace panel at the terminal state.
      await shot(page, SCENES.length + 1, "final-done-trace");
    } finally {
      await page.close();
      await context.close(); // flush the video
      await browser.close();
    }

    // Copy the recorded webm to a stable name for review.
    const webms = fs
      .readdirSync(VIDEO_DIR)
      .filter((f) => f.endsWith(".webm"))
      .map((f) => ({ f, t: fs.statSync(path.join(VIDEO_DIR, f)).mtimeMs }))
      .sort((a, b) => b.t - a.t);
    expect(webms.length, "expected a recorded video webm").toBeGreaterThan(0);
    const latest = path.join(VIDEO_DIR, webms[0].f);
    fs.copyFileSync(latest, path.join(ARTIFACT_DIR, "prd-chat.webm"));

    // Surface the artifact manifest in the test output.
    console.log("[web-chat] screenshots:\n" + screenshotPaths.join("\n"));
    console.log("[web-chat] video: " + latest);
    console.log("[web-chat] video (stable): " + path.join(ARTIFACT_DIR, "prd-chat.webm"));
  });
});
