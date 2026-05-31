<template>
  <div class="oracle-detail">
    <!-- Common header -->
    <div class="oracle-detail__header">
      <span class="oracle-detail__verb-badge" :class="verbBadgeClass">{{ verb }}</span>
      <span v-if="agent" class="oracle-detail__meta">{{ agent }}</span>
      <span v-if="model" class="oracle-detail__meta oracle-detail__meta--model">{{ model }}</span>
      <span class="oracle-detail__spacer" />
      <span v-if="durationMs !== undefined" class="oracle-detail__stat">{{ fmtMs(durationMs) }}</span>
      <span v-if="promptTokens !== undefined" class="oracle-detail__stat oracle-detail__stat--tokens">
        in:{{ fmtTokens(promptTokens) }}
      </span>
      <span v-if="responseTokens !== undefined" class="oracle-detail__stat oracle-detail__stat--tokens">
        out:{{ fmtTokens(responseTokens) }}
      </span>
      <span
        v-if="cacheReadTokens"
        class="oracle-detail__stat oracle-detail__stat--tokens"
        title="Prompt tokens served from the cache (cache_read_input_tokens)"
      >
        cache:{{ fmtTokens(cacheReadTokens) }}
      </span>
      <span v-if="costStr" class="oracle-detail__stat oracle-detail__stat--cost">{{ costStr }}</span>
    </div>

    <!-- Error banner -->
    <div v-if="errorMsg" class="oracle-detail__error">{{ errorMsg }}</div>

    <!-- Token usage breakdown: per-type counts straight from the trace's
         meta.usage object, plus the total cost the CLI reported. Per-type cost
         is not recorded (the CLI returns a single total_cost_usd), so only the
         total is shown — the UI never fabricates a per-type split. -->
    <table v-if="usageRows.length" class="oracle-detail__usage">
      <thead>
        <tr>
          <th class="oracle-detail__usage-th">Token usage</th>
          <th class="oracle-detail__usage-th oracle-detail__usage-th--num">tokens</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in usageRows" :key="r.label" :title="r.hint">
          <td class="oracle-detail__usage-label">{{ r.label }}</td>
          <td class="oracle-detail__usage-num">{{ fmtTokens(r.tokens) }}</td>
        </tr>
      </tbody>
      <tfoot>
        <tr class="oracle-detail__usage-total">
          <td class="oracle-detail__usage-label">Total tokens</td>
          <td class="oracle-detail__usage-num">{{ fmtTokens(totalTokens) }}</td>
        </tr>
        <tr v-if="costStr" class="oracle-detail__usage-cost">
          <td class="oracle-detail__usage-label">Total cost</td>
          <td class="oracle-detail__usage-num">{{ costStr }}</td>
        </tr>
      </tfoot>
    </table>

    <!-- Per-verb body -->
    <DecideDetail  v-if="verb === 'decide'"  :event="event" />
    <ExtractDetail v-else-if="verb === 'extract'" :event="event" />
    <AskDetail     v-else-if="verb === 'ask'"     :event="event" />
    <TaskDetail    v-else-if="verb === 'task'"    :event="event" />
    <ConverseDetail v-else-if="verb === 'converse'" :event="event" />

    <!-- Fallback: raw attrs dump for unknown verbs -->
    <div v-else class="oracle-detail__fallback">
      <pre class="oracle-detail__pre">{{ prettyJson(event.attrs) }}</pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { TraceEvent } from "../../types.js";
import { fmtMs, fmtTokens, fmtCost, prettyJson, readOracleUsage } from "./lib.js";
import DecideDetail from "./DecideDetail.vue";
import ExtractDetail from "./ExtractDetail.vue";
import AskDetail from "./AskDetail.vue";
import TaskDetail from "./TaskDetail.vue";
import ConverseDetail from "./ConverseDetail.vue";

const props = defineProps<{ event: TraceEvent }>();

const attrs = computed(() => props.event.attrs);

const verb = computed(() => {
  // Canonical: the verb lives in attrs.verb (engine emits oracle.call.*).
  // Fall back to inferring from a legacy per-verb msg ("oracle.<verb>.complete")
  // but never treat the canonical "call" token as a verb.
  const fromAttrs = typeof attrs.value.verb === "string" ? attrs.value.verb : "";
  if (fromAttrs) return fromAttrs;
  const m = props.event.msg.match(/^oracle\.([a-z]+)\.complete$/);
  return m && m[1] !== "call" ? m[1]! : "";
});
const agent    = computed(() => String(attrs.value.agent ?? ""));
const model    = computed(() => String(attrs.value.model ?? ""));
const durationMs     = computed(() => attrs.value.duration_ms as number | undefined);
// Token usage + cost come from the canonical opaque transport meta
// (attrs.meta.usage / attrs.meta.cost_usd), with a fallback to the legacy flat
// fields so synthetic fixtures still render. See readOracleUsage.
const usage          = computed(() => readOracleUsage(attrs.value));
const promptTokens   = computed(() => usage.value.promptTokens);
const responseTokens = computed(() => usage.value.responseTokens);
const cacheReadTokens = computed(() => usage.value.cacheReadTokens);
const costStr  = computed(() => fmtCost(usage.value.costUsd));

// Per-type token rows for the expanded breakdown table. claude reports the
// input categories disjointly: `input_tokens` is fresh (uncached) input, while
// cache read / cache write are billed separately — so they sum to the full
// input side. Only rows the trace actually carries are shown.
const usageRows = computed(() => {
  const u = usage.value;
  const rows: { label: string; tokens: number; hint: string }[] = [];
  if (u.promptTokens !== undefined)
    rows.push({ label: "Input (uncached)", tokens: u.promptTokens, hint: "input_tokens — fresh prompt tokens billed at full rate" });
  if (u.cacheReadTokens)
    rows.push({ label: "Cache read", tokens: u.cacheReadTokens, hint: "cache_read_input_tokens — prompt tokens served from the cache" });
  if (u.cacheCreationTokens)
    rows.push({ label: "Cache write", tokens: u.cacheCreationTokens, hint: "cache_creation_input_tokens — prompt tokens written to the cache" });
  if (u.responseTokens !== undefined)
    rows.push({ label: "Output", tokens: u.responseTokens, hint: "output_tokens — tokens generated in the response" });
  return rows;
});
const totalTokens = computed(() => usageRows.value.reduce((sum, r) => sum + r.tokens, 0));
const errorMsg = computed(() => typeof attrs.value.error === "string" ? attrs.value.error : null);

const verbBadgeClass = computed(() => {
  switch (verb.value) {
    case "decide":  return "verb--decide";
    case "extract": return "verb--extract";
    case "ask":     return "verb--ask";
    case "task":    return "verb--task";
    case "converse": return "verb--converse";
    default:        return "verb--other";
  }
});
</script>

<style scoped>
.oracle-detail {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.oracle-detail__header {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  flex-wrap: wrap;
  padding-bottom: 0.35rem;
  border-bottom: 1px solid #1e293b;
}

.oracle-detail__verb-badge {
  padding: 0.1rem 0.5rem;
  border-radius: 3px;
  font-size: 0.75rem;
  font-weight: 700;
  font-family: ui-monospace, monospace;
  text-transform: uppercase;
}

/* Verb badge colours */
.verb--decide  { background: #1e1b4b; color: #a5b4fc; border: 1px solid #3730a3; }
.verb--extract { background: #042f2e; color: #5eead4; border: 1px solid #0d9488; }
.verb--ask     { background: #431407; color: #fdba74; border: 1px solid #c2410c; }
.verb--task    { background: #450a0a; color: #fca5a5; border: 1px solid #991b1b; }
.verb--converse { background: #083344; color: #67e8f9; border: 1px solid #0891b2; }
.verb--other   { background: #1e293b; color: #94a3b8; border: 1px solid #334155; }

.oracle-detail__usage {
  border-collapse: collapse;
  font-family: ui-monospace, monospace;
  font-size: 0.72rem;
  align-self: flex-start;
  min-width: 16rem;
  background: #080f1a;
  border: 1px solid #1e293b;
  border-radius: 4px;
  overflow: hidden;
}

.oracle-detail__usage-th {
  text-align: left;
  color: #64748b;
  font-weight: 600;
  text-transform: uppercase;
  font-size: 0.65rem;
  letter-spacing: 0.03em;
  padding: 0.3rem 0.6rem;
  border-bottom: 1px solid #1e293b;
}

.oracle-detail__usage-th--num {
  text-align: right;
}

.oracle-detail__usage-label {
  color: #94a3b8;
  padding: 0.18rem 0.6rem;
}

.oracle-detail__usage-num {
  color: #cbd5e1;
  text-align: right;
  padding: 0.18rem 0.6rem;
  font-variant-numeric: tabular-nums;
}

.oracle-detail__usage-total td {
  border-top: 1px solid #1e293b;
  color: #e2e8f0;
  font-weight: 600;
  padding-top: 0.28rem;
}

.oracle-detail__usage-cost td {
  color: #a3e635;
  font-weight: 600;
}

.oracle-detail__meta {
  color: #94a3b8;
  font-size: 0.75rem;
  font-family: ui-monospace, monospace;
}

.oracle-detail__meta--model {
  color: #64748b;
  font-size: 0.7rem;
}

.oracle-detail__spacer {
  flex: 1;
}

.oracle-detail__stat {
  color: #64748b;
  font-size: 0.7rem;
  font-family: ui-monospace, monospace;
  white-space: nowrap;
}

.oracle-detail__stat--tokens {
  color: #475569;
}

.oracle-detail__stat--cost {
  color: #a3e635;
}

.oracle-detail__error {
  background: #2d0707;
  border: 1px solid #991b1b;
  border-radius: 4px;
  color: #fca5a5;
  padding: 0.3rem 0.5rem;
  font-size: 0.75rem;
  font-family: ui-monospace, monospace;
}

.oracle-detail__fallback {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}

.oracle-detail__pre {
  background: #080f1a;
  border: 1px solid #1e293b;
  border-radius: 4px;
  padding: 0.4rem 0.6rem;
  font-family: ui-monospace, monospace;
  font-size: 0.72rem;
  color: #7dd3fc;
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0;
}
</style>
