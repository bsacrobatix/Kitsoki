/**
 * VitePress config for the kitsoki promo site + help docs.
 *
 * One source tree, two build variants:
 *   full     (default)        — GitHub Pages: SITE_BASE=/Kitsoki/, full videos
 *   embedded (SITE_VARIANT)   — go:embed'd into the binary at /help/: posters
 *                               only, video pages link out to SITE_PUBLIC_URL
 *
 * The base is BAKED into a VitePress build, so the two variants are two
 * builds (out dirs .vitepress/dist and .vitepress/dist-embedded). cleanUrls
 * stays false: the Go static file server has no extensionless→.html fallback.
 */
import * as path from "path";
import { fileURLToPath } from "url";
import { defineConfig } from "vitepress";
import { loadFeatures, featuresSidebar, guideSidebar } from "./data/features.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const variant = process.env.SITE_VARIANT === "embedded" ? "embedded" : "full";
const base = process.env.SITE_BASE ?? "/";
const publicUrl = process.env.SITE_PUBLIC_URL ?? "https://bsacrobatix.github.io/Kitsoki";

const features = loadFeatures();

export default defineConfig({
  title: "kitsoki",
  description:
    "A conversational workflow engine: deterministic YAML state machines with the LLM confined to narrow, traceable decision points.",
  base,
  srcDir: "./src",
  outDir: path.resolve(__dirname, variant === "embedded" ? "dist-embedded" : "dist"),
  cleanUrls: false,

  markdown: {
    // kitsoki prompt templates use ```pongo fences (Pongo2 = Django/Twig-style).
    languageAlias: { pongo: "twig" },
    config(md) {
      // kitsoki docs are full of Pongo2 `{{ ... }}` / `{% ... %}` markers in
      // INLINE code spans; VitePress only v-pre's fenced blocks by default, so
      // Vue would parse those as interpolations and fail the build. Force
      // v-pre onto inline code across the whole site.
      const orig =
        md.renderer.rules.code_inline ??
        ((tokens, idx, opts, _env, self) => self.renderToken(tokens, idx, opts));
      md.renderer.rules.code_inline = (tokens, idx, opts, env, self) =>
        orig(tokens, idx, opts, env, self).replace(/^<code/, "<code v-pre");

      // ... and PROSE in staged guide docs can carry mustaches too (e.g.
      // story-style's `(\${{ world.threat_level * 50 }})` bullet). Escape the
      // opening braces in text tokens — for /guide/ pages ONLY, so our own
      // pages keep real `{{ $params.x }}` interpolation.
      const origText =
        md.renderer.rules.text ?? ((tokens, idx) => tokens[idx].content);
      md.renderer.rules.text = (tokens, idx, opts, env, self) => {
        const out = origText(tokens, idx, opts, env, self);
        return env?.relativePath?.startsWith("guide/")
          ? out.replace(/\{\{/g, "&#123;&#123;")
          : out;
      };
    },
  },

  themeConfig: {
    nav: [
      { text: "Features", link: "/features/" },
      { text: "Guide", link: "/guide/getting-started" },
    ],
    sidebar: {
      "/guide/": guideSidebar(),
      "/features/": featuresSidebar(),
    },
    socialLinks: [{ icon: "github", link: "https://github.com/bsacrobatix/Kitsoki" }],
    search: { provider: "local" },
    outline: { level: [2, 3] },

    // Site-specific (read via useData().theme by the custom components):
    // the lightweight feature list for grids/hero, the build variant, and the
    // public site URL the embedded variant links out to for videos.
    siteVariant: variant,
    sitePublicUrl: publicUrl,
    features: features.map((f) => ({
      id: f.id,
      kind: f.kind,
      title: f.title,
      tagline: f.tagline,
      promo: f.promo,
      media: f.media,
    })),
  },
});
