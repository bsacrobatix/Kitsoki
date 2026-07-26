<template>
  <div class="application-surface" data-testid="surface-application">
    <div v-if="loading" class="application-surface__state">Loading application…</div>
    <div v-else-if="!sessionId" class="application-surface__state">No active application session.</div>
    <div v-else-if="error" class="application-surface__error" role="alert">{{ error }}</div>
    <ApplicationFrameRenderer
      v-else-if="frame"
      :frame="frame"
      :dispatch="dispatch"
      @navigate="navigate"
    />
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import {
  ApplicationFrameRenderer,
  type ApplicationActionDispatcher,
  type ApplicationFrame,
} from "../application/index.js";
import { createDataSource, type DataSource } from "../data/source.js";
import { JsonRpcClient } from "../transport/jsonrpc.js";

interface ApplicationOutcome {
  frame?: ApplicationFrame;
}

const rpc = new JsonRpcClient();
const sessionId = ref<string | null>(null);
const frame = ref<ApplicationFrame | null>(null);
const currentPage = ref("");
const loading = ref(true);
const error = ref("");

let source: DataSource | null = null;
let unsubscribeCurrent: (() => void) | null = null;
let unsubscribeEvents: (() => void) | null = null;
let refreshQueued = false;

async function loadFrame(id: string, page = currentPage.value): Promise<void> {
  try {
    const nextFrame = await rpc.post<ApplicationFrame>("runstatus.application.frame", {
      session_id: id,
      ...(page ? { page } : {}),
    });
    frame.value = nextFrame;
    currentPage.value = nextFrame.page;
    error.value = "";
  } catch (cause) {
    frame.value = null;
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    loading.value = false;
  }
}

function followSession(id: string | null): void {
  unsubscribeEvents?.();
  unsubscribeEvents = null;
  sessionId.value = id;
  frame.value = null;
  currentPage.value = "";
  error.value = "";
  loading.value = Boolean(id);
  if (!id) return;

  void loadFrame(id);
  unsubscribeEvents = source?.subscribe(id, () => {
    if (refreshQueued) return;
    refreshQueued = true;
    queueMicrotask(() => {
      refreshQueued = false;
      if (sessionId.value === id) void loadFrame(id);
    });
  }) ?? null;
}

const dispatch: ApplicationActionDispatcher = async (envelope) => {
  try {
    const outcome = await rpc.post<ApplicationOutcome>("runstatus.application.action", {
      ...envelope,
      transport: "web",
      ...(currentPage.value ? { page: currentPage.value } : {}),
    });
    if (outcome.frame) {
      frame.value = outcome.frame;
      currentPage.value = outcome.frame.page;
    }
    return { ok: true };
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

onMounted(async () => {
  source = createDataSource();
  try {
    followSession(await source.getCurrentSession());
    unsubscribeCurrent = source.subscribeCurrentSession(followSession);
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause);
    loading.value = false;
  }
});

onUnmounted(() => {
  unsubscribeCurrent?.();
  unsubscribeEvents?.();
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
</style>
