<script setup lang="ts">
import { computed, provide, ref } from "vue";
import ApplicationBodyRenderer from "./ApplicationBodyRenderer.vue";
import { createApplicationComponentRegistry } from "./registry.js";
import { applicationRuntimeKey } from "./runtime.js";
import { semanticDataAttributes } from "./semantic.js";
import type {
  ApplicationAction,
  ApplicationActionEnvelope,
  ApplicationActionDispatcher,
  ApplicationComponentRegistrySource,
  ApplicationFrame,
  ApplicationNavigationItem,
  JSONValue,
} from "./types.js";

const props = withDefaults(defineProps<{
  frame: ApplicationFrame;
  dispatch: ApplicationActionDispatcher;
  components?: ApplicationComponentRegistrySource;
  stale?: boolean | string;
}>(), {
  components: () => ({}),
  stale: false,
});
const emit = defineEmits<{
  (event: "navigate", page: string): void;
}>();

const registry = computed(() => createApplicationComponentRegistry(props.components));
const pendingActions = ref(new Set<string>());
const dispatchError = ref("");
const staleReason = computed(() => {
  if (typeof props.stale === "string") return props.stale;
  if (props.stale) return "Reload the session before dispatching another action.";
  return props.frame.errors?.find((error) => error.code === "STALE_FRAME")?.message ?? "";
});
const isStale = computed(() => Boolean(staleReason.value));

const runtime = {
  get frame() {
    return props.frame;
  },
  dispatch: dispatchEnvelope,
  get components() {
    return registry.value;
  },
  get stale() {
    return isStale.value;
  },
  isActionPending(action: string) {
    return pendingActions.value.has(action);
  },
};
provide(applicationRuntimeKey, runtime);

function envelope(action: string, input: JSONValue = {}) {
  return {
    action,
    input,
    session_id: props.frame.session_id,
    frame_revision: props.frame.revision,
  };
}

async function dispatchEnvelope(value: ApplicationActionEnvelope): Promise<void> {
  if (isStale.value || pendingActions.value.has(value.action)) return;
  dispatchError.value = "";
  pendingActions.value = new Set(pendingActions.value).add(value.action);
  try {
    const result = await props.dispatch(value);
    if (result && !result.ok) {
      dispatchError.value = result.error || `Action ${value.action} failed.`;
    }
  } catch (error) {
    dispatchError.value = error instanceof Error ? error.message : String(error);
  } finally {
    const pending = new Set(pendingActions.value);
    pending.delete(value.action);
    pendingActions.value = pending;
  }
}

function dispatchAction(action: ApplicationAction): void {
  void dispatchEnvelope(envelope(action.id));
}

function dispatchNavigation(item: ApplicationNavigationItem): void {
  if (!isStale.value && item.state?.enabled !== false) {
    emit("navigate", item.page);
  }
}

function actionDisabled(action: ApplicationAction): boolean {
  return isStale.value
    || !action.enabled
    || action.state?.enabled === false
    || pendingActions.value.has(action.id);
}
</script>

<template>
  <main
    class="application-frame"
    data-application-root
    :data-application-id="frame.application_id"
    :data-frame-revision="frame.revision"
    :data-frame-stale="isStale || undefined"
    :aria-label="frame.semantic.name"
    v-bind="semanticDataAttributes(frame.semantic)"
  >
    <aside v-if="isStale" class="application-frame__stale" role="alert" data-testid="application-stale">
      <strong>This application session is stale.</strong>
      <span>{{ staleReason }}</span>
    </aside>

    <nav
      v-if="frame.navigation?.length"
      class="application-frame__navigation"
      aria-label="Application"
    >
      <button
        v-for="item in frame.navigation"
        :key="item.id"
        type="button"
        class="application-frame__nav-item"
        :class="{ 'application-frame__nav-item--selected': item.state?.selected || item.page === frame.page }"
        :aria-current="item.state?.selected || item.page === frame.page ? 'page' : undefined"
        :aria-label="item.semantic.name"
        :title="item.semantic.description"
        :disabled="isStale || item.state?.enabled === false"
        v-bind="semanticDataAttributes(item.semantic)"
        @click="dispatchNavigation(item)"
      >
        {{ item.semantic.name }}
      </button>
    </nav>

    <section v-if="frame.errors?.length || dispatchError" class="application-frame__errors" aria-label="Application errors">
      <p
        v-for="error in frame.errors"
        :key="`${error.code}:${error.message}`"
        role="alert"
        :data-error-code="error.code"
        :data-semantic-ref="error.semantic_ref"
      >
        {{ error.message }}
      </p>
      <p v-if="dispatchError" role="alert" data-testid="application-dispatch-error">{{ dispatchError }}</p>
    </section>

    <section
      v-for="region in frame.regions ?? []"
      v-show="region.state?.visible !== false"
      :key="region.id"
      class="application-frame__region"
      :aria-label="region.semantic.name"
      v-bind="semanticDataAttributes(region.semantic)"
    >
      <header class="application-frame__region-header">
        <h2>{{ region.semantic.name }}</h2>
        <p>{{ region.semantic.description }}</p>
      </header>

      <article
        v-for="card in region.cards ?? []"
        v-show="card.state?.visible !== false"
        :key="card.id"
        class="application-frame__card"
        :class="{ 'application-frame__card--selected': card.state?.selected }"
        :aria-label="card.semantic.name"
        v-bind="semanticDataAttributes(card.semantic)"
      >
        <header class="application-frame__card-header">
          <h3>{{ card.semantic.name }}</h3>
          <p>{{ card.semantic.description }}</p>
        </header>

        <div class="application-frame__body">
          <ApplicationBodyRenderer
            v-for="(body, index) in card.body ?? []"
            :key="body.id ?? `${body.kind}-${index}`"
            :body="body"
            :runtime="runtime"
          />
        </div>

        <footer v-if="card.actions?.length" class="application-frame__actions">
          <button
            v-for="action in card.actions"
            :key="action.id"
            type="button"
            :disabled="actionDisabled(action)"
            :aria-label="action.semantic.name"
            :title="action.semantic.description"
            :aria-busy="pendingActions.has(action.id) || undefined"
            v-bind="semanticDataAttributes(action.semantic)"
            @click="dispatchAction(action)"
          >
            {{ action.semantic.name }}
          </button>
        </footer>
      </article>
    </section>
  </main>
</template>

<style scoped>
.application-frame {
  display: grid;
  grid-template-columns: minmax(10rem, 14rem) minmax(0, 1fr);
  align-content: start;
  gap: 1rem 1.5rem;
  width: 100%;
  color: var(--k-fg, #1d2433);
}
.application-frame__stale,
.application-frame__errors {
  grid-column: 1 / -1;
  padding: 0.75rem 1rem;
  border-inline-start: 4px solid var(--k-danger, #b42318);
  background: var(--k-danger-bg, #fff2f0);
}
.application-frame__stale {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}
.application-frame__errors p {
  margin: 0.25rem 0;
}
.application-frame__navigation {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  position: sticky;
  top: 1rem;
}
.application-frame__nav-item {
  padding: 0.55rem 0.7rem;
  border: 0;
  border-inline-start: 3px solid transparent;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
}
.application-frame__nav-item--selected {
  border-color: var(--k-fg-accent, #1d4ed8);
  background: var(--k-selected-bg, #eef4ff);
  font-weight: 600;
}
.application-frame__region {
  grid-column: 2;
  min-width: 0;
}
.application-frame__region + .application-frame__region {
  margin-top: 1rem;
}
.application-frame__region-header h2,
.application-frame__card-header h3 {
  margin: 0;
}
.application-frame__region-header p,
.application-frame__card-header p {
  margin: 0.25rem 0 0;
  color: var(--k-fg-muted, #596273);
}
.application-frame__region-header {
  margin-bottom: 0.75rem;
}
.application-frame__card {
  padding: 1rem;
  border: 1px solid var(--k-border, #d8dce3);
  border-radius: 6px;
  background: var(--k-paper-bg, #fff);
}
.application-frame__card + .application-frame__card {
  margin-top: 0.75rem;
}
.application-frame__card--selected {
  border-color: var(--k-fg-accent, #1d4ed8);
}
.application-frame__body {
  display: grid;
  gap: 0.75rem;
  margin-top: 0.875rem;
}
.application-frame__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-top: 1rem;
}
.application-frame__actions button {
  min-height: 2.25rem;
  padding: 0.45rem 0.75rem;
  border: 1px solid var(--k-button-bg, #1d4ed8);
  border-radius: 6px;
  background: var(--k-button-bg, #1d4ed8);
  color: var(--k-button-fg, #fff);
  cursor: pointer;
}
button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
@media (max-width: 700px) {
  .application-frame {
    grid-template-columns: minmax(0, 1fr);
  }
  .application-frame__navigation {
    position: static;
    flex-direction: row;
    overflow-x: auto;
  }
  .application-frame__region {
    grid-column: 1;
  }
}
</style>
