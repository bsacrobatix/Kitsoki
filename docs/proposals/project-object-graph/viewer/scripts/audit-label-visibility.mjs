import { createRequire } from "node:module";
import path from "node:path";
import process from "node:process";

const requireFromSharedInstall = createRequire("/Users/brad/code/package.json");
const { chromium } = requireFromSharedInstall("playwright-core");

const url = process.env.GRAPH_MOCKUP_URL || "http://127.0.0.1:5182/";
const chromePath = process.env.CHROME_PATH || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const screenshotPath = process.env.LABEL_AUDIT_SCREENSHOT || path.resolve("label-audit-failure.png");

const browser = await chromium.launch({
  executablePath: chromePath,
  headless: true,
});

const failures = [];
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 920 } });
  await page.goto(url, { waitUntil: "networkidle" });
  await settle(page);

  const graphCount = await page.locator(".switcher__btn").count();
  for (let graphIndex = 0; graphIndex < graphCount; graphIndex += 1) {
    await page.locator(".switcher__btn").nth(graphIndex).click();
    await settle(page);
    const graphTitle = await page.locator(".switcher__btn").nth(graphIndex).locator("strong").innerText();

    for (const radiusName of ["1 edge", "2 edges", "3 edges", "All"]) {
      await clickButtonByText(page, ".panel:has-text('Focus') button", radiusName);
      await settle(page);

      for (const layoutName of ["LR", "TB"]) {
        await clickButtonByText(page, ".panel:has-text('Layout') button", layoutName);
        await settle(page);
        failures.push(...await auditLabels(page, { graphTitle, radiusName, layoutName }));
      }
    }
  }

  if (failures.length > 0) {
    await page.screenshot({ path: screenshotPath, fullPage: true });
    console.error(`Label visibility audit failed with ${failures.length} overlap(s). Screenshot: ${screenshotPath}`);
    for (const failure of failures.slice(0, 30)) {
      console.error(`${failure.graphTitle} / ${failure.radiusName} / ${failure.layoutName}: ${failure.label} overlaps ${failure.nodeLabel} by ${failure.area}px2`);
    }
    if (failures.length > 30) {
      console.error(`... ${failures.length - 30} more overlap(s) omitted`);
    }
    process.exitCode = 1;
  } else {
    console.log("Label visibility audit passed: no edge labels intersect visible graph nodes.");
  }
} finally {
  await browser.close();
}

async function clickButtonByText(page, selector, text) {
  const button = page.locator(selector).filter({ hasText: text }).first();
  await button.click();
}

async function settle(page) {
  await page.waitForTimeout(850);
  await page.waitForFunction(() => {
    const loading = document.querySelector(".loading");
    return !loading || loading.offsetParent === null;
  });
}

async function auditLabels(page, context) {
  return page.evaluate((ctx) => {
    const labels = Array.from(document.querySelectorAll(".k-edge-label"));
    const nodes = Array.from(document.querySelectorAll(".vue-flow__node.k-node"));
    const visibleLabels = labels
      .map((label) => ({ label, rect: label.getBoundingClientRect() }))
      .filter((item) => item.rect.width > 0 && item.rect.height > 0);
    const visibleNodes = nodes
      .map((node) => ({
        node,
        rect: node.getBoundingClientRect(),
        label: node.querySelector(".graph-node__label")?.textContent?.trim() || node.getAttribute("data-id") || "node",
      }))
      .filter((item) => item.rect.width > 0 && item.rect.height > 0);
    const overlaps = [];

    for (const labelItem of visibleLabels) {
      const labelText = labelItem.label.textContent.trim();
      for (const nodeItem of visibleNodes) {
        const area = overlapArea(labelItem.rect, insetRect(nodeItem.rect, -2));
        if (area > 0.5) {
          overlaps.push({
            ...ctx,
            label: labelText,
            nodeLabel: nodeItem.label,
            area: Math.round(area),
          });
        }
      }
    }
    return overlaps;

    function insetRect(rect, amount) {
      return {
        left: rect.left + amount,
        top: rect.top + amount,
        right: rect.right - amount,
        bottom: rect.bottom - amount,
      };
    }

    function overlapArea(a, b) {
      const width = Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left));
      const height = Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top));
      return width * height;
    }
  }, context);
}
