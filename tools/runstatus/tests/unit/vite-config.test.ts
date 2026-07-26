import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import applicationConfig from "../../application.vite.config.js";
import config from "../../vite.config.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const runstatusRoot = path.resolve(__dirname, "../..");

describe("vite config", () => {
  it("roots the SPA at tools/runstatus regardless of caller cwd", () => {
    const resolved = typeof config === "function" ? config({ command: "build", mode: "test" }) : config;

    expect(resolved.root).toBe(runstatusRoot);
    expect(resolved.server?.fs?.allow).toContain(runstatusRoot);
  });

  it("resolves generated application entries against the local Vue toolchain", () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), "kitsoki-application-vite-"));
    const workspace = path.join(root, "workspace");
    fs.mkdirSync(workspace);
    const planPath = path.join(workspace, "plan.json");
    fs.writeFileSync(planPath, JSON.stringify({
      application_id: "test",
      story_root: root,
      workspace,
      out_dir: path.join(workspace, "dist"),
      backend_url: "http://127.0.0.1:7777",
      vite_port: 5173,
      compatibility_out: path.join(workspace, "compatibility.json"),
    }));
    const previous = process.env.KITSOKI_APPLICATION_PLAN;
    process.env.KITSOKI_APPLICATION_PLAN = planPath;
    try {
      const resolved = typeof applicationConfig === "function"
        ? applicationConfig({ command: "build", mode: "test" })
        : applicationConfig;
      const vue = resolved.resolve?.alias && !Array.isArray(resolved.resolve.alias)
        ? resolved.resolve.alias.vue
        : undefined;
      expect(resolved.root).toBe(workspace);
      expect(vue).toBe(path.join(runstatusRoot, "node_modules", "vue", "dist", "vue.esm-bundler.js"));
    } finally {
      if (previous === undefined) delete process.env.KITSOKI_APPLICATION_PLAN;
      else process.env.KITSOKI_APPLICATION_PLAN = previous;
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
});
