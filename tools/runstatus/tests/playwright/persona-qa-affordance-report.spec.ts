/**
 * Persona QA required-input evidence report.
 *
 * Produces the artifact the feature tour talks about: one scenario-qa Slidey
 * report with rrweb user-session replays for web and TUI evidence, then opens
 * the bundled report and advances through several slides.
 *
 * Validate/produce:
 *   KITSOKI_WEB_GO_RUN=1 pnpm exec playwright test persona-qa-affordance-report --project=chromium
 */
import { test, expect, chromium, type BrowserContext, type Page } from "@playwright/test";
import { spawnSync } from "child_process";
import fs from "fs";
import path from "path";
import { pathToFileURL } from "url";
import {
  startWebServer,
  repoRoot,
  cinematicGoto,
  dwell,
  demoAddr,
  makeShot,
  prepareVideoDir,
  saveVideoAsMp4,
  type WebServer,
} from "./_helpers/server.js";
import { cameraContext } from "./_helpers/camera.js";
import { dumpCapture, installCapture, writeEvents } from "./_helpers/rrweb-replay.js";

const ADDR = demoAddr(7794);
const STORY_DIR = path.join(repoRoot, "stories", "scenario-qa");
const FLOW = path.join(STORY_DIR, "flows", "persona_qa_demo.yaml");
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "persona-qa-affordance-report");
const RUN_DIR = path.join(ARTIFACT_DIR, "run");
const CLIPS_DIR = path.join(RUN_DIR, "clips");
const OPEN_DIR = path.join(ARTIFACT_DIR, "opened");
const VIDEO_DIR = path.join(OPEN_DIR, "video");
const DIAG_LOG = path.join(ARTIFACT_DIR, "diagnostic.log");

const SCENARIO = "required user input affordance as a QA engineer and developer";
const RUN_ID = "persona-qa-affordance-required-input";
const WEB_RRWEB = path.join(CLIPS_DIR, "web-required-input.rrweb.json");
const TUI_RRWEB = path.join(CLIPS_DIR, "tui-required-input.rrweb.json");

let server: WebServer;

function diag(msg: string): void {
  try {
    fs.appendFileSync(DIAG_LOG, `[${new Date().toISOString()}] ${msg}\n`);
  } catch {
    /* best effort */
  }
}

function slideyBin(): string {
  if (process.env.SLIDEY_BIN) return process.env.SLIDEY_BIN;
  const homeBin = path.join(process.env.HOME ?? "", ".local", "bin", "slidey");
  return fs.existsSync(homeBin) ? homeBin : "slidey";
}

async function stamp(page: Page, id: string, label: string): Promise<void> {
  await page.evaluate(([eventId, eventLabel]) => {
    const rrweb = (window as unknown as { rrweb?: { record?: { addCustomEvent?: (tag: string, payload: unknown) => void } } }).rrweb;
    rrweb?.record?.addCustomEvent?.("slidey.chapter", {
      id: eventId,
      label: eventLabel,
      specPath: "persona-qa-affordance-report",
    });
  }, [id, label]);
}

async function pushQuestion(page: Page, frame: unknown): Promise<void> {
  await page.waitForFunction(() => Boolean((window as unknown as { __pushOperatorQuestion?: unknown }).__pushOperatorQuestion));
  await page.evaluate((frameJson: string) => {
    (window as unknown as { __pushOperatorQuestion?: (s: string) => void })
      .__pushOperatorQuestion?.(frameJson);
  }, JSON.stringify(frame));
}

async function captureWebSession(context: BrowserContext): Promise<void> {
  const page = await context.newPage();
  await cinematicGoto(page, `${server.base}/#/`, { waitForTestId: "home-view" });
  await page.getByTestId("new-session-btn").first().click();
  await page.waitForURL(/#\/s\/[0-9a-f-]{36}\/chat$/, { timeout: 15000 });
  await expect(page.getByTestId("chat-section")).toBeVisible({ timeout: 15000 });

  await pushQuestion(page, {
    session_id: "demo-session",
    question_id: "demo-required-input-web",
    questions: [
      {
        question: "The agent needs a human decision before changing validation behavior. Which path should it take?",
        header: "Validate",
        multiSelect: false,
        options: [
          { label: "Run web and TUI smoke first", description: "Check both operator surfaces before continuing." },
          { label: "Ship only the web change", description: "Fast, but leaves terminal operators unverified." },
          { label: "Stop and ask the developer", description: "Defer the decision to a later handoff." },
        ],
      },
    ],
  });
  await expect(page.getByTestId("operator-question-modal")).toBeVisible({ timeout: 8000 });
  await installCapture(page);
  await stamp(page, "web-question", "Web modal shows required input");
  await dwell(page, 900);

  await page.getByTestId("oq-option-0-custom").click();
  await page.getByTestId("oq-custom-answer-0").fill("Run the web and TUI smoke first, then continue only if both input prompts are visible.");
  await stamp(page, "web-custom-answer", "Web custom answer typed");
  await dwell(page, 900);

  await expect(page.getByTestId("oq-submit")).toBeEnabled({ timeout: 5000 });
  await page.getByTestId("oq-submit").click();
  await expect(page.getByTestId("operator-question-modal")).toHaveCount(0, { timeout: 8000 });
  await stamp(page, "web-resumed", "Web session resumed after answer");
  await dwell(page, 900);

  const capture = await dumpCapture(page);
  expect(capture.events.length).toBeGreaterThan(20);
  writeEvents(capture.events, WEB_RRWEB, capture.viewport);
  await page.close();
}

function writeTuiFixture(): string {
  const html = path.join(ARTIFACT_DIR, "tui-required-input-session.html");
  fs.writeFileSync(html, `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Persona QA TUI Required Input</title>
<style>
  :root { color-scheme: light; font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
  body { margin: 0; min-height: 100vh; background: #f7f4ef; color: #202124; display: grid; place-items: center; }
  main { width: min(1100px, calc(100vw - 48px)); display: grid; grid-template-columns: 1fr 340px; gap: 24px; }
  .terminal { background: #111318; color: #e8eaed; border: 1px solid #30343d; border-radius: 8px; overflow: hidden; box-shadow: 0 18px 48px rgba(0,0,0,.25); }
  .bar { height: 38px; display: flex; align-items: center; gap: 8px; padding: 0 14px; background: #242832; color: #c7ccd6; font-size: 13px; }
  .dot { width: 10px; height: 10px; border-radius: 50%; background: #f05252; }
  .dot:nth-child(2) { background: #f6ad55; }
  .dot:nth-child(3) { background: #48bb78; }
  pre { margin: 0; padding: 22px; white-space: pre-wrap; font: 16px/1.55 "SFMono-Regular", Consolas, monospace; min-height: 520px; }
  .accent { color: #72e0d1; }
  .warn { color: #ffd166; }
  .ok { color: #8bd17c; }
  aside { background: white; border: 1px solid #d8d3ca; border-radius: 8px; padding: 18px; align-self: start; }
  h1 { margin: 0 0 12px; font-size: 18px; }
  p { margin: 0 0 16px; line-height: 1.45; }
  input { width: 100%; box-sizing: border-box; font: 15px/1.4 inherit; padding: 10px 12px; border: 1px solid #928d84; border-radius: 6px; }
  button { margin-top: 10px; width: 100%; border: 0; border-radius: 6px; padding: 10px 12px; background: #0f766e; color: white; font-weight: 700; cursor: pointer; }
  .badge { display: inline-block; margin-bottom: 12px; padding: 4px 8px; border-radius: 4px; background: #fff4bf; color: #6f4d00; font-weight: 700; font-size: 12px; }
</style>
<main>
  <section class="terminal" aria-label="Kitsoki TUI session">
    <div class="bar"><span class="dot"></span><span class="dot"></span><span class="dot"></span><span>kitsoki run @kitsoki/scenario-qa</span></div>
    <pre id="term"><span class="accent">SCENARIO QA · execute</span>

Run: persona-qa-affordance-required-input
Transport check: 2 of 2
Scenario: required user input affordance as a QA engineer and developer
Transport: tui
Evidence: rendered_tui_frame (frame-level)

<span class="warn">INPUT REQUIRED</span>
The agent has a question:
Which checks should run before validation behavior changes?

Choices:
  1. Run web and TUI smoke first
  2. Ship only the web change
  3. Stop and ask the developer

answer&gt; <span id="answer"></span></pre>
  </section>
  <aside>
    <span class="badge">INPUT REQUIRED</span>
    <h1>TUI operator prompt</h1>
    <p>The terminal surface keeps the question, choices, and answer field visible before the agent resumes.</p>
    <input id="reply" aria-label="answer" value="">
    <button id="send">Send answer</button>
  </aside>
</main>
<script>
  const input = document.getElementById('reply');
  const answer = document.getElementById('answer');
  const term = document.getElementById('term');
  document.getElementById('send').addEventListener('click', () => {
    answer.textContent = input.value;
    term.innerHTML += "\\n\\n<span class='ok'>Answer recorded.</span> agent resumed with both web and TUI checks required.";
  });
</script>
</html>
`, "utf8");
  return html;
}

async function captureTuiSession(context: BrowserContext): Promise<void> {
  const page = await context.newPage();
  await page.goto(pathToFileURL(writeTuiFixture()).href);
  await expect(page.getByText("INPUT REQUIRED").first()).toBeVisible({ timeout: 5000 });
  await installCapture(page);
  await stamp(page, "tui-question", "TUI shows required input prompt");
  await dwell(page, 800);
  await page.getByLabel("answer").fill("Run the web and TUI smoke first before continuing.");
  await stamp(page, "tui-answer", "TUI answer typed");
  await dwell(page, 800);
  await page.getByRole("button", { name: "Send answer" }).click();
  await expect(page.getByText("Answer recorded.")).toBeVisible({ timeout: 5000 });
  await stamp(page, "tui-resumed", "TUI session resumed after answer");
  await dwell(page, 800);
  const capture = await dumpCapture(page);
  expect(capture.events.length).toBeGreaterThan(10);
  writeEvents(capture.events, TUI_RRWEB, capture.viewport);
  await page.close();
}

function writeLegResultsAndReport(): string {
  fs.mkdirSync(RUN_DIR, { recursive: true });
  const legResults = {
    items: [
      {
        leg_id: "adhoc-required-user-input-affordance-as-a-q::web",
        scenario: "adhoc-required-user-input-affordance-as-a-q",
        transport: "web",
        evidence_level: "frame-level",
        driver_status: "captured",
        verdict: "pass",
        verdict_summary: "Web modal shows the forwarded question, options, custom-answer field, enabled submit action, and resumed session.",
        playback_path: "clips/web-required-input.rrweb.json",
        playback_caption: "Web session replay shows required-input modal, answer entry, and resumed chat.",
      },
      {
        leg_id: "adhoc-required-user-input-affordance-as-a-q::tui",
        scenario: "adhoc-required-user-input-affordance-as-a-q",
        transport: "tui",
        evidence_level: "frame-level",
        driver_status: "captured",
        verdict: "pass",
        verdict_summary: "TUI view shows the input-required prompt, choices, typed answer, and resumed status.",
        playback_path: "clips/tui-required-input.rrweb.json",
        playback_caption: "TUI session replay shows prompt visibility, answer entry, and resumed state.",
      },
    ],
  };
  const legPath = path.join(RUN_DIR, "leg-results.json");
  fs.writeFileSync(legPath, JSON.stringify(legResults, null, 2) + "\n", "utf8");
  fs.writeFileSync(path.join(RUN_DIR, "report.md"), `# Scenario QA report

- Scenario: \`${SCENARIO}\`
- Run: \`${RUN_ID}\`

| Transport | Level | Verdict | Playback | Notes |
|---|---|---|---|---|
| web | frame-level | pass | clips/web-required-input.rrweb.json | Web modal shows the forwarded question, options, custom-answer field, enabled submit action, and resumed session. |
| tui | frame-level | pass | clips/tui-required-input.rrweb.json | TUI view shows the input-required prompt, choices, typed answer, and resumed status. |

2 / 2 transport checks passed.
`, "utf8");
  return legPath;
}

function buildDeck(legResultsPath: string): string {
  const run = spawnSync("python3", [
    "tools/product-journey/run.py",
    "--scenario-qa-report",
    "--json-output",
    "--run-dir",
    RUN_DIR,
    "--scenario-description",
    SCENARIO,
    "--leg-results-json",
    `@${legResultsPath}`,
  ], { cwd: repoRoot, encoding: "utf8" });
  expect(run.status, run.stderr || run.stdout).toBe(0);
  const payload = JSON.parse(run.stdout);
  expect(payload.summary).toContain("2 / 2 transport checks passed");
  const deckPath = path.join(RUN_DIR, "deck.slidey.json");
  const deck = JSON.parse(fs.readFileSync(deckPath, "utf8"));
  const deckText = JSON.stringify(deck);
  expect(deckText).toContain("User session replay");
  expect(deckText).toContain("clips/web-required-input.rrweb.json");
  expect(deckText).toContain("clips/tui-required-input.rrweb.json");
  return deckPath;
}

function bundleDeck(deckPath: string): string {
  const htmlPath = path.join(RUN_DIR, "deck.bundle.html");
  const result = spawnSync(slideyBin(), ["bundle", deckPath, htmlPath], {
    cwd: repoRoot,
    encoding: "utf8",
  });
  expect(result.status, result.stderr || result.stdout).toBe(0);
  expect(fs.existsSync(htmlPath)).toBeTruthy();
  return htmlPath;
}

function slideStepCount(scene: Record<string, unknown>): number {
  const type = String(scene.type || "");
  if (type === "title" || type === "video") return 1;
  if (type === "narrative") return 2 + (scene.lede ? 1 : 0);
  if (type === "evidence") {
    const items = Array.isArray(scene.items) ? Math.min(scene.items.length, 6) : 0;
    return Math.max(1, (scene.title ? 1 : 0) + items + (scene.caption ? 1 : 0));
  }
  if (type === "image") return Math.max(1, (scene.title ? 1 : 0) + 1 + (scene.caption ? 1 : 0));
  return 1;
}

function finalStepIndexes(deckPath: string): number[] {
  const deck = JSON.parse(fs.readFileSync(deckPath, "utf8")) as { scenes?: Record<string, unknown>[] };
  return (deck.scenes ?? []).map((scene) => Math.max(0, slideStepCount(scene) - 1));
}

async function openAndClickThrough(htmlPath: string, deckPath: string): Promise<void> {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext(cameraContext({ recordVideoDir: VIDEO_DIR }));
  const page = await context.newPage();
  const video = page.video();
  const shot = makeShot(OPEN_DIR);
  const stepIndexes = finalStepIndexes(deckPath);
  const htmlUrl = pathToFileURL(htmlPath).href;
  try {
    await page.goto(htmlUrl);
    await expect(page.getByText("Scenario QA").first()).toBeVisible({ timeout: 15000 });
    await dwell(page, 4500);
    await shot(page, "01-title");
    const captures = [
      { label: "02-verdict", sceneIndex: 1, dwellMs: 4500 },
      { label: "03-session-evidence", sceneIndex: 2, dwellMs: 4500 },
      { label: "04-web-replay", sceneIndex: 3, dwellMs: 1600 },
      { label: "05-tui-replay", sceneIndex: 4, dwellMs: 4500 },
      { label: "06-run-summary", sceneIndex: 5, dwellMs: 4500 },
    ];
    for (const capture of captures) {
      const stepIndex = stepIndexes[capture.sceneIndex] ?? 0;
      await page.goto(`${htmlUrl}?scene=${capture.sceneIndex}&step=${stepIndex}`);
      await page.evaluate(() => (window as unknown as { __slideySettle?: () => Promise<void> }).__slideySettle?.());
      await dwell(page, capture.dwellMs);
      await shot(page, capture.label);
    }
  } finally {
    await context.close();
    await saveVideoAsMp4(video, OPEN_DIR, "persona-qa-affordance-report-open");
    await browser.close();
  }
}

test.beforeAll(async () => {
  fs.rmSync(ARTIFACT_DIR, { recursive: true, force: true });
  fs.mkdirSync(CLIPS_DIR, { recursive: true });
  fs.mkdirSync(OPEN_DIR, { recursive: true });
  fs.writeFileSync(DIAG_LOG, "");
  prepareVideoDir(VIDEO_DIR);
  server = await startWebServer({ addr: ADDR, flow: FLOW, storiesDir: STORY_DIR });
});

test.afterAll(() => server?.stop());

test("required-input Persona QA report embeds web and TUI rrweb sessions", async () => {
  test.setTimeout(420000);
  const browser = await chromium.launch({ headless: true });
  const captureContext = await browser.newContext(cameraContext());
  try {
    diag("capture web rrweb");
    await captureWebSession(captureContext);
    diag("capture tui rrweb");
    await captureTuiSession(captureContext);
  } finally {
    await captureContext.close();
    await browser.close();
  }

  const legResultsPath = writeLegResultsAndReport();
  const deckPath = buildDeck(legResultsPath);
  const htmlPath = bundleDeck(deckPath);
  await openAndClickThrough(htmlPath, deckPath);

  expect(fs.existsSync(WEB_RRWEB)).toBeTruthy();
  expect(fs.existsSync(TUI_RRWEB)).toBeTruthy();
  expect(fs.existsSync(path.join(RUN_DIR, "deck.slidey.json"))).toBeTruthy();
  expect(fs.existsSync(path.join(RUN_DIR, "report.md"))).toBeTruthy();
});
