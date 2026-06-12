/**
 * vscode-tour.e2e.spec.ts — THE deterministic, no-LLM end-to-end gate for the
 * Kitsoki VS Code extension, and the spine the demo-video recorder later reuses.
 *
 * "One spec, two modes" (mirrors WEB_CHAT_PACE in tools/runstatus):
 *   KITSOKI_VSCODE_PACE=0  (default) → fast/assert: every critical-path beat is
 *                          a hard assertion, no dwells, no recordVideo. This is
 *                          the CI / de-risk gate. Same input → same result.
 *   KITSOKI_VSCODE_PACE≥1  → paced: the SAME asserted beats plus per-beat dwells
 *                          and recordVideo, so the recorder only ADDS pacing on
 *                          top of the EXACT path this gate proves.
 *
 * Determinism contract (no flake allowed — a race is a bug, not a sleep):
 *   - No LLM. The backend runs `kitsoki web --flow stories/weather-report/
 *     flows/tour.yaml`, whose starlark_http_cassette replays all HTTP. No model,
 *     no socket.
 *   - Fixed VS Code 1.96.4, throwaway user-data + extensions dirs, fixed window
 *     size, VSCODE_* stripped (all in _helpers/launch.ts).
 *   - The backend port is auto-allocated by the extension (net :0), unique per
 *     run, so parallel runs never collide.
 *   - Readiness is asserted, never slept: backend health-poll (Backend.start),
 *     webview-guest descent (webviewFrame probes for a real element), and
 *     toHaveText/toBeVisible expectations with timeouts.
 *
 * Critical path asserted beat-by-beat (the scenarios kitsoki-ui-qa checks the
 * video against):
 *   (a) the Kitsoki Activity Bar view opens;
 *   (b) the SPA renders INSIDE the webview (home story-card visible — proves
 *       bundle + CSP + relay + backend round-trip end to end);
 *   (c) a session is started/observed (New session → /chat, current-state=lobby);
 *   (d) a turn is driven and state advances (forecast → current-state=report,
 *       state-badge present; plus the intent-btn-* control path on the report
 *       room);
 *   (e) the trace surfaces render for the driven session (trace-diagram +
 *       trace-timeline with a host.starlark.run row).
 *
 * Run (one-liner gate):  pnpm e2e
 *   ≡  KITSOKI_VSCODE_PACE=0 playwright test vscode-tour.e2e
 * Record (paced):        KITSOKI_VSCODE_PACE=1 pnpm playwright vscode-tour.e2e
 * Make targets:          make vscode-e2e-fast   /   make vscode-e2e
 *
 * Requires a built extension + embedded SPA: `make build && (cd tools/
 * vscode-kitsoki && pnpm build)`. packageExtension() asserts both are present.
 */
import { test, expect, type FrameLocator } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as os from 'node:os';
import {
  launchVSCode,
  packageExtension,
  webviewFrame,
  type LaunchedVSCode,
} from './_helpers/launch';

const EXT_ROOT = path.resolve(__dirname, '..');
const REPO_ROOT = path.resolve(EXT_ROOT, '..', '..');
const STORY_DIR = path.join(REPO_ROOT, 'stories', 'weather-report');
const FLOW = path.join(STORY_DIR, 'flows', 'tour.yaml');

const PACE = Number.parseInt(process.env.KITSOKI_VSCODE_PACE ?? '0', 10) || 0;
const RECORD = PACE >= 1;
const ARTIFACT_DIR = path.join(REPO_ROOT, '.artifacts', 'vscode-e2e');

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
/** Paced dwell — a no-op in assert mode so the gate stays instant + deterministic. */
const dwell = (ms: number) => (RECORD ? sleep(ms * PACE) : Promise.resolve());

test('vscode tour e2e — load, render, drive, trace (no-LLM, deterministic)', async () => {
  test.setTimeout(240_000);

  // Sanity: the no-LLM fixtures must exist (fail loudly, not as a blank webview).
  for (const p of [FLOW, STORY_DIR, path.join(STORY_DIR, 'cassettes', 'tour.http.yaml')]) {
    if (!fs.existsSync(p)) throw new Error(`missing no-LLM fixture: ${p}`);
  }

  const tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'vscode-kitsoki-e2e-'));
  const workspace = path.join(tmpRoot, 'workspace');
  fs.mkdirSync(path.join(workspace, '.vscode'), { recursive: true });

  // Inject extension settings: point Backend at the weather-report no-LLM flow.
  // `kitsoki` is taken from PATH (binaryPath empty). The backend port is
  // auto-allocated by the extension, so no port appears here.
  fs.writeFileSync(
    path.join(workspace, '.vscode', 'settings.json'),
    JSON.stringify(
      {
        'kitsoki.flow': FLOW,
        'kitsoki.storiesDir': STORY_DIR,
        'kitsoki.binaryPath': fs.existsSync(path.join(REPO_ROOT, 'bin', 'kitsoki'))
          ? path.join(REPO_ROOT, 'bin', 'kitsoki')
          : '',
      },
      null,
      2,
    ),
  );

  // The backend spawns with cwd = workspace folder, but the flow references the
  // story via absolute --stories-dir/--flow, so cwd is irrelevant for resolution.
  const extensionsDir = packageExtension(EXT_ROOT, path.join(tmpRoot, 'extensions'));

  // Tee the extension host's OutputChannel (backend spawn/health, relay errors)
  // to a file the spec can read — these diagnostics are otherwise trapped in the
  // in-editor Output panel.
  fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
  const hostLog = path.join(ARTIFACT_DIR, 'extension-host.log');
  fs.writeFileSync(hostLog, '');
  process.env.KITSOKI_E2E_LOG = hostLog;

  let shotIdx = 0;
  let launched: LaunchedVSCode | undefined;

  try {
    launched = await launchVSCode({
      workspace,
      extensionsDir,
      userDataDir: path.join(tmpRoot, 'user-data'),
      size: { width: 1400, height: 900 },
      ...(RECORD ? { videoDir: path.join(ARTIFACT_DIR, 'video') } : {}),
    });
    const { win } = launched;

    // Surface webview-guest errors (CSP violations, transport bootstrap
    // failures) — otherwise a blank webview is undiagnosable. Errors only, to
    // keep a passing run's output clean.
    win.on('console', (m) => {
      if (m.type() === 'error') console.log(`[webview console.error] ${m.text()}`);
    });
    win.on('pageerror', (e) => console.log(`[webview pageerror] ${e.message}`));

    const shot = async (label: string) => {
      const n = String(++shotIdx).padStart(2, '0');
      const p = path.join(ARTIFACT_DIR, `${n}-${label}.png`);
      fs.mkdirSync(ARTIFACT_DIR, { recursive: true });
      await win.screenshot({ path: p }).catch(() => undefined);
      return p;
    };

    // ── (a) Open the Kitsoki Activity Bar view ───────────────────────────────
    // The extension contributes a `kitsoki` viewsContainer to the Activity Bar.
    // Clicking its icon reveals the chat WebviewView (the reliable, cross-
    // platform open — the command-palette path is brittle by comparison).
    await win.waitForSelector('.monaco-workbench', { timeout: 60_000 });
    const kitsokiIcon = win.locator('.activitybar [aria-label*="Kitsoki" i]').first();
    await expect(kitsokiIcon, 'Kitsoki Activity Bar item present').toBeVisible({ timeout: 30_000 });
    await kitsokiIcon.click();
    // The chat WebviewView resolves now → Backend.start() spawns kitsoki web and
    // health-polls before the SPA html is set. Allow generous time for the
    // first-run binary spawn + health poll.
    await dwell(1500);
    await shot('a-view-open');

    // ── (b) The SPA renders INSIDE the webview ───────────────────────────────
    // Descend into the webview guest and assert the home story library rendered.
    // A visible story-card proves: extension bundle loaded → CSP allowed the
    // inlined SPA → BridgeTransport relayed runstatus.stories.* over postMessage
    // → host fetched the backend → backend answered. Full round-trip in one beat.
    const chatFrame: FrameLocator = await webviewFrame(
      win,
      { selector: '[data-testid="home-view"]' },
      45_000,
    );
    // Confirm the webview transport is the bridge (acquireVsCodeApi present),
    // proving the SPA mounted under the host and not as a plain browser tab.
    await expect
      .poll(
        () =>
          chatFrame
            .locator('body')
            .evaluate(
              () => typeof (window as unknown as { acquireVsCodeApi?: unknown }).acquireVsCodeApi === 'function',
            )
            .catch(() => false),
        { timeout: 15_000, message: 'webview exposes acquireVsCodeApi (BridgeTransport active)' },
      )
      .toBe(true);
    await expect(
      chatFrame.locator('[data-testid="story-card"]').first(),
      'home story-card visible inside webview (bundle+CSP+relay+backend round-trip)',
    ).toBeVisible({ timeout: 30_000 });
    await dwell(1500);
    await shot('b-spa-rendered');

    // ── (c) Start / observe a session ────────────────────────────────────────
    // Click "New session" on the weather-report card → the SPA routes to /chat.
    const weatherCard = chatFrame
      .locator('[data-testid="story-card"]')
      .filter({ hasText: /weather/i })
      .first();
    await expect(weatherCard, 'weather-report story card present').toBeVisible({ timeout: 15_000 });
    await weatherCard.locator('[data-testid="new-session-btn"]').click();
    // Session header + current-state appear once routed to the interactive view.
    await expect(
      chatFrame.locator('[data-testid="current-state"]'),
      'interactive view shows current-state after New session',
    ).toBeVisible({ timeout: 30_000 });
    await expect(
      chatFrame.locator('[data-testid="current-state"]'),
      'fresh session opens in the lobby room',
    ).toHaveText('lobby', { timeout: 30_000 });
    // Observe link proves the session is addressable/observable.
    await expect(
      chatFrame.locator('[data-testid="observe-link"]'),
      'session is observable',
    ).toBeVisible({ timeout: 10_000 });
    await dwell(1500);
    await shot('c-session-started');

    // ── (d) Drive a turn → state advances ────────────────────────────────────
    // Submit the lobby forecast intent (a `choice:` param form). The flow's
    // cassette replays geocode + forecast, so this is a real turn with no LLM.
    const forecastForm = chatFrame.locator('form[data-intent="forecast"]');
    await expect(forecastForm, 'forecast intent form present in lobby').toBeVisible({
      timeout: 15_000,
    });
    await forecastForm.locator('input').fill('Tokyo');
    await dwell(700);
    await forecastForm.locator('button[type="submit"]').click();
    // Hard assertion the state-badge / current-state ADVANCED lobby → report.
    await expect(
      chatFrame.locator('[data-testid="current-state"]'),
      'driven turn advances current-state lobby → report',
    ).toHaveText('report', { timeout: 30_000 });
    await expect(
      chatFrame.locator('[data-testid="state-badge"]'),
      'state-badge present after the driven turn',
    ).toBeVisible({ timeout: 10_000 });
    // The rendered forecast for the geocoded place proves the cassette replay ran.
    await expect(
      chatFrame.locator('[data-testid="chat-transcript"]').getByText('Tokyo, Japan'),
      'forecast report rendered (cassette replay, no LLM)',
    ).toBeVisible({ timeout: 15_000 });
    await dwell(1500);
    await shot('d-turn-driven');

    // intent-btn-* control path: the report room exposes a `back` action button.
    // Asserting it both confirms the intent-btn-<name> selector the video uses
    // and exercises a second state transition (report → lobby).
    const backBtn = chatFrame.locator('[data-testid="intent-btn-back"]').first();
    await expect(backBtn, 'intent-btn-back control present in report room').toBeVisible({
      timeout: 15_000,
    });

    // ── (e) Trace surfaces render for the driven session ─────────────────────
    // The interactive view embeds the SAME trace surfaces the panel webview
    // shows: the state diagram and the trace timeline. After a real turn the
    // timeline carries a host.starlark.run row.
    await expect(
      chatFrame.locator('[data-testid="trace-diagram"]'),
      'trace state diagram renders for the driven session',
    ).toBeVisible({ timeout: 15_000 });
    const timeline = chatFrame.locator('[data-testid="trace-timeline"]');
    await expect(timeline, 'trace timeline renders for the driven session').toBeVisible({
      timeout: 15_000,
    });
    await expect(
      timeline.locator('.trace-timeline__row:has([data-subsystem="host"])').first(),
      'trace timeline shows a host.starlark.run row from the driven turn',
    ).toBeVisible({ timeout: 20_000 });
    // The current station on the diagram reflects the advanced state.
    await expect(
      chatFrame.locator('[data-testid="diagram-current-station"]').first(),
      'state diagram marks the current station',
    ).toBeVisible({ timeout: 15_000 });
    await dwell(2000);
    await shot('e-trace-rendered');
  } finally {
    if (launched) await launched.app.close().catch(() => undefined);
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  }
});
