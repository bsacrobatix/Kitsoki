import { test, expect } from "@playwright/test";

// End-to-end proof that a browser page can drive a REAL pty over a REAL
// websocket: real keystrokes in, real bytes out, no cassette/replay in the
// loop. The bridge spawns /bin/cat (deterministic, no LLM) per the
// playwright.config.ts webServer entry.
const BRIDGE_ADDR = process.env.TUI_BRIDGE_ADDR ?? "127.0.0.1:4700";

test("browser round-trips real keystrokes through the pty bridge", async ({ page }) => {
  await page.goto(`/player/?ws=ws://${BRIDGE_ADDR}/pty`);
  await page.waitForFunction(() => (window as any).__ready === true);
  await expect
    .poll(() => page.evaluate(() => (window as any).__status()))
    .toBe("connected");

  // Real keystrokes: xterm's paste/typing path, not a raw socket send from
  // the test — proves the page's own onData wiring works, not just the
  // bridge. xterm captures keystrokes via a hidden textarea that only
  // receives focus once the terminal element itself is clicked.
  await page.click("#term");
  await page.keyboard.type("hello-from-playwright");
  await page.keyboard.press("Enter");

  await expect
    .poll(() => page.evaluate(() => (window as any).__dump()), { timeout: 10_000 })
    .toContain("hello-from-playwright");
});

test("resize control frame reaches the pty before subsequent input", async ({ page }) => {
  await page.goto(`/player/?ws=ws://${BRIDGE_ADDR}/pty`);
  await page.waitForFunction(() => (window as any).__ready === true);
  await expect
    .poll(() => page.evaluate(() => (window as any).__status()))
    .toBe("connected");

  // /bin/cat doesn't report size, but the resize frame must not break the
  // connection or subsequent echo.
  await page.setViewportSize({ width: 900, height: 500 });
  await page.waitForTimeout(200); // let the resize listener fire and send
  await page.click("#term");
  await page.keyboard.type("still-alive-after-resize");
  await page.keyboard.press("Enter");

  await expect
    .poll(() => page.evaluate(() => (window as any).__dump()), { timeout: 10_000 })
    .toContain("still-alive-after-resize");
});
