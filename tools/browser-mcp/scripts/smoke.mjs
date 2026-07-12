#!/usr/bin/env node
// No-LLM smoke test for the deterministic primitives: spawns the real
// server.mjs over stdio (a real headless Stagehand/Playwright browser
// launches, but observe/act are never called, so no LLM call happens),
// drives it through the MCP wire protocol against the local fixture page,
// and asserts the primitive tool surface actually works end to end.
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const serverPath = path.join(here, "..", "server.mjs");
const fixturePath = path.join(here, "..", "test", "fixtures", "fixture.html");
const fixtureUrl = `file://${fixturePath}`;

function startClient() {
  const child = spawn(process.execPath, [serverPath], {
    cwd: path.join(here, ".."),
    env: { ...process.env, KITSOKI_BROWSER_MCP_HEADLESS: "1" },
    stdio: ["pipe", "pipe", "inherit"]
  });

  let buffer = "";
  const pending = new Map();
  let nextId = 1;

  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    buffer += chunk;
    for (;;) {
      const idx = buffer.indexOf("\n");
      if (idx === -1) break;
      const line = buffer.slice(0, idx).replace(/\r$/, "");
      buffer = buffer.slice(idx + 1);
      if (!line.trim()) continue;
      let message;
      try {
        message = JSON.parse(line);
      } catch {
        continue;
      }
      if (message.id !== undefined && pending.has(message.id)) {
        pending.get(message.id).resolve(message);
        pending.delete(message.id);
      }
    }
  });

  function send(method, params) {
    const id = nextId++;
    const message = { jsonrpc: "2.0", id, method, params };
    child.stdin.write(`${JSON.stringify(message)}\n`);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new Error(`timed out waiting for response to ${method}`));
      }, 30000);
      pending.set(id, {
        resolve: (msg) => {
          clearTimeout(timer);
          resolve(msg);
        }
      });
    });
  }

  function notify(method, params) {
    child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", method, params })}\n`);
  }

  async function callTool(name, args = {}) {
    const response = await send("tools/call", { name, arguments: args });
    if (response.error) throw new Error(`${name}: ${JSON.stringify(response.error)}`);
    const result = response.result;
    const text = result?.content?.[0]?.text;
    let parsed = text;
    try {
      parsed = JSON.parse(text);
    } catch {
      // leave as text
    }
    if (result?.isError) throw new Error(`${name} returned isError: ${text}`);
    return parsed;
  }

  return { child, send, notify, callTool };
}

function assertOk(cond, message) {
  if (!cond) throw new Error(`SMOKE FAIL: ${message}`);
  console.log(`ok - ${message}`);
}

async function main() {
  const client = startClient();

  const init = await client.send("initialize", {
    protocolVersion: "2025-03-26",
    capabilities: {},
    clientInfo: { name: "browser-mcp-smoke", version: "0.0.0" }
  });
  assertOk(init.result?.serverInfo?.name === "kitsoki-browser-mcp", "initialize returns server info");
  client.notify("notifications/initialized", {});

  const list = await client.send("tools/list", {});
  const names = (list.result?.tools || []).map((t) => t.name);
  for (const expected of [
    "browser_navigate",
    "browser_click",
    "browser_fill",
    "browser_scroll",
    "browser_press",
    "browser_snapshot",
    "browser_find",
    "browser_batch",
    "observe",
    "act"
  ]) {
    assertOk(names.includes(expected), `tools/list includes ${expected}`);
  }

  const nav = await client.callTool("browser_navigate", { url: fixtureUrl });
  assertOk(nav.url === fixtureUrl, "browser_navigate opens the fixture page (no LLM call)");

  const snap = await client.callTool("browser_snapshot", {});
  assertOk(Array.isArray(snap.refs) && snap.refs.length > 0, "browser_snapshot captures interactive elements");
  assertOk(snap.refs.some((r) => r.testid === "save-btn"), "browser_snapshot finds the save button by testid");

  const found = await client.callTool("browser_find", { query: "save" });
  assertOk(found.refs.length === 1 && found.refs[0].testid === "save-btn", "browser_find narrows to the matching subset");

  const clicked = await client.callTool("browser_click", { testid: "save-btn" });
  assertOk(clicked.clicked === true && clicked.strategy === "testid", "browser_click resolves by testid and clicks");

  const filled = await client.callTool("browser_fill", { testid: "name-input", value: "kitsoki" });
  assertOk(filled.filled === true, "browser_fill resolves and fills");

  const scrolled = await client.callTool("browser_scroll", { testid: "panel-btn" });
  assertOk(scrolled.scrolled === true && scrolled.mode === "into-view", "browser_scroll scrolls a resolved target into view");

  const badAnchor = await client.callTool("browser_click", { testid: "does-not-exist" }).catch((err) => err);
  assertOk(badAnchor instanceof Error, "browser_click on a missing anchor fails loudly, not silently");

  const batch = await client.callTool("browser_batch", {
    ops: [
      { tool: "browser_navigate", args: { url: fixtureUrl } },
      { tool: "browser_click", args: { testid: "save-btn" } }
    ]
  });
  assertOk(batch.results.length === 2 && batch.results.every((r) => r.ok), "browser_batch runs multiple primitive ops in one call");

  await client.callTool("browser_close", {});
  client.child.kill();
  console.log("\nbrowser-mcp smoke: ALL PRIMITIVES PASSED (no LLM calls made)");
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
