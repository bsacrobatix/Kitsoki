<script setup lang="ts">
import { withBase } from "vitepress";
import type { ExtensionStory } from "../../data/extensions";

const props = defineProps<{ story: ExtensionStory }>();
</script>

<template>
  <section class="klib-detail">
    <p class="klib-eyebrow">Story · {{ props.story.id }}</p>
    <h1>{{ props.story.title }}</h1>
    <p class="klib-lede"><code>{{ props.story.path }}</code></p>
    <p v-if="props.story.package_id && props.story.packageSlug">
      Package:
      <a :href="withBase(`/library/packages/${props.story.packageSlug}.html`)"><code>{{ props.story.package_id }}</code></a>
    </p>
    <div class="klib-stats">
      <span><strong>{{ props.story.states.length }}</strong> states</span>
      <span><strong>{{ props.story.intents.length }}</strong> intents</span>
      <span><strong>{{ props.story.world_keys.length }}</strong> world keys</span>
      <span><strong>{{ props.story.flows.length }}</strong> flows</span>
    </div>
  </section>

  <section class="klib-section">
    <h2>Runtime surface</h2>
    <div class="klib-columns">
      <div>
        <h3>Host interfaces</h3>
        <ul v-if="props.story.host_interfaces.length">
          <li v-for="item in props.story.host_interfaces" :key="item"><code>{{ item }}</code></li>
        </ul>
        <p v-else>None declared.</p>
      </div>
      <div>
        <h3>Agents</h3>
        <ul v-if="props.story.agents.length || props.story.agent_plugins.length">
          <li v-for="item in [...props.story.agents, ...props.story.agent_plugins]" :key="item"><code>{{ item }}</code></li>
        </ul>
        <p v-else>None declared.</p>
      </div>
    </div>
  </section>

  <section class="klib-section">
    <h2>State machine</h2>
    <div class="klib-columns">
      <div>
        <h3>States</h3>
        <ul class="klib-compact">
          <li v-for="item in props.story.states" :key="item"><code>{{ item }}</code></li>
        </ul>
      </div>
      <div>
        <h3>Intents</h3>
        <ul class="klib-compact">
          <li v-for="item in props.story.intents" :key="item"><code>{{ item }}</code></li>
        </ul>
      </div>
    </div>
  </section>

  <section class="klib-section" v-if="props.story.exports.length || props.story.exits.length || props.story.flows.length">
    <h2>Integration and tests</h2>
    <div class="klib-columns">
      <div v-if="props.story.exports.length">
        <h3>Exports</h3>
        <ul><li v-for="item in props.story.exports" :key="item"><code>{{ item }}</code></li></ul>
      </div>
      <div v-if="props.story.exits.length">
        <h3>Exits</h3>
        <ul><li v-for="item in props.story.exits" :key="item"><code>{{ item }}</code></li></ul>
      </div>
      <div v-if="props.story.flows.length">
        <h3>Flows</h3>
        <ul><li v-for="item in props.story.flows" :key="item"><code>{{ item }}</code></li></ul>
      </div>
    </div>
  </section>
</template>
