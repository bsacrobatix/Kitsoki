<script setup lang="ts">
import { computed } from "vue";
import { useData, withBase } from "vitepress";
import type { ExtensionLibrary as ExtensionLibraryData } from "../../data/extensions";

const { theme } = useData();
const library = computed(() => theme.value.extensions as ExtensionLibraryData);
</script>

<template>
  <section class="klib-hero" aria-labelledby="extension-library-title">
    <p class="klib-eyebrow">Build-time extension index</p>
    <h1 id="extension-library-title">Extension library</h1>
    <p>
      Browse Kitsoki packages and stories from the source-owned docs index. The site is generated from
      <code>docs.yaml</code> sidecars and story manifests during the build, so this catalog tracks the repo instead of a hand-maintained page.
    </p>
    <div class="klib-stats" aria-label="Library summary">
      <span><strong>{{ library.stats.packages }}</strong> packages</span>
      <span><strong>{{ library.stats.stories }}</strong> stories</span>
      <span><strong>{{ library.stats.publishedDocs }}</strong> docs</span>
      <span><strong>{{ library.stats.hostInterfaces }}</strong> host interfaces</span>
    </div>
  </section>

  <section v-if="library.packages.length" class="klib-section" aria-labelledby="packages-title">
    <h2 id="packages-title">Packages</h2>
    <div class="klib-grid">
      <a
        v-for="pkg in library.packages"
        :key="pkg.id"
        class="klib-card"
        :href="withBase(`/library/packages/${pkg.slug}.html`)"
      >
        <span class="klib-chip">{{ pkg.kind }}</span>
        <h3>{{ pkg.title }}</h3>
        <p>{{ pkg.summary || pkg.id }}</p>
        <dl class="klib-meta">
          <div><dt>Stories</dt><dd>{{ pkg.stories.length }}</dd></div>
          <div><dt>Provides</dt><dd>{{ pkg.provides.length }}</dd></div>
          <div><dt>Docs</dt><dd>{{ pkg.publishedDocs.length }}</dd></div>
        </dl>
      </a>
    </div>
  </section>

  <section class="klib-section" aria-labelledby="stories-title">
    <h2 id="stories-title">Stories</h2>
    <div class="klib-story-list">
      <a
        v-for="story in library.stories.slice(0, 24)"
        :key="story.id"
        class="klib-row"
        :href="withBase(`/library/stories/${story.slug}.html`)"
      >
        <span>
          <strong>{{ story.title }}</strong>
          <small>{{ story.package_id || story.id }}</small>
        </span>
        <span>{{ story.states.length }} states · {{ story.intents.length }} intents · {{ story.flows.length }} flows</span>
      </a>
    </div>
  </section>
</template>
