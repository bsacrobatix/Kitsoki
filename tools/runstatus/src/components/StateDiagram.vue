<template>
  <div class="state-diagram" ref="containerRef">
    <div v-if="!mermaidSource" class="state-diagram__empty">No diagram available.</div>
    <div v-else ref="svgHostRef" class="state-diagram__svg-host" />
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, nextTick } from "vue";
import mermaid from "mermaid";
import type { NodeRef } from "../types.js";

// ---- props & emits ----------------------------------------------------------

const props = defineProps<{
  mermaidSource: string;
  nodeMap: Record<string, NodeRef>;
  currentStatePath: string;
}>();

const emit = defineEmits<{
  (e: "select", nodeId: string, nodeRef: NodeRef): void;
}>();

// ---- refs -------------------------------------------------------------------

const containerRef = ref<HTMLElement | null>(null);
const svgHostRef = ref<HTMLElement | null>(null);

// Unique per-component counter for stable Mermaid IDs.
let _renderCounter = 0;

// Cleanup handlers attached to the SVG click targets.
let _cleanupClickHandlers: (() => void) | null = null;

// ---- init mermaid (once) ---------------------------------------------------

let _mermaidInitialised = false;

function ensureMermaidInit(): void {
  if (_mermaidInitialised) return;
  _mermaidInitialised = true;
  mermaid.initialize({ startOnLoad: false, theme: "dark", securityLevel: "loose" });
}

// ---- render -----------------------------------------------------------------

async function renderDiagram(): Promise<void> {
  if (!svgHostRef.value || !props.mermaidSource) return;

  ensureMermaidInit();

  _renderCounter += 1;
  const id = `kitsoki-mermaid-${_renderCounter}`;

  let svgHtml: string;
  try {
    const result = await mermaid.render(id, props.mermaidSource, svgHostRef.value);
    svgHtml = result.svg;
  } catch (err) {
    console.error("[StateDiagram] mermaid.render failed:", err);
    if (svgHostRef.value) {
      svgHostRef.value.innerHTML = `<pre class="state-diagram__error">Diagram render error</pre>`;
    }
    return;
  }

  if (!svgHostRef.value) return; // component unmounted while awaiting

  // Inject the SVG.
  svgHostRef.value.innerHTML = svgHtml;

  // Attach click handlers and current-state class.
  attachNodeHandlers();
}

// ---- node interaction -------------------------------------------------------

function attachNodeHandlers(): void {
  // Remove any prior click handlers.
  _cleanupClickHandlers?.();
  const cleanups: Array<() => void> = [];

  const host = svgHostRef.value;
  if (!host) return;

  // Mermaid 10/11 renders nodes as <g> elements with an id matching the
  // flowchart node ID (e.g. id="flowchart-ST_root_active-42" or
  // "ST_root_active"). We query all <g> elements with an id and match against
  // our nodeMap keys.
  const allGs = host.querySelectorAll<SVGGElement>("g[id]");

  allGs.forEach((g) => {
    const rawId = g.getAttribute("id") ?? "";
    // Mermaid may prefix/suffix with "flowchart-" and a digit suffix.
    const nodeId = extractMermaidNodeId(rawId);
    if (!nodeId) return;

    const nodeRef = props.nodeMap[nodeId];
    if (!nodeRef) return;

    // Apply current-state class.
    if (nodeRef.kind === "state" && nodeRef.ref === props.currentStatePath) {
      g.classList.add("current");
    } else {
      g.classList.remove("current");
    }

    // Click handler — only for nodes in the map.
    const handler = (ev: Event) => {
      ev.stopPropagation();
      emit("select", nodeId, nodeRef);
    };
    g.style.cursor = "pointer";
    g.addEventListener("click", handler);
    cleanups.push(() => g.removeEventListener("click", handler));
  });

  _cleanupClickHandlers = () => {
    cleanups.forEach((fn) => fn());
    _cleanupClickHandlers = null;
  };
}

/**
 * Extract our stable node ID from the mermaid-assigned SVG id.
 *
 * Mermaid 11 assigns SVG element ids in the form:
 *   "<containerId>-flowchart-<nodeId>-<number>"
 *   e.g. "kitsoki-mermaid-1-flowchart-ST_root_active-3"
 * Mermaid 10 omitted the container prefix:
 *   "flowchart-<nodeId>-<number>"
 *
 * We strip everything up to and including "flowchart-" and the trailing
 * "-<number>" suffix.
 */
function extractMermaidNodeId(svgId: string): string {
  let s = svgId.replace(/^.*?flowchart-/, "");
  s = s.replace(/-\d+$/, "");
  return s;
}

// ---- update current class without full re-render ---------------------------

function refreshCurrentClass(): void {
  const host = svgHostRef.value;
  if (!host) return;

  const allGs = host.querySelectorAll<SVGGElement>("g[id]");
  allGs.forEach((g) => {
    const rawId = g.getAttribute("id") ?? "";
    const nodeId = extractMermaidNodeId(rawId);
    if (!nodeId) return;
    const nodeRef = props.nodeMap[nodeId];
    if (!nodeRef) return;
    if (nodeRef.kind === "state" && nodeRef.ref === props.currentStatePath) {
      g.classList.add("current");
    } else {
      g.classList.remove("current");
    }
  });
}

// ---- lifecycle --------------------------------------------------------------

onMounted(async () => {
  await nextTick();
  await renderDiagram();
});

watch(
  () => props.mermaidSource,
  async () => {
    await nextTick();
    await renderDiagram();
  }
);

watch(
  () => props.currentStatePath,
  () => {
    refreshCurrentClass();
  }
);

onBeforeUnmount(() => {
  _cleanupClickHandlers?.();
});
</script>

<style scoped>
.state-diagram {
  width: 100%;
  overflow: auto;
  min-height: 120px;
  background: #0f172a;
  border: 1px solid #1e293b;
  border-radius: 6px;
  padding: 0.5rem;
}

.state-diagram__empty,
.state-diagram__error {
  color: #64748b;
  font-size: 0.875rem;
  padding: 1rem;
}

.state-diagram__error {
  color: #f87171;
}

.state-diagram__svg-host :deep(svg) {
  max-width: 100%;
  height: auto;
}

/* Highlight the currently active state node */
.state-diagram__svg-host :deep(g.current > rect),
.state-diagram__svg-host :deep(g.current > polygon),
.state-diagram__svg-host :deep(g.current > circle),
.state-diagram__svg-host :deep(g.current .label-container),
.state-diagram__svg-host :deep(g.current .node-label) {
  stroke: #60a5fa !important;
  stroke-width: 3px !important;
  filter: drop-shadow(0 0 4px rgba(96, 165, 250, 0.6));
}

.state-diagram__svg-host :deep(g.current > rect),
.state-diagram__svg-host :deep(g.current > polygon) {
  fill: rgba(30, 64, 175, 0.35) !important;
}

.state-diagram__svg-host :deep(g[style*="cursor: pointer"]:hover > rect),
.state-diagram__svg-host :deep(g[style*="cursor: pointer"]:hover > polygon) {
  opacity: 0.8;
}
</style>
