<script setup lang="ts">
import { withBase } from "vitepress";
import type { ExtensionPackage } from "../../data/extensions";

const props = defineProps<{ pkg: ExtensionPackage }>();
</script>

<template>
  <section class="klib-detail">
    <p class="klib-eyebrow">{{ props.pkg.kind }} · {{ props.pkg.id }}</p>
    <h1>{{ props.pkg.title }}</h1>
    <p class="klib-lede">{{ props.pkg.summary }}</p>
    <div class="klib-stats">
      <span><strong>{{ props.pkg.stories.length }}</strong> stories</span>
      <span><strong>{{ props.pkg.provides.length }}</strong> provides</span>
      <span><strong>{{ props.pkg.requires.length }}</strong> requires</span>
      <span><strong>{{ props.pkg.publishedDocs.length }}</strong> docs</span>
    </div>
  </section>

  <section class="klib-section">
    <h2>Published docs</h2>
    <div class="klib-docs" v-if="props.pkg.publishedDocs.length">
      <div v-for="doc in props.pkg.publishedDocs" :key="doc.id" class="klib-doc">
        <span class="klib-chip">{{ doc.kind || 'doc' }}</span>
        <h3>{{ doc.title }}</h3>
        <p><code>{{ doc.path || doc.generated_from }}</code></p>
      </div>
    </div>
    <p v-else>No published docs in the current sidecar.</p>
  </section>

  <section class="klib-section" v-if="props.pkg.provides.length || props.pkg.requires.length">
    <h2>Contracts</h2>
    <div class="klib-columns">
      <div v-if="props.pkg.provides.length">
        <h3>Provides</h3>
        <ul>
          <li v-for="item in props.pkg.provides" :key="item"><code>{{ item }}</code></li>
        </ul>
      </div>
      <div v-if="props.pkg.requires.length">
        <h3>Requires</h3>
        <ul>
          <li v-for="item in props.pkg.requires" :key="item"><code>{{ item }}</code></li>
        </ul>
      </div>
    </div>
  </section>

  <section class="klib-section" v-if="props.pkg.stories.length">
    <h2>Stories</h2>
    <div class="klib-story-list">
      <a
        v-for="story in props.pkg.stories"
        :key="story.id"
        class="klib-row"
        :href="withBase(`/library/stories/${story.slug}.html`)"
      >
        <span>
          <strong>{{ story.title }}</strong>
          <small>{{ story.path }}</small>
        </span>
        <span>{{ story.states.length }} states · {{ story.intents.length }} intents · {{ story.flows.length }} flows</span>
      </a>
    </div>
  </section>
</template>
