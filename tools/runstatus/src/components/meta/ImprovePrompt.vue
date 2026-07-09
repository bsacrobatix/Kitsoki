<template>
  <section
    class="improve-prompt"
    data-testid="improve-prompt"
    aria-live="polite"
  >
    <div class="improve-prompt__copy">
      <div class="improve-prompt__title">Improve this run</div>
      <div class="improve-prompt__body">
        Review the completed session for false starts, wasted tool calls,
        prompt/tool changes, scripts, permission cleanup, and regression
        coverage.
      </div>
      <div
        v-if="statusMessage"
        class="improve-prompt__status"
        data-testid="improve-status"
      >
        {{ statusMessage }}
      </div>
      <div
        v-if="error"
        class="improve-prompt__error"
        data-testid="improve-error"
      >
        {{ error }}
      </div>
    </div>

    <div class="improve-prompt__actions">
      <button
        type="button"
        class="improve-prompt__run"
        data-testid="improve-run"
        :disabled="runDisabled"
        :title="runTitle"
        @click="runImprove()"
      >
        {{ runLabel }}
      </button>

      <label class="improve-prompt__auto">
        <input
          v-model="autoRun"
          type="checkbox"
          data-testid="improve-auto-toggle"
        />
        <span>Auto-run at completion</span>
      </label>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { LiveSource } from "../../data/live-source.js";
import { useMetaStore } from "../../stores/meta.js";

const IMPROVE_MODE = "story.improve";
const AUTO_RUN_KEY = "kitsoki:improve:autoRun";
const AUTO_RAN_PREFIX = "kitsoki:improve:autoRan:";
const IMPROVE_PROMPT =
  "Review this completed session for false starts, unexpected output, wasted tool calls, prompt/tool/script improvements, permission cleanup, and no-LLM regression coverage. Produce the standard introspection improvement report.";

const props = defineProps<{ sessionId: string }>();

const meta = useMetaStore();
const source = new LiveSource("/");
const autoRun = ref(readAutoRun());
const modesLoadedFor = ref("");
const loadingModes = ref(false);
const runState = ref<"idle" | "starting" | "running">("idle");
const statusMessage = ref("");
const error = ref("");

const improveAvailable = computed(
  () =>
    modesLoadedFor.value === props.sessionId &&
    meta.modes.some((m) => m.key === IMPROVE_MODE)
);
const improveBusy = computed(
  () => meta.statusFor(props.sessionId, IMPROVE_MODE).busy
);
const runDisabled = computed(
  () =>
    !props.sessionId ||
    loadingModes.value ||
    runState.value !== "idle" ||
    !improveAvailable.value ||
    improveBusy.value
);
const runLabel = computed(() => {
  if (runState.value === "starting") return "Opening improve...";
  if (runState.value === "running" || improveBusy.value) return "Running improve...";
  if (loadingModes.value) return "Checking improve...";
  if (!improveAvailable.value) return "Improve unavailable";
  return "Run improve now";
});
const runTitle = computed(() =>
  improveAvailable.value
    ? "Open Meta and run the completed-session improvement report"
    : "This session does not advertise story.improve"
);

onMounted(() => {
  void refreshModes().then(maybeAutoRun);
});

watch(
  () => props.sessionId,
  () => {
    statusMessage.value = "";
    error.value = "";
    void refreshModes().then(maybeAutoRun);
  }
);

watch(autoRun, (enabled) => {
  writeAutoRun(enabled);
  if (enabled) void maybeAutoRun();
});

async function refreshModes(): Promise<void> {
  if (!props.sessionId) return;
  loadingModes.value = true;
  error.value = "";
  meta.setSession(props.sessionId);
  try {
    await meta.loadModes(source, props.sessionId);
    modesLoadedFor.value = props.sessionId;
  } finally {
    loadingModes.value = false;
  }
}

async function runImprove(): Promise<void> {
  if (!props.sessionId || runState.value !== "idle" || improveBusy.value) return;
  statusMessage.value = "";
  error.value = "";
  if (!improveAvailable.value) await refreshModes();
  if (!improveAvailable.value) {
    error.value = "Improve mode is not available for this session.";
    return;
  }
  runState.value = "starting";
  try {
    await meta.openMode(source, props.sessionId, IMPROVE_MODE);
    runState.value = "running";
    await meta.send(source, IMPROVE_PROMPT);
    statusMessage.value = "Improve report is ready in Meta.";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    runState.value = "idle";
  }
}

async function maybeAutoRun(): Promise<void> {
  if (!autoRun.value || !props.sessionId || !improveAvailable.value) return;
  if (!claimAutoRun(props.sessionId)) return;
  await runImprove();
}

function autoRanKey(sessionId: string): string {
  return `${AUTO_RAN_PREFIX}${sessionId}`;
}

function claimAutoRun(sessionId: string): boolean {
  try {
    const key = autoRanKey(sessionId);
    if (localStorage.getItem(key)) return false;
    localStorage.setItem(key, new Date().toISOString());
    return true;
  } catch {
    return false;
  }
}

function readAutoRun(): boolean {
  try {
    return localStorage.getItem(AUTO_RUN_KEY) === "1";
  } catch {
    return false;
  }
}

function writeAutoRun(enabled: boolean): void {
  try {
    if (enabled) localStorage.setItem(AUTO_RUN_KEY, "1");
    else localStorage.removeItem(AUTO_RUN_KEY);
  } catch {
    // Local storage is a convenience, not required for manual improve.
  }
}
</script>

<style scoped>
.improve-prompt {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.65rem 0.9rem;
  background: #0b1d2a;
  border-top: 1px solid #164e63;
  border-bottom: 1px solid #164e63;
  color: #dbeafe;
  flex-shrink: 0;
}

.improve-prompt__copy {
  min-width: 0;
}

.improve-prompt__title {
  font-size: 0.78rem;
  font-weight: 800;
  color: #bae6fd;
  letter-spacing: 0;
  text-transform: uppercase;
}

.improve-prompt__body {
  margin-top: 0.15rem;
  color: #cbd5e1;
  font-size: 0.82rem;
  line-height: 1.35;
}

.improve-prompt__status {
  margin-top: 0.25rem;
  color: #a7f3d0;
  font-size: 0.76rem;
}

.improve-prompt__error {
  margin-top: 0.25rem;
  color: #fecaca;
  font-size: 0.76rem;
}

.improve-prompt__actions {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  flex-shrink: 0;
}

.improve-prompt__run {
  border: 1px solid #38bdf8;
  border-radius: 4px;
  background: #075985;
  color: #f0f9ff;
  font: inherit;
  font-size: 0.8rem;
  font-weight: 700;
  padding: 0.34rem 0.7rem;
  cursor: pointer;
  white-space: nowrap;
}

.improve-prompt__run:hover:not(:disabled) {
  background: #0369a1;
  border-color: #7dd3fc;
}

.improve-prompt__run:disabled {
  cursor: not-allowed;
  opacity: 0.58;
}

.improve-prompt__auto {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  color: #bfdbfe;
  font-size: 0.78rem;
  white-space: nowrap;
}

.improve-prompt__auto input {
  width: 0.95rem;
  height: 0.95rem;
  accent-color: #38bdf8;
}

@media (max-width: 720px) {
  .improve-prompt {
    align-items: stretch;
    flex-direction: column;
    gap: 0.65rem;
  }

  .improve-prompt__actions {
    justify-content: space-between;
  }
}
</style>
