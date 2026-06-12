/**
 * External-target PRD → Design video — gears-rust edition.
 *
 * Records the gears-rust POC: dev-story pointed at a FOREIGN repo
 * (constructorfabric/gears-rust), driving a gear's PRD → Design walk so the
 * docs publish into the gears-rust checkout as gears-sdlc PRD.md / DESIGN.md.
 * The whole video is TOUR-DRIVEN (src/tour/generated/gears-prd-design.ts via
 * window.__startTourWithSteps) and stays in the MAIN CHAT: home story library
 * → new session → the chat → author a PRD by talking it through → watch it
 * publish into the gears tree → continue into the design intake → author the
 * gears-sdlc DESIGN that publishes alongside it.
 *
 * THE CONVERSATION IS THE DEMO. Every pipeline turn is driven THROUGH THE PAGE
 * (composer fills + intent-button clicks), not via off-camera RPC — so each
 * turn renders into the chat transcript the spotlight then frames. (An
 * RPC-driven turn advances server state but never renders in the driving
 * page's transcript, which is why this spec clicks.)
 *
 * No LLM: stubs from stories/gears-rust/flows/prd_to_design_full.yaml (the
 * full single-session walk, also a `test flows` fixture).
 *
 * Record:  pnpm exec playwright test gears-prd-design --project=chromium
 * Fast:    WEB_CHAT_PACE=0 pnpm exec playwright test gears-prd-design --project=chromium
 */
import { test, expect, chromium, type Browser, type BrowserContext, type Page, type Locator } from "@playwright/test";
import path from "path";
import {
  startWebServer,
  repoRoot,
  makeShot,
  waitForState,
  prepareVideoDir,
  saveVideoAsMp4,
  dwell,
  cinematicGoto,
  SETTLE_MS,
  ChapterRecorder,
  writeChapters,
  type WebServer,
} from "./_helpers/server.js";
import { DEMO_VIEWPORT, captureDiagnostics } from "./_helpers/demo.js";
import { GEARS_PRD_DESIGN_TOUR_STEPS, type TourStep } from "../../src/tour/generated/gears-prd-design.js";

// Port distinct from all other specs (7740–7758 taken).
const ADDR = "127.0.0.1:7759";
const STORY_DIR = path.join(repoRoot, "stories", "gears-rust");
const FLOW = path.join(STORY_DIR, "flows", "prd_to_design_full.yaml");
const ARTIFACT_DIR = path.join(repoRoot, ".artifacts", "gears-prd-design");
const VIDEO_DIR = path.join(ARTIFACT_DIR, "video");
// Feature-catalog source of truth for this spec's tour steps — each step
// becomes a chapter in the MP4's sidecar.
const CHAPTER_SOURCE = "features/gears-prd-design.yaml";

let server: WebServer;
test.beforeAll(async () => {
  prepareVideoDir(VIDEO_DIR);
  server = await startWebServer({ addr: ADDR, flow: FLOW, storiesDir: STORY_DIR });
});
test.afterAll(() => server?.stop());

async function resolveTarget(page: Page, step: TourStep): Promise<Locator> {
  return page.getByTestId(step.target!).first();
}

test("external-target PRD → Design — gears-rust", async () => {
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

  // Accumulate per-step time windows for the chapter sidecar. The clock starts
  // now so windows line up with the recorded MP4 timeline.
  const chapters = new ChapterRecorder();

  // ── Page-driving helpers: drive every turn THROUGH THE PAGE so the chat
  // transcript renders it. DOM-dispatch the click so it fires through the
  // tour overlay regardless of paint order (the overlay backdrop is otherwise
  // a hit-test wall over controls below the spotlight). ───────────────────────
  const clickIntent = async (intent: string) => {
    const btn = page.getByTestId(`intent-btn-${intent}`).first();
    await expect(btn).toBeVisible({ timeout: 15000 });
    await btn.scrollIntoViewIfNeeded().catch(() => undefined);
    await btn.evaluate((el) => (el as HTMLElement).click());
  };
  const typeAndSend = async (text: string) => {
    const input = page.getByTestId("composer-input").first();
    await expect(input).toBeVisible({ timeout: 15000 });
    await input.fill(text);
    await dwell(page, 200); // let v-model settle so composer-send enables
    await page.getByTestId("composer-send").first().evaluate((el) => (el as HTMLElement).click());
  };
  // ── Natural per-turn conversation rhythm ──────────────────────────────────
  // The chat component auto-scrolls to the bottom INSTANTLY on every new message
  // (a watcher does `scrollTop = scrollHeight`). That instant jump is the
  // unreadable "fast scroll" — and a bulk pan after all turns are driven reads
  // as a mechanical sweep, not a conversation. So we DISABLE the native jump and
  // drive scrolling ourselves, one turn at a time, the way a person reads:
  //
  //   send the input → ease it up to the TOP of the chat → pause (read input +
  //   the start of the reply) → ease DOWN through the reply → pause → next turn.
  //
  // Easing uses a custom rAF tween so the DURATION (pace) is ours to set, slow
  // and readable. Everything scales with WEB_CHAT_PACE, so fast-validation
  // (PACE=0) collapses to instant while a watch-speed record (PACE=1) is calm.
  const PACE = Number(process.env.WEB_CHAT_PACE ?? "1");
  const paced = (ms: number) => Math.round(ms * PACE);
  const SCROLL_UP_MS = 1200;  // ease the new input up to the top
  const READ_INPUT_MS = 1300; // hold on the input + the reply's opening
  const READ_REPLY_MS = 1500; // hold on the reply before the next turn

  let scrollReady = false;
  const ensureScrollControl = async () => {
    if (scrollReady) return;
    scrollReady = await page.evaluate(() => {
      const el = document.querySelector('[data-testid="chat-transcript"]') as HTMLElement | null;
      if (!el) return false;
      const tag = el as unknown as { __nat?: boolean };
      if (tag.__nat) return true;
      tag.__nat = true;
      const desc = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop")!;
      const realGet = () => (desc.get as () => number).call(el);
      const realSet = (v: number) => (desc.set as (v: number) => void).call(el, v);
      // Neuter the component's instant auto-scroll-to-bottom; we drive scroll.
      Object.defineProperty(el, "scrollTop", {
        configurable: true,
        get() { return realGet(); },
        set() { /* ignored — natural scroll is driven via __ease below */ },
      });
      const w = window as unknown as Record<string, unknown>;
      w.__ease = (to: number, ms: number) =>
        new Promise<void>((res) => {
          const from = realGet();
          const max = el.scrollHeight - el.clientHeight;
          const target = Math.max(0, Math.min(to, max));
          if (ms <= 0 || Math.abs(target - from) < 2) { realSet(target); return res(); }
          const t0 = performance.now();
          const tick = (now: number) => {
            const p = Math.min(1, (now - t0) / ms);
            const e = p < 0.5 ? 2 * p * p : 1 - Math.pow(-2 * p + 2, 2) / 2; // easeInOutQuad
            realSet(from + (target - from) * e);
            if (p < 1) requestAnimationFrame(tick); else res();
          };
          requestAnimationFrame(tick);
        });
      w.__lastUserTop = () => {
        const rows = el.querySelectorAll('[data-testid="chat-row-user"]');
        const last = rows[rows.length - 1] as HTMLElement | undefined;
        return last ? Math.max(0, last.offsetTop - 16) : el.scrollHeight; // a little headroom
      };
      w.__scrollMax = () => el.scrollHeight - el.clientHeight;
      return true;
    });
  };
  const ease = async (to: number, ms: number) => {
    await page.evaluate(
      ([to, ms]) => (window as unknown as { __ease: (a: number, b: number) => Promise<void> }).__ease(to, ms),
      [to, ms] as [number, number],
    );
  };
  let revealCount = 0;
  // Drive ONE turn, then reveal it the way a reader follows it. `wait` is the
  // state to settle on (some turns advance the room, some self-loop); `label`
  // names the captured frames.
  const revealTurn = async (action: () => Promise<void>, opts: { wait?: string; label: string }) => {
    await action();
    if (opts.wait) await waitForState(page, opts.wait, 20000);
    await dwell(page, SETTLE_MS); // let the turn's rows render
    await ensureScrollControl();
    // 1. New operator input → ease it to the top of the chat; hold.
    const top = await page.evaluate(() => (window as unknown as { __lastUserTop?: () => number }).__lastUserTop?.() ?? 0);
    await ease(top, paced(SCROLL_UP_MS));
    await dwell(page, paced(READ_INPUT_MS));
    await shot(page, `${opts.label}-in`);
    // 2. Ease DOWN through the reply (duration tracks the distance, slow +
    //    readable); hold on the finished reply. No-op when it already fits.
    const max = await page.evaluate(() => (window as unknown as { __scrollMax?: () => number }).__scrollMax?.() ?? 0);
    const span = Math.max(0, max - top);
    await ease(max, paced(Math.min(3000, Math.max(700, Math.round(span * 3)))));
    await dwell(page, paced(READ_REPLY_MS));
    await shot(page, `${opts.label}-out`);
    revealCount++;
  };

  try {
    // ── 1. Open the home story library and start the tour ON it ──────────────
    mark("navigating home");
    await cinematicGoto(page, `${server.base}/#/`, { waitForTestId: "home-view" });

    // THE CONVERSATION IS THE DEMO. Hide the secondary trace panel and give the
    // chat ~70% of the frame, pinned RIGHT — so the dialogue text is large and
    // stays legible after a vision QA pass downsamples the frame, while the left
    // ~30% stays an empty gutter the tour popover (placement: left) sits in
    // WITHOUT covering the left-aligned room banners or the right-aligned
    // operator bubbles. Injected as a style tag (no SPA rebuild); harmless on
    // the home view, applies once the chat renders and persists across the SPA's
    // hash-route navigation.
    await page.addStyleTag({
      content: `
        .iv__trace { display: none !important; }
        .iv__chat { flex: 0 0 70% !important; margin-left: 30% !important; border-right: none !important; }
      `,
    });

    await page.evaluate((stepsJson: string) => {
      (window as unknown as { __startTourWithSteps?: (s: string) => void })
        .__startTourWithSteps?.(stepsJson);
    }, JSON.stringify(GEARS_PRD_DESIGN_TOUR_STEPS));
    await expect(page.getByTestId("tour-overlay")).toBeVisible({ timeout: 8000 });

    // ── 2. Walk the GEARS_PRD_DESIGN_TOUR_STEPS ──────────────────────────────
    for (const step of GEARS_PRD_DESIGN_TOUR_STEPS) {
      mark(`step ${step.id}`);
      const currentUrl = page.url();
      const currentRouteKind = currentUrl.includes("/chat")
        ? "interactive"
        : currentUrl.match(/#\/s\/[0-9a-f-]{36}$/)
          ? "any"
          : "home";
      if (step.route !== "any" && step.route !== currentRouteKind) {
        mark(`  route-skip (${currentRouteKind})`);
        continue;
      }

      // Interactive beats drive + reveal their whole conversation in the hook
      // below, so open the chapter NOW (its popover is already showing) so the
      // chapter window spans the conversation, not just a trailing dwell. The
      // center/home steps open theirs after their hold (further down).
      if (step.route === "interactive") {
        chapters.open(step.id, step.title, CHAPTER_SOURCE);
      }

      // ── Pre-step setup: drive the pipeline THROUGH THE PAGE so this step's
      // chat content exists and is on-screen before the spotlight lands. ──────
      if (step.id === "gr-prd-discovery") {
        // `main` is a SEMANTIC room (no intent buttons); the deterministic
        // router matches the typed verb "prd" → go_prd with no LLM, exactly as
        // an operator would type it. From prd.idle on, rooms expose composers
        // and intent buttons, so the rest is driven by composer + clicks.
        await waitForState(page, "core.main", 15000);
        await revealTurn(() => typeAndSend("prd"), { wait: "core.prd.idle", label: "prd-enter" });
        await revealTurn(() => typeAndSend("I want a multi-tenant notes-service gear for the platform"), {
          wait: "core.prd.idle",
          label: "prd-pitch",
        });
      }
      if (step.id === "gr-prd-clarify") {
        await revealTurn(() => clickIntent("core__prd__start"), { wait: "core.prd.search", label: "prd-start" });
        await revealTurn(() => clickIntent("core__prd__confirm"), { wait: "core.prd.clarifying", label: "prd-scout" });
        // Round 1: answer the questions, then submit. clarifying has no submit
        // BUTTON — the verbs live in prose, caught by the deterministic router
        // ("submit" is a submit_answers example).
        await revealTurn(() => typeAndSend("Platform users; the metric is notes-saved-per-session"), {
          wait: "core.prd.clarifying",
          label: "prd-answer1",
        });
        await revealTurn(() => typeAndSend("submit"), { wait: "core.prd.brief", label: "prd-submit1" });
        // Round 2: the brief's `clarify` loops back for another round,
        // preserving the accumulated transcript.
        await revealTurn(() => clickIntent("core__prd__clarify"), { wait: "core.prd.clarifying", label: "prd-round2" });
        await revealTurn(() => typeAndSend("Tenant isolation is mandatory; admins see only aggregate metrics"), {
          wait: "core.prd.clarifying",
          label: "prd-answer2",
        });
      }
      if (step.id === "gr-prd-draft") {
        await revealTurn(() => typeAndSend("submit"), { wait: "core.prd.brief", label: "prd-submit2" });
        await revealTurn(() => clickIntent("core__prd__confirm"), { wait: "core.prd.references", label: "prd-brief-ok" });
        await revealTurn(() => clickIntent("core__prd__confirm"), { wait: "core.prd.drafting", label: "prd-refs-ok" });
      }
      if (step.id === "gr-published") {
        await revealTurn(() => clickIntent("core__prd__accept"), { wait: "core.prd_published", label: "prd-publish" });
      }
      if (step.id === "gr-design-intake") {
        // prd_published uses a prose `list:` (no choice buttons) → semantic
        // room; "continue" is a continue-intent example matched by the router.
        await revealTurn(() => typeAndSend("continue"), { wait: "core.design", label: "design-handoff" });
      }
      if (step.id === "gr-design-refine") {
        await revealTurn(() => typeAndSend("Realize the notes-service PRD as a gears-sdlc DESIGN"), {
          wait: "core.design_search",
          label: "design-pitch",
        });
        await revealTurn(() => clickIntent("core__confirm"), { wait: "core.design_refine", label: "design-scout" });
        // Re-run the refiner on the brief — the design's refine loop. The
        // `refine` choice fires `discuss` (intent-btn-core__discuss); the
        // refiner reworks the brief and the gaps update, then `ready` checks it.
        await revealTurn(() => clickIntent("core__discuss"), { wait: "core.design_refine", label: "design-refine" });
      }
      if (step.id === "gr-design-done") {
        // `ready` re-enters design_refine and arms the brief judge; once it
        // returns `continue`, advance_brief appears as a choice the operator
        // clicks (the UI does not auto-advance the way a flow turn does).
        await revealTurn(() => clickIntent("core__ready"), { wait: "core.design_refine", label: "design-ready" });
        await expect(page.getByTestId("intent-btn-core__advance_brief").first()).toBeVisible({ timeout: 20000 });
        await revealTurn(() => clickIntent("core__advance_brief"), { wait: "core.design_draft", label: "design-advance" });
        await revealTurn(() => clickIntent("core__accept"), { wait: "core.design_done", label: "design-publish" });
      }

      if (step.waitForTarget) {
        await expect(page.getByTestId(step.waitForTarget).first()).toBeVisible({ timeout: 15000 });
      }

      // Anti-drift assertion: the popover must show THIS step's title.
      const titleEl = page.getByTestId("tour-title");
      const actualTitle = await titleEl.textContent({ timeout: 8000 }).catch(() => "");
      if (actualTitle !== step.title) {
        const remaining = GEARS_PRD_DESIGN_TOUR_STEPS.slice(
          GEARS_PRD_DESIGN_TOUR_STEPS.indexOf(step) + 1
        );
        if (remaining.some((s) => s.title === actualTitle)) {
          mark(`  drift-skip: overlay on "${actualTitle}"`);
          continue;
        }
      }
      await expect(titleEl).toHaveText(step.title, { timeout: 12000 });

      // Interactive chat beats already revealed + captured each turn at a
      // readable pace (the chapter for them was opened before the hook so its
      // window spans the conversation); the center intro/recap steps get the
      // normal hold + chapter + frame here.
      if (step.route !== "interactive") {
        chapters.open(step.id, step.title, CHAPTER_SOURCE);
        await dwell(page, step.dwellMs ?? 3000);
        await shot(page, step.id);
      }

      if (step.kind === "explain") {
        await page.getByTestId("tour-next").click();
        await dwell(page, 700);
      } else {
        const target = await resolveTarget(page, step);
        await target.scrollIntoViewIfNeeded().catch(() => undefined);
        if (step.advance === "route-match") {
          await target.click();
          await page.waitForTimeout(300);
          if (step.advanceRoute === "interactive") {
            await page.waitForURL(/#\/s\/[0-9a-f-]{36}\/chat$/, { timeout: 15000 });
          } else if (step.advanceRoute === "any") {
            await page.waitForURL(/#\/s\/[0-9a-f-]{36}$/, { timeout: 15000 });
          }
          await dwell(page, 1000);
        } else {
          await target.evaluate((el) => (el as HTMLElement).click());
          await dwell(page, 1000);
        }
      }
    }

    // The final step's "Done" closes the tour.
    await expect(page.getByTestId("tour-overlay")).toHaveCount(0, { timeout: 5000 });

    // Deterministic guard (no LLM): every conversation turn must have been
    // revealed one-at-a-time (input eased to top → reply eased through), each
    // capturing its own held -in / -out frames. A regression that drops the
    // per-turn reveal — or skips driving the walk — collapses this count and
    // fails here, catching "scrolled too fast / not all turns shown" in the
    // deterministic test layer (the vision QA scenarios are the higher-level
    // legibility check). The full PRD → Design walk drives ~18 turns.
    expect(
      revealCount,
      `expected the whole conversation to be revealed turn-by-turn (got ${revealCount} turns) — the natural per-turn scroll was skipped`,
    ).toBeGreaterThanOrEqual(15);
  } catch (err) {
    onThrow(err);
    throw err;
  } finally {
    await context.close();
    const mp4 = await saveVideoAsMp4(video, ARTIFACT_DIR, "gears-prd-design");
    // Emit the producer-agnostic chapter sidecar beside the MP4: each tour
    // step → one chapter with source_ref kind=tour.
    writeChapters(mp4, chapters.list());
    await browser.close();
  }
});
