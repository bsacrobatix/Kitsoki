<template>
  <div ref="scrollEl" class="chat-transcript" data-testid="chat-transcript">
    <div
      v-for="(entry, i) in transcript"
      :key="i"
      class="chat-row"
      :class="`chat-row--${entry.role}`"
      :data-testid="`chat-row-${entry.role}`"
    >
      <div class="chat-avatar" :class="`chat-avatar--${entry.role}`">
        {{ entry.role === "user" ? "U" : "A" }}
      </div>
      <div class="chat-bubble" :class="`chat-bubble--${entry.role}`">
        <div class="chat-role">{{ entry.role === "user" ? "You" : "Agent" }}</div>
        <div
          v-if="entry.role === 'agent' && hasElements(entry)"
          class="chat-elements"
        >
          <ViewElement
            v-for="(el, j) in entry.typedView!.Elements"
            :key="j"
            :element="el"
          />
        </div>
        <!-- Agent text is the engine's already-rendered room view: 80-col
             terminal layout (aligned key:value, numbered lists, indented
             sub-lines, hard wraps). The browser never evaluates pongo and must
             NOT re-flow that layout — doing so collapses lists into run-on
             prose. We preserve it verbatim (monospace + pre-wrap, faithful to
             the TUI) and only format inline bold/code + heading lines. -->
        <div
          v-else-if="entry.role === 'agent'"
          class="chat-view"
          v-html="renderView(entry.text)"
        ></div>
        <div v-else class="chat-text">{{ entry.text }}</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, nextTick, onMounted } from "vue";
import type { View } from "../types.js";
import ViewElement from "./ViewElement.vue";

export interface ChatEntry {
  role: "user" | "agent";
  text: string;
  typedView?: View;
}

const props = defineProps<{ transcript: ChatEntry[] }>();

const scrollEl = ref<HTMLElement | null>(null);

function hasElements(entry: ChatEntry): boolean {
  const els = entry.typedView?.Elements;
  return Array.isArray(els) && els.length > 0;
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// renderInline applies bold + inline-code to an ALREADY HTML-escaped string.
function renderInline(s: string): string {
  return s
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
}

// renderView prepares the engine's rendered room view for display. The text is
// HTML-escaped FIRST (it embeds user-supplied idea text), then each line is
// kept verbatim — newlines, indentation and alignment are preserved by the
// .chat-view CSS (white-space: pre-wrap, monospace), faithfully reproducing the
// operator's TUI view. Inline **bold** / `code` are formatted, and an ATX
// heading line (`## Title`) is rendered as a bold heading line with the markers
// stripped. We do NOT join or re-flow lines: the engine already laid the view
// out, and re-flowing collapses its lists/tables into run-on prose.
function renderView(src: string): string {
  return escapeHtml(src ?? "")
    .split("\n")
    .map((line) => {
      const h = line.match(/^(#{1,6})\s+(.*)$/);
      if (h) return `<span class="cv-h">${renderInline(h[2])}</span>`;
      return renderInline(line);
    })
    .join("\n");
}

async function scrollToBottom() {
  await nextTick();
  const el = scrollEl.value;
  if (el) el.scrollTop = el.scrollHeight;
}

onMounted(scrollToBottom);
watch(
  () => props.transcript.length,
  () => {
    void scrollToBottom();
  },
);
</script>

<style scoped>
.chat-transcript {
  display: flex;
  flex-direction: column;
  gap: 16px;
  overflow-y: auto;
  padding: 20px 24px;
  height: 100%;
  box-sizing: border-box;
  background: #0f1115;
}

.chat-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  max-width: 78%;
}

.chat-row--user {
  align-self: flex-end;
  flex-direction: row-reverse;
}

/* Agent rows carry the engine's 80-col room view, so they need most of the
   chat column width to render without re-wrapping the terminal layout. */
.chat-row--agent {
  align-self: flex-start;
  max-width: 98%;
}

.chat-avatar {
  flex: 0 0 auto;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  font-weight: 600;
  color: #fff;
  user-select: none;
}

.chat-avatar--user {
  background: #2563eb;
}

.chat-avatar--agent {
  background: #475569;
}

.chat-bubble {
  border-radius: 12px;
  padding: 10px 14px;
  font-size: 14px;
  line-height: 1.5;
  /* overflow-wrap (not word-break) so only over-long tokens break, never
     ordinary words mid-character. */
  overflow-wrap: anywhere;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
}

.chat-bubble--agent {
  /* Let the agent card grow to hold the 80-col view. */
  width: 100%;
  box-sizing: border-box;
}

.chat-bubble--user {
  background: #2563eb;
  color: #fff;
  border-bottom-right-radius: 4px;
}

/* Agent bubble is a light "paper" card on the dark chat pane: ViewElement
   renders its typed room-view elements with a light-theme palette (dark text,
   light banners, a dark code block), so the bubble must be light or the
   prose-heavy room views would render dark-on-dark and vanish. The plain-text
   fallback inherits this dark text too, so both render paths stay legible. */
.chat-bubble--agent {
  background: #f7f8fa;
  color: #1f2430;
  border: 1px solid #d8dbe2;
  border-bottom-left-radius: 4px;
}

.chat-role {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  opacity: 0.6;
  margin-bottom: 4px;
}

.chat-text {
  white-space: pre-wrap;
}

/* The agent room view: preserve the engine's layout verbatim. Monospace +
   pre-wrap keeps aligned key:value columns, numbered lists and indentation
   intact; long lines soft-wrap at the bubble edge rather than re-flowing. */
.chat-view {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas,
    "Liberation Mono", monospace;
  font-size: 12.5px;
  line-height: 1.55;
  tab-size: 2;
}
.chat-view .cv-h {
  font-weight: 700;
  color: #11151c;
}
.chat-view strong {
  font-weight: 700;
  color: #11151c;
}
.chat-view code {
  background: #eceef2;
  border-radius: 4px;
  padding: 0.05em 0.3em;
  color: #b3306b;
}

.chat-elements {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
