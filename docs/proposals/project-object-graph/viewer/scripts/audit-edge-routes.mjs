import { createRequire } from "node:module";
import process from "node:process";

const requireFromSharedInstall = createRequire("/Users/brad/code/package.json");
const { chromium } = requireFromSharedInstall("playwright-core");

const url = process.env.GRAPH_MOCKUP_URL || "http://127.0.0.1:5182/";
const chromePath = process.env.CHROME_PATH || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

const browser = await chromium.launch({
  executablePath: chromePath,
  headless: true,
});

try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 920 } });
  await page.goto(url, { waitUntil: "networkidle" });
  await settle(page);

  const graphCount = await page.locator(".switcher__btn").count();
  const failures = [];
  for (let graphIndex = 0; graphIndex < graphCount; graphIndex += 1) {
    await page.locator(".switcher__btn").nth(graphIndex).click();
    await settle(page);
    const graphTitle = await page.locator(".switcher__btn").nth(graphIndex).locator("strong").innerText();

    for (const layoutName of ["LR", "TB"]) {
      await page.locator(".panel:has-text('Layout') button").filter({ hasText: layoutName }).click();
      await page.locator(".panel:has-text('Focus') button").filter({ hasText: "All" }).click();
      await settle(page);
      failures.push(...await auditRoutes(page, { graphTitle, layoutName }));
    }
  }

  if (failures.length > 0) {
    console.error(`Edge route audit failed with ${failures.length} issue(s).`);
    for (const failure of failures.slice(0, 30)) {
      console.error(`${failure.graphTitle} / ${failure.layoutName}: ${failure.route} ${failure.reason} (${failure.d})`);
    }
    if (failures.length > 30) {
      console.error(`... ${failures.length - 30} more issue(s) omitted`);
    }
    process.exitCode = 1;
  } else {
    console.log("Edge route audit passed: transit routes use level orthogonal segments with no arbitrary curves.");
  }
} finally {
  await browser.close();
}

async function auditRoutes(page, context) {
  return page.evaluate((ctx) => {
    const edges = Array.from(document.querySelectorAll(".vue-flow__edge.k-edge"));
    const failures = [];
    for (const edge of edges) {
      const path = edge.querySelector(".vue-flow__edge-path");
      if (!path) continue;
      const d = path.getAttribute("d") || "";
      const route = routeFor(edge);
      if (route === "forward" || route === "backtrack") {
        const commands = commandLetters(d);
        if (!["ML", "MLLL"].includes(commands.join(""))) {
          failures.push({ ...ctx, route, d, reason: "must use a straight or transit elbow path" });
        }
        if (!orthogonalOnly(parsePoints(d))) {
          failures.push({ ...ctx, route, d, reason: "must have level orthogonal segments" });
        }
      } else if (route === "loop") {
        if (/[CQSTA]/i.test(d)) {
          failures.push({ ...ctx, route, d, reason: "must not use curved commands" });
        }
        if (!orthogonalOnly(parsePoints(d))) {
          failures.push({ ...ctx, route, d, reason: "must be vertical-horizontal-vertical or horizontal-vertical-horizontal" });
        }
      }
    }
    return failures;

    function routeFor(edge) {
      if (edge.classList.contains("k-edge--route-backtrack")) return "backtrack";
      if (edge.classList.contains("k-edge--route-loop")) return "loop";
      return "forward";
    }

    function commandLetters(d) {
      return Array.from(d.matchAll(/[A-Za-z]/g)).map((match) => match[0].toUpperCase());
    }

    function parsePoints(d) {
      const numbers = Array.from(d.matchAll(/-?\d+(?:\.\d+)?/g)).map((match) => Number(match[0]));
      const points = [];
      for (let i = 0; i < numbers.length; i += 2) {
        points.push({ x: numbers[i], y: numbers[i + 1] });
      }
      return points;
    }

    function orthogonalOnly(points) {
      if (points.length < 2) return false;
      for (let i = 1; i < points.length; i += 1) {
        const prev = points[i - 1];
        const next = points[i];
        const vertical = Math.abs(prev.x - next.x) < 0.5;
        const horizontal = Math.abs(prev.y - next.y) < 0.5;
        if (!vertical && !horizontal) {
          return false;
        }
      }
      return true;
    }
  }, context);
}

async function settle(page) {
  await page.waitForTimeout(900);
  await page.waitForFunction(() => {
    const loading = document.querySelector(".loading");
    return !loading || loading.offsetParent === null;
  });
}
