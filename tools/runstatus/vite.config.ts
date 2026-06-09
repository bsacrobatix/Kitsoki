import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { viteSingleFile } from "vite-plugin-singlefile";

// In dev mode, proxy /rpc and /rpc/events to the kitsoki Go backend so the
// Vite HMR dev server can serve the Vue app while the real JSON-RPC surface
// runs in the Go process. Set KITSOKI_API to override the default address.
const apiBase = process.env.KITSOKI_API ?? "http://127.0.0.1:7777";

export default defineConfig({
  plugins: [vue(), viteSingleFile()],
  server: {
    middlewareMode: false,
    fs: {
      allow: ['fixtures', '.'],
    },
    host: "127.0.0.1",
    port: parseInt(process.env.VITE_PORT ?? "5173"),
    proxy: {
      "/rpc": { target: apiBase, changeOrigin: true },
    },
  },
  build: {
    target: "es2020",
    outDir: "dist",
    emptyOutDir: true,
  },
});
