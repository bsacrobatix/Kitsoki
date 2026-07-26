import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import vue from "@vitejs/plugin-vue";

interface ApplicationPlan {
  readonly application_id: string;
  readonly story_root: string;
  readonly workspace: string;
  readonly out_dir: string;
  readonly backend_url: string;
  readonly vite_port: number;
  readonly compatibility_out: string;
}

function loadPlan(): ApplicationPlan {
  const planPath = process.env.KITSOKI_APPLICATION_PLAN;
  if (!planPath) {
    throw new Error("KITSOKI_APPLICATION_PLAN is required");
  }
  return JSON.parse(fs.readFileSync(planPath, "utf8")) as ApplicationPlan;
}

function compatibilityPlugin(plan: ApplicationPlan): Plugin {
  const storyRoot = path.resolve(plan.story_root);
  return {
    name: "kitsoki-application-compatibility",
    configureServer(server) {
      server.watcher.add([
        path.join(storyRoot, "**/*.yaml"),
        path.join(storyRoot, "**/*.yml"),
        path.join(storyRoot, "**/*.json"),
        path.join(storyRoot, "**/*.star"),
      ]);
    },
    handleHotUpdate(context) {
      const extension = path.extname(context.file).toLowerCase();
      if (![".yaml", ".yml", ".json", ".star"].includes(extension)) {
        return;
      }
      context.server.ws.send({
        type: "custom",
        event: "kitsoki:application-compatibility",
        data: {
          status: "checking",
          changed_path: path.relative(storyRoot, context.file).split(path.sep).join("/"),
        },
      });
      return [];
    },
  };
}

export default defineConfig(() => {
  const plan = loadPlan();
  const toolRoot = path.dirname(fileURLToPath(import.meta.url));
  return {
    root: plan.workspace,
    base: "./",
    cacheDir: path.join(plan.workspace, "vite-cache"),
    plugins: [vue(), compatibilityPlugin(plan)],
    resolve: {
      alias: {
        vue: path.join(toolRoot, "node_modules", "vue", "dist", "vue.esm-bundler.js"),
      },
    },
    server: {
      host: "127.0.0.1",
      port: plan.vite_port,
      strictPort: true,
      fs: {
        allow: [plan.workspace, plan.story_root, toolRoot],
      },
      proxy: {
        "/rpc": {
          target: plan.backend_url,
          changeOrigin: true,
          timeout: 0,
          proxyTimeout: 0,
        },
        "/artifact": {
          target: plan.backend_url,
          changeOrigin: true,
          timeout: 0,
          proxyTimeout: 0,
        },
      },
    },
    build: {
      target: "es2020",
      outDir: plan.out_dir,
      emptyOutDir: true,
      manifest: true,
      rollupOptions: {
        input: path.join(plan.workspace, "index.html"),
      },
    },
  };
});
