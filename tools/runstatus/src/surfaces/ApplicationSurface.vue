<template>
  <div
    class="application-surface"
    data-testid="surface-application"
    :style="applicationThemeStyle(installedApplicationTheme())"
    @click.capture="selectFeedbackTarget"
    @focusin.capture="selectFeedbackTarget"
  >
    <div v-if="loading" class="application-surface__state">Loading application…</div>
    <div v-else-if="!sessionId" class="application-surface__state">No active application session.</div>
    <div v-else-if="error" class="application-surface__error" role="alert">{{ error }}</div>
    <ApplicationWizard
      v-else-if="frame"
      :frame="frame"
      :dispatch="dispatch"
      :components="installedApplicationComponents()"
      :stale="compatibilityReason"
      :event-count="eventCount"
      :connection-state="connectionState"
      @navigate="navigate"
    />
    <button
      v-if="frame && feedbackTarget"
      class="application-surface__feedback"
      type="button"
      data-application-feedback-control
      data-testid="application-feedback-submit"
      :aria-label="`Report feedback about ${feedbackTarget.label}`"
      :title="`Report feedback about ${feedbackTarget.label}`"
      @click.stop="reportFeedback"
    >!</button>
    <span class="application-surface__feedback-status" role="status" aria-live="polite">
      {{ feedbackStatus }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import {
  ApplicationWizard,
  applicationThemeStyle,
  dispatchWebApplicationAction,
  ensureApplicationSession,
  inspectApplicationFeedbackTarget,
  installedApplicationComponents,
  installedApplicationTheme,
  submitApplicationFeedback,
  type ApplicationActionDispatcher,
  type ApplicationFeedbackClient,
  type ApplicationFrame,
  type ApplicationSemanticInspection,
} from "../application/index.js";
import {
  createDataSource,
  type ConnectionState,
  type DataSource,
} from "../data/source.js";
import { JsonRpcClient } from "../transport/jsonrpc.js";

interface ApplicationCompatibility {
  readonly schema: "application-compatibility/v1";
  readonly definition_digest: string;
  readonly status: "compatible" | "refresh-compatible" | "reload-required" | "invalid";
  readonly message: string;
}

const rpc = new JsonRpcClient();
const feedbackRPC: ApplicationFeedbackClient = {
  post: (method, params) => rpc.post<unknown>(method, params),
};
const props = withDefaults(defineProps<{ applicationId?: string }>(), {
  applicationId: "",
});
const sessionId = ref<string | null>(null);
const frame = ref<ApplicationFrame | null>(null);
const currentPage = ref("");
const loading = ref(true);
const error = ref("");
const compatibilityReason = ref("");
const eventCount = ref(0);
const connectionState = ref<ConnectionState>("connected");
const feedbackTarget = ref<ApplicationSemanticInspection["semantic_element"] | null>(null);
const feedbackStatus = ref("");
let feedbackSelectionSequence = 0;

let source: DataSource | null = null;
let unsubscribeCurrent: (() => void) | null = null;
let unsubscribeEvents: (() => void) | null = null;
let refreshQueued = false;
let compatibilityTimer: ReturnType<typeof setInterval> | null = null;
let refreshedDefinition = "";
let compatibilityRequest: Promise<void> | null = null;

async function loadFrame(id: string, page = currentPage.value): Promise<void> {
  try {
    const nextFrame = await rpc.post<ApplicationFrame>("runstatus.application.frame", {
      session_id: id,
      ...(page ? { page } : {}),
    });
    frame.value = nextFrame;
    currentPage.value = nextFrame.page;
    feedbackTarget.value = null;
    feedbackStatus.value = "";
    error.value = "";
  } catch (cause) {
    frame.value = null;
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    loading.value = false;
  }
}

async function selectFeedbackTarget(event: Event): Promise<void> {
  const currentFrame = frame.value as ApplicationFrame | null;
  if (!currentFrame) return;
  const element = event.target as HTMLElement | null;
  if (element?.closest("[data-application-feedback-control]")) return;
  const sequence = ++feedbackSelectionSequence;
  try {
    const selected = await inspectApplicationFeedbackTarget(feedbackRPC, currentFrame, event.target);
    if (sequence === feedbackSelectionSequence && selected) {
      feedbackTarget.value = selected.semantic_element;
      feedbackStatus.value = "";
    }
  } catch (cause) {
    if (sequence === feedbackSelectionSequence) {
      feedbackStatus.value = cause instanceof Error ? cause.message : String(cause);
    }
  }
}

async function reportFeedback(): Promise<void> {
  const currentFrame = frame.value;
  const target = feedbackTarget.value;
  if (!currentFrame || !target) return;
  const instruction = window.prompt(`Feedback about ${target.label}`);
  if (!instruction?.trim()) return;
  try {
    const submitted = await submitApplicationFeedback(
      feedbackRPC,
      currentFrame,
      target.ref,
      instruction.trim(),
    );
    feedbackStatus.value = `Feedback ${submitted.receipt.ref} filed`;
  } catch (cause) {
    feedbackStatus.value = cause instanceof Error ? cause.message : String(cause);
  }
}

function followSession(id: string | null): void {
  unsubscribeEvents?.();
  unsubscribeEvents = null;
  sessionId.value = id;
  frame.value = null;
  currentPage.value = "";
  error.value = "";
  compatibilityReason.value = "";
  eventCount.value = 0;
  connectionState.value = "connected";
  loading.value = Boolean(id);
  if (!id) return;

  void loadFrame(id);
  unsubscribeEvents = source?.subscribe(id, () => {
    eventCount.value += 1;
    if (refreshQueued) return;
    refreshQueued = true;
    queueMicrotask(() => {
      refreshQueued = false;
      if (sessionId.value === id) void loadFrame(id);
    });
  }, (state) => {
    connectionState.value = state;
  }) ?? null;
}

const dispatch: ApplicationActionDispatcher = async (envelope) => {
  try {
    const outcome = await dispatchWebApplicationAction(rpc, envelope, currentPage.value);
    if (outcome.frame) {
      frame.value = outcome.frame;
      currentPage.value = outcome.frame.page;
    }
    return { ...outcome, ok: !outcome.error };
  } catch (cause) {
    return { ok: false, error: cause instanceof Error ? cause.message : String(cause) };
  }
};

function navigate(page: string): void {
  if (!sessionId.value || page === currentPage.value) return;
  currentPage.value = page;
  loading.value = true;
  void loadFrame(sessionId.value, page);
}

async function checkCompatibility(): Promise<void> {
  if (compatibilityRequest) return compatibilityRequest;
  compatibilityRequest = (async () => {
    try {
      const response = await fetch("./compatibility.json", { cache: "no-store" });
      if (!response.ok) return;
      await applyCompatibility(await response.json() as ApplicationCompatibility);
    } catch {
      // The embedded production host has no compatibility sidecar.
    } finally {
      compatibilityRequest = null;
    }
  })();
  return compatibilityRequest;
}

async function applyCompatibility(next: ApplicationCompatibility): Promise<void> {
  if (next.schema !== "application-compatibility/v1") return;
  if (next.status === "reload-required" || next.status === "invalid") {
    compatibilityReason.value = next.message;
    return;
  }
  compatibilityReason.value = "";
  if (
    next.status !== "refresh-compatible" ||
    !sessionId.value ||
    next.definition_digest === refreshedDefinition
  ) return;

  refreshedDefinition = next.definition_digest;
  try {
    await rpc.post("runstatus.session.reload", { session_id: sessionId.value });
    await loadFrame(sessionId.value);
  } catch (cause) {
    compatibilityReason.value = cause instanceof Error ? cause.message : String(cause);
  }
}

onMounted(async () => {
  source = createDataSource();
  try {
    const current = await source.getCurrentSession();
    followSession(await ensureApplicationSession(rpc, props.applicationId, current));
    unsubscribeCurrent = source.subscribeCurrentSession(followSession);
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
    loading.value = false;
  }
  if (import.meta.env.DEV) {
    compatibilityTimer = setInterval(() => void checkCompatibility(), 500);
    import.meta.hot?.on("kitsoki:application-compatibility", () => {
      setTimeout(() => void checkCompatibility(), 50);
    });
  }
});

onUnmounted(() => {
  unsubscribeCurrent?.();
  unsubscribeEvents?.();
  if (compatibilityTimer) clearInterval(compatibilityTimer);
});
</script>

<style scoped>
.application-surface {
  min-height: 100vh;
  padding: 1rem;
  background: var(--k-bg, #f4f6f8);
  color: var(--k-fg, #1d2433);
}
.application-surface__state,
.application-surface__error {
  display: grid;
  min-height: 12rem;
  place-items: center;
  color: var(--k-fg-muted, #596273);
}
.application-surface__error {
  color: var(--k-danger, #b42318);
}
.application-surface__feedback {
  position: fixed;
  right: 1rem;
  bottom: 1rem;
  width: 2rem;
  height: 2rem;
  border: 1px solid var(--k-border, #c8ced8);
  border-radius: 4px;
  background: var(--k-surface, #ffffff);
  color: var(--k-fg, #1d2433);
  font-weight: 700;
  cursor: pointer;
}
.application-surface__feedback-status {
  position: fixed;
  overflow: hidden;
  width: 1px;
  height: 1px;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}
</style>
