/**
 * Proposal pipeline walkthrough video — tamagotchi-pets edition.
 *
 * Records the full proposal user flow from idea entry through to a published
 * proposal, using the tamagotchi-pets idea as realistic content. Designed for
 * UI review: every input, button, and room view is shown on-camera with
 * narration so a reviewer can judge whether the UI is clear and not confusing.
 *
 * Scenes (on-camera, behind a curtain for setup):
 *   1. Proposal intake  — type the idea, see the slug minted + search kick off
 *   2. Proposal search  — scout results: no overlap, confirm to continue
 *   3. Proposal refine  — brief editor open in IDE, refine analysis shown
 *   4. Ready / judge    — press "ready", brief judge fires, verdict: continue
 *   5. Proposal draft   — draft generated, ready to review
 *   6. Proposal done    — published confirmation, back to main
 *
 * No LLM: uses stories/dev-story/flows/proposal_tamagotchi_demo.yaml stubs.
 *
 * Record:  pnpm exec playwright test proposal-walkthrough --project=chromium
 * Fast:    WEB_CHAT_PACE=0 pnpm exec playwright test proposal-walkthrough --project=chromium
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page } from "@playwright/test";
import path from "path";
import {
  startWebServer,
  repoRoot,
  makeShot,
  waitForState,
  prepareVideoDir,
  saveVideoAsMp4,
  dwell,
  type WebServer,
} from "./_helpers/server.js";
import {
  DEMO_VIEWPORT,
  installCurtain,
  liftCurtain,
  makeCaption,
  captureDiagnostics,
} from "./_helpers/demo.js";

// Port distinct from all other specs.
const ADDR = "127.0.0.1:7754";
const STORY_DIR = path.join(repoRoot, "stories", "dev-story");
const FLOW = path.join(STORY_DIR, "flows", "proposal_tamagotchi_demo.yaml");
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "proposal-walkthrough");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");

let server: WebServer;
test.beforeAll(async () => {
  prepareVideoDir(VIDEO_DIR);
  server = await startWebServer({ addr: ADDR, flow: FLOW, storiesDir: STORY_DIR });
});
test.afterAll(() => server?.stop());

test("proposal pipeline walkthrough — tamagotchi pets", async () => {
  test.setTimeout(300000);
  const browser: Browser = await chromium.launch({ headless: true });
  const context: BrowserContext = await browser.newContext({
    viewport: { ...DEMO_VIEWPORT },
    recordVideo: { dir: VIDEO_DIR, size: { ...DEMO_VIEWPORT } },
  });
  const page: Page = await context.newPage();
  const video = page.video();
  const shot = makeShot(ARTIFACT_DIR);
  const { mark, onThrow } = captureDiagnostics(page, ARTIFACT_DIR);

  let sid = "";
  const submit = (intent: string, slots: Record<string, unknown> = {}) =>
    server.rpc("runstatus.session.submit", { session_id: sid, intent, slots });

  /** Click an intent button in the current page and wait for the target state. */
  const advance = async (intent: string, next: string, timeoutMs = 20000): Promise<void> => {
    mark(`advance:${intent}->${next}`);
    await page.getByTestId(`intent-btn-${intent}`).first().click();
    await waitForState(page, next, timeoutMs);
    await dwell(page, 1200);
  };

  await installCurtain(page, "kitsoki — proposal pipeline");

  try {
    // ── Off-camera setup: boot to proposal intake room ───────────────────
    await page.goto(`${server.base}/#/`);
    await expect(page.getByTestId("home-view")).toBeVisible({ timeout: 15000 });

    const card = page.locator("[data-testid='story-card']").filter({ hasText: /dev.story/i }).first();
    await card.getByTestId("new-session-btn").click();
    await page.waitForURL(/#\/s\/[0-9a-f-]{36}\/chat$/, { timeout: 15000 });
    sid = page.url().match(/\/s\/([0-9a-f-]{36})\/chat$/)?.[1] ?? "";
    await waitForState(page, "main", 15000);

    // Navigate to proposal intake room off-camera.
    await submit("go_idea", { message: "" });
    await waitForState(page, "proposal", 12000);

    // Reload so the curtain shows a clean intake view.
    await page.reload();
    await waitForState(page, "proposal", 12000);
    let beat = await makeCaption(page, 5000);
    await dwell(page, 500);
    await liftCurtain(page);

    // ── Scene 0: Proposal intake — enter the idea ────────────────────────
    mark("scene:intake");
    await beat("Start with an idea",
      "Type your idea in the chat. kitsoki will search for conflicts, scaffold a brief, and draft a proposal.", 6000);
    await shot(page, "00-proposal-intake");

    // Type the idea in the chat composer on-camera.
    await beat("Entering the idea…", "", 1500);
    const composer = page.getByTestId("composer-input").first();
    await composer.click();
    await composer.fill("I want to add tamagotchi-style virtual pets to the session UI");
    await dwell(page, 1500);
    await shot(page, "01-idea-typed");

    await beat("Submitting — kitsoki names the proposal and scouts for overlap…", "", 1500);
    await page.getByTestId("composer-send").first().click();
    await waitForState(page, "proposal_search", 20000);
    await dwell(page, 1000);

    // ── Scene 1: Proposal search — scout found no conflicts ──────────────
    mark("scene:search");
    await beat("Step 1 of 4 — scout search complete",
      "kitsoki searched existing proposals for overlap. No conflicts found — the idea is clear to proceed.", 5500);
    await shot(page, "02-proposal-search");

    await beat("Confirm to continue, or quit if the idea needs rethinking",
      "\"confirm\" advances to the next stage. \"quit\" drops back to the main menu.", 4000);
    await shot(page, "03-proposal-search-intents");

    // ── Scene 2: Refine brief — on-camera advance ────────────────────────
    mark("scene:refine");
    await beat("Advancing to the brief editor…",
      "kitsoki mints the workspace and scaffolds the brief template.", 2500);
    await advance("confirm", "proposal_refine", 20000);

    await beat("Step 2 of 4 — brief refinement",
      "The idea has been distilled into a structured brief. Review it — the IDE has the file open for editing.", 6000);
    await shot(page, "04-proposal-refine");

    await beat("The brief already captures the key gaps",
      "A refiner agent read the idea and noted: pet lifetime scope, and which UI surface hosts the widget.", 5000);
    await shot(page, "05-proposal-refine-analysis");

    await beat("When the brief looks right, press \"ready\"",
      "A judge reviews the brief for completeness. If it passes, the pipeline advances automatically.", 5000);

    // ── Scene 3: Ready / brief judge → advance ──────────────────────────
    mark("scene:ready");
    await beat("Pressing \"ready\" — the brief judge is running…",
      "A judge reviews the brief: clear why, scoped change, named kind.", 2000);
    // Click ready → brief_check fires (stub: continue) → engine bails to human
    // with advance_brief available. In human judge mode emit_intent defers to
    // the operator, so advance_brief appears as a choice item after the judge.
    await advance("ready", "proposal_refine", 15000);
    await dwell(page, 1000);
    await shot(page, "06-judge-approved");

    await beat("Brief approved — advancing to the draft stage",
      "The judge verdict is \"continue\". Click \"advance to draft\" to proceed.", 3000);
    // advance_brief is now the primary action — click it on-camera.
    await advance("advance_brief", "proposal_draft", 20000);

    // ── Scene 4: Proposal draft ──────────────────────────────────────────
    mark("scene:draft");
    await beat("Step 3 of 4 — proposal draft generated",
      "The judge approved the brief (verdict: continue). A draft author wrote the full proposal document.", 6000);
    await shot(page, "07-proposal-draft");

    await beat("Review the draft, then accept to publish",
      "\"accept\" runs the publish script: moves the draft from the workspace to docs/proposals/.", 5000);
    await shot(page, "08-proposal-draft-intents");

    // ── Scene 5: Publish (accept) ────────────────────────────────────────
    mark("scene:publish");
    await beat("Publishing…",
      "The proposal moves from the .workspace/ staging area to docs/proposals/tamagotchi-pet-ui.md.", 2500);
    // RPC + reload for the same reliability reason as the ready step.
    await submit("accept", {});
    await page.reload();
    await waitForState(page, "proposal_done", 20000);
    beat = await makeCaption(page, 5000);
    await dwell(page, 500);

    // ── Scene 6: Done ────────────────────────────────────────────────────
    mark("scene:done");
    await beat("Step 4 of 4 — proposal published",
      "docs/proposals/tamagotchi-pet-ui.md is now part of the queue. Go back to the main menu to pick up the next ticket.", 7000);
    await shot(page, "09-proposal-done");

    await beat("Back to the main menu",
      "The full pipeline: idea → scout → brief → draft → published.", 3000);
    await advance("go_main", "main", 12000);
    await shot(page, "10-back-to-main");

  } catch (err) {
    onThrow(err);
    throw err;
  } finally {
    await context.close();
    await saveVideoAsMp4(video, ARTIFACT_DIR, "proposal-walkthrough");
    await browser.close();
  }
});
