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
import { loadDecks } from "./data/decks.js";
import type { LocaleCode } from "./data/i18n.js";
import { locales, prefixed } from "./data/i18n.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const variant = process.env.SITE_VARIANT === "embedded" ? "embedded" : "full";
const base = process.env.SITE_BASE ?? "/";
const publicUrl = process.env.SITE_PUBLIC_URL ?? "https://bsacrobatix.github.io/Kitsoki";

function localizedThemeConfig(locale: LocaleCode) {
  const info = locales[locale];
  return {
    logo: {
      light: "/branding/mesa-sun.svg",
      dark: "/branding/mesa-sun-light.svg",
      alt: "kitsoki mesa sun",
    },
    nav: [
      { text: "Evaluate", link: "/guide/evaluate-kitsoki.html" },
      { text: "Proof", link: "/proof.html" },
      { text: "Decks", link: "/decks/index.html" },
      { text: "Get Started", link: "/guide/getting-started.html" },
      { text: info.text.nav.guide, link: "/guide/" },
      { text: "Download", link: "/download.html" },
    ],
    sidebar: {
      [prefixed(locale, "/features/")]: featuresSidebar(locale),
      ...(locale === "en" ? { "/guide/": guideSidebar() } : {}),
    },
    socialLinks: [{ icon: "github", link: "https://github.com/bsacrobatix/Kitsoki" }],
    search: { provider: "local" },
    outline: { level: [2, 3] },
    siteVariant: variant,
    sitePublicUrl: publicUrl,
    siteLocale: locale,
    siteText: info.text,
    features: loadFeatures(locale).map((f) => ({
      id: f.id,
      kind: f.kind,
      title: f.title,
      tagline: f.tagline,
      promo: f.promo,
      media: f.media,
    })),
    decks: loadDecks(),
  };
}

export default defineConfig({
  title: "kitsoki",
  description:
    "A conversational workflow engine: deterministic YAML state machines with the LLM confined to narrow, traceable decision points.",
  base,
  head: [
    ["link", { rel: "icon", type: "image/svg+xml", href: `${base}branding/mesa-sun-simple.svg` }],
  ],
  srcDir: "./src",
  outDir: path.resolve(__dirname, variant === "embedded" ? "dist-embedded" : "dist"),
  cleanUrls: false,
  lang: locales.en.lang,
  vite: {
    optimizeDeps: {
      entries: [
        ".vitepress/**/*.{ts,js,vue}",
        "src/**/*.{md,ts,js,vue}",
        "!.vitepress/cache/**",
        "!.vitepress/dist/**",
        "!.vitepress/dist-embedded/**",
        "!.vitepress/gen/**",
        "!src/public/**",
      ],
    },
  },

  // th/ja are registered in the shared i18n data (locales, below) and their
  // page trees + overlays are kept on disk, but the VitePress locale entries
  // themselves are commented out: only 1 of 27 feature cards has translation
  // overlays today, so publishing the switcher would mostly serve English
  // content inside a th/ja shell. srcExclude drops the now-unlinked src/th//
  // src/ja page trees from this build so they don't ship as orphaned,
  // locale-config-less pages. Re-enable both blocks (and drop srcExclude) once
  // `stories/product-site-localization/` has produced enough overlays to be
  // worth publishing.
  srcExclude: ["th/**", "ja/**"],
  locales: {
    root: {
      label: locales.en.label,
      lang: locales.en.lang,
      title: locales.en.title,
      description: locales.en.description,
    },
    // th: {
    //   label: locales.th.label,
    //   lang: locales.th.lang,
    //   title: locales.th.title,
    //   description: locales.th.description,
    //   themeConfig: localizedThemeConfig("th"),
    // },
    // ja: {
    //   label: locales.ja.label,
    //   lang: locales.ja.lang,
    //   title: locales.ja.title,
    //   description: locales.ja.description,
    //   themeConfig: localizedThemeConfig("ja"),
    // },
  },

  markdown: {
    // kitsoki prompt templates use ```pongo fences (Pongo2 = Django/Twig-style).
    languageAlias: { pongo: "twig" },
    config(md) {
      const origFence =
        md.renderer.rules.fence ??
        ((tokens, idx, opts, _env, self) => self.renderToken(tokens, idx, opts));
      md.renderer.rules.fence = (tokens, idx, opts, env, self) => {
        if (env?.relativePath?.startsWith("guide/")) {
          const token = tokens[idx];
          const lang = token.info.trim().split(/\s+/)[0] || "text";
          return `<pre v-pre class="language-${md.utils.escapeHtml(lang)}"><code>${md.utils.escapeHtml(token.content)}</code></pre>`;
        }
        return origFence(tokens, idx, opts, env, self);
      };

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

  themeConfig: localizedThemeConfig("en"),
});
