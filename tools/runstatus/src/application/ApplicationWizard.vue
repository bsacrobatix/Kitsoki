<script setup lang="ts">
import { computed } from "vue";
import ApplicationFrameRenderer from "./ApplicationFrameRenderer.vue";
import type {
  ApplicationActionDispatcher,
  ApplicationComponentRegistrySource,
  ApplicationFrame,
} from "./types.js";

const props = withDefaults(defineProps<{
  frame: ApplicationFrame;
  dispatch: ApplicationActionDispatcher;
  components?: ApplicationComponentRegistrySource;
  stale?: boolean | string;
  eventCount?: number;
  connectionState?: "connected" | "reconnecting";
}>(), {
  components: () => ({}),
  stale: false,
  eventCount: 0,
  connectionState: "connected",
});
const emit = defineEmits<{
  (event: "navigate", page: string): void;
}>();

const progress = computed(() => {
  const pages = props.frame.pages ?? [];
  const current = pages.findIndex((page) => page.current || page.id === props.frame.page);
  return {
    current: current < 0 ? 1 : current + 1,
    total: Math.max(pages.length, 1),
  };
});
</script>

<template>
  <section class="application-wizard" data-testid="application-wizard">
    <header class="application-wizard__status">
      <span data-testid="application-progress">Step {{ progress.current }} of {{ progress.total }}</span>
      <span>State: {{ frame.workflow.state }}</span>
      <span v-if="frame.workflow.budget_state">Budget: {{ frame.workflow.budget_state }}</span>
      <span v-if="frame.workflow.degradation">Mode: {{ frame.workflow.degradation }}</span>
      <span v-if="eventCount" data-testid="application-event-count">{{ eventCount }} updates</span>
      <span v-if="connectionState === 'reconnecting'" role="status">Reconnecting…</span>
    </header>
    <ApplicationFrameRenderer
      :frame="frame"
      :dispatch="dispatch"
      :components="components"
      :stale="stale"
      @navigate="emit('navigate', $event)"
    />
  </section>
</template>

<style scoped>
.application-wizard {
  display: grid;
  gap: 0.875rem;
}
.application-wizard__status {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem 1rem;
  padding-bottom: 0.75rem;
  border-bottom: 1px solid var(--k-border, #d8dce3);
  color: var(--k-fg-muted, #596273);
  font-size: 0.875rem;
}
</style>
