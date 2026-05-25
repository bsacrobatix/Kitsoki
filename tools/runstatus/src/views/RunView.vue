<template>
  <div>Loading…</div>
</template>

<script setup lang="ts">
// RunView stub — a parallel agent implements the three-panel layout.
// Props: sessionId (string) — injected by router.
// Will: call useRunStore().hydrate(source, sessionId) on mount,
//       render StateDiagram, TraceTimeline, DetailDrawer.
defineProps<{ sessionId: string }>();
</script>
