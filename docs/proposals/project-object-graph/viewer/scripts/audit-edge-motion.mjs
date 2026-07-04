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

  await page.locator(".switcher__btn").filter({ hasText: "Dev-story hub" }).click();
  await settle(page);
  await page.locator(".panel:has-text('Focus') button").filter({ hasText: "All" }).click();
  await settle(page);
  await page.locator(".panel:has-text('Layout') button").filter({ hasText: "LR" }).click();
  await settle(page);

  const before = await sample(page);
  await page.locator(".panel:has-text('Layout') button").filter({ hasText: "TB" }).click();
  await page.waitForTimeout(120);
  const during = await sample(page);
  await page.waitForTimeout(700);
  const after = await sample(page);

  const movingNode = largestMover(before.nodes, after.nodes);
  const nodeMove = movement(before.nodes[movingNode], during.nodes[movingNode]);
  const edgeMove = averageSharedMovement(before.edges, during.edges);
  const labelMove = averageSharedMovement(before.labels, during.labels);
  const totalNodeMove = movement(before.nodes[movingNode], after.nodes[movingNode]);
  const totalEdgeMove = averageSharedMovement(before.edges, after.edges);
  const totalLabelMove = averageSharedMovement(before.labels, after.labels);

  const failures = [];
  if (totalNodeMove < 30 || totalEdgeMove < 30 || totalLabelMove < 30) {
    failures.push(`probe did not exercise enough movement: node=${totalNodeMove.toFixed(1)}, edge=${totalEdgeMove.toFixed(1)}, label=${totalLabelMove.toFixed(1)}`);
  }
  if (nodeMove > 10 && edgeMove < 6) {
    failures.push(`edge endpoint stayed nearly stationary while node moved: node=${nodeMove.toFixed(1)}, edge=${edgeMove.toFixed(1)}`);
  }
  if (nodeMove > 10 && labelMove < 6) {
      failures.push(`label stayed nearly stationary while node moved: node=${nodeMove.toFixed(1)}, label=${labelMove.toFixed(1)}`);
  }

  if (failures.length > 0) {
    for (const failure of failures) {
      console.error(failure);
    }
    process.exitCode = 1;
  } else {
    console.log(
      `Edge motion audit passed: node=${nodeMove.toFixed(1)}px, edge=${edgeMove.toFixed(1)}px, label=${labelMove.toFixed(1)}px during sampled transition.`,
    );
  }
} finally {
  await browser.close();
}

async function sample(page) {
  return page.evaluate(() => {
    return {
      nodes: Object.fromEntries(Array.from(document.querySelectorAll(".vue-flow__node.k-node")).map((node, index) => {
        const key = node.querySelector(".graph-node__label")?.textContent?.trim() || `node-${index}`;
        return [key, center(node.getBoundingClientRect())];
      })),
      edges: Object.fromEntries(Array.from(document.querySelectorAll(".vue-flow__edge-path")).map((edge, index) => [
        `edge-${index}`,
        center(edge.getBoundingClientRect()),
      ])),
      labels: Object.fromEntries(Array.from(document.querySelectorAll(".k-edge-label")).map((label, index) => [
        label.getAttribute("data-edge-label-id") || `label-${index}`,
        center(label.getBoundingClientRect()),
      ])),
    };

    function center(rect) {
      return {
        x: rect.left + rect.width / 2,
        y: rect.top + rect.height / 2,
      };
    }
  });
}

async function settle(page) {
  await page.waitForTimeout(900);
  await page.waitForFunction(() => {
    const loading = document.querySelector(".loading");
    return !loading || loading.offsetParent === null;
  });
}

function movement(a, b) {
  if (!a || !b) {
    return 0;
  }
  return Math.hypot(a.x - b.x, a.y - b.y);
}

function largestMover(before, after) {
  let bestKey = Object.keys(before)[0];
  let bestMove = -1;
  for (const key of Object.keys(before)) {
    if (!after[key]) {
      continue;
    }
    const move = movement(before[key], after[key]);
    if (move > bestMove) {
      bestKey = key;
      bestMove = move;
    }
  }
  return bestKey;
}

function averageSharedMovement(before, after) {
  let sum = 0;
  let count = 0;
  for (const key of Object.keys(before)) {
    if (!after[key]) {
      continue;
    }
    sum += movement(before[key], after[key]);
    count += 1;
  }
  return count ? sum / count : 0;
}
