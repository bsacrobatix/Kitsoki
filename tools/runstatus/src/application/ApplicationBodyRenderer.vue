<script setup lang="ts">
import { computed } from "vue";
import { componentEventListeners } from "./component-bindings.js";
import type { ApplicationComponentBody, ApplicationElement, JSONValue } from "./types.js";
import type { ApplicationRuntime } from "./runtime.js";
import { semanticDataAttributes } from "./semantic.js";
import ApplicationWizardForm from "./ApplicationWizardForm.vue";

const props = defineProps<{
  body: ApplicationElement;
  runtime: ApplicationRuntime;
}>();

const componentBody = computed(() =>
  props.body.kind === "component" && props.body.component
    ? props.body as ApplicationComponentBody
    : undefined
);
const componentResolution = computed(() =>
  componentBody.value
    ? props.runtime.components.resolve(componentBody.value.component)
    : {}
);
const componentProps = computed(() => {
  const value = componentBody.value?.props;
  return isRecord(value) ? value : {};
});
const componentDescriptor = computed(() =>
  props.runtime.frame.components?.find(
    (candidate) => candidate.id === componentBody.value?.component
  )
);
const componentListeners = computed(() =>
  componentBody.value
    ? componentEventListeners(componentBody.value, componentDescriptor.value, props.runtime)
    : {}
);
const fallback = computed<ApplicationElement | undefined>(() => {
  if (!componentBody.value || componentResolution.value.registration?.component) return undefined;
  if (componentResolution.value.fallback) return componentResolution.value.fallback;
  const descriptor = componentDescriptor.value;
  if (!descriptor?.fallback || descriptor.fallback === "component") return undefined;
  return {
    ...componentBody.value,
    kind: descriptor.fallback,
    component: undefined,
    value: componentBody.value.value ?? componentBody.value.props,
  };
});
const listItems = computed(() => {
  if (props.body.items?.length) return props.body.items;
  if (Array.isArray(props.body.value)) {
    return props.body.value.map<ApplicationElement>((value) => ({ kind: "prose", value }));
  }
  if (props.body.value !== undefined) {
    return [{ kind: "prose", value: props.body.value }];
  }
  return [] as ApplicationElement[];
});
const tableRows = computed(() =>
  Array.isArray(props.body.value)
    ? props.body.value.filter(isRecord)
    : []
);
const tableColumns = computed(() => {
  const keys = new Set<string>();
  for (const row of tableRows.value) {
    for (const key of Object.keys(row)) keys.add(key);
  }
  return [...keys];
});
const keyValues = computed(() =>
  isRecord(props.body.value) ? Object.entries(props.body.value) : []
);
const artifact = computed(() => {
  if (typeof props.body.value === "string") {
    return { handle: props.body.value, name: props.body.value };
  }
  if (isRecord(props.body.value) && typeof props.body.value.handle === "string") {
    return {
      handle: props.body.value.handle,
      name: typeof props.body.value.name === "string"
        ? props.body.value.name
        : props.body.value.handle,
    };
  }
  return undefined;
});
const media = computed(() => {
  if (typeof props.body.value === "string") {
    return { handle: props.body.value, caption: props.body.value };
  }
  if (isRecord(props.body.value)) {
    const handle = typeof props.body.value.handle === "string"
      ? props.body.value.handle
      : typeof props.body.value.MediaHandle === "string"
        ? props.body.value.MediaHandle
        : "";
    if (handle) {
      return {
        handle,
        caption: typeof props.body.value.caption === "string"
          ? props.body.value.caption
          : typeof props.body.value.MediaCaption === "string"
            ? props.body.value.MediaCaption
            : handle,
      };
    }
  }
  return undefined;
});

function isRecord(value: unknown): value is Readonly<Record<string, JSONValue>> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function display(value: JSONValue | undefined): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}
</script>

<template>
  <div
    v-show="body.state?.visible !== false"
    class="application-body"
    :class="`application-body--${body.kind}`"
    :data-body-kind="body.kind"
    v-bind="semanticDataAttributes(body.semantic)"
  >
    <component
      :is="componentResolution.registration.component"
      v-if="componentBody && componentResolution.registration?.component"
      v-bind="componentProps"
      v-on="componentListeners"
      :frame="runtime.frame"
      :body="componentBody"
      :dispatch="componentBody.events ? undefined : runtime.dispatch"
    />
    <ApplicationBodyRenderer
      v-else-if="fallback"
      class="application-body__fallback"
      :body="fallback"
      :runtime="runtime"
      data-component-fallback
    />
    <ApplicationWizardForm
      v-else-if="body.kind === 'form'"
      :body="body"
      :runtime="runtime"
    />
    <h4 v-else-if="body.kind === 'heading'" class="application-body__heading">{{ display(body.value) }}</h4>
    <p v-else-if="body.kind === 'prose'" class="application-body__prose">{{ display(body.value) }}</p>
    <pre v-else-if="body.kind === 'code' || body.kind === 'template'" class="application-body__code"><code>{{ display(body.value) }}</code></pre>
    <ul v-else-if="body.kind === 'list'" class="application-body__list">
      <li v-for="(item, index) in listItems" :key="item.id ?? index">
        <ApplicationBodyRenderer :body="item" :runtime="runtime" />
      </li>
    </ul>
    <div v-else-if="body.kind === 'table'" class="application-body__table-wrap">
      <table class="application-body__table">
        <thead>
          <tr>
            <th v-for="column in tableColumns" :key="column" scope="col">{{ column }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, rowIndex) in tableRows" :key="rowIndex">
            <td v-for="column in tableColumns" :key="column">{{ display(row[column]) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <dl v-else-if="body.kind === 'kv'" class="application-body__kv">
      <template v-for="[key, value] in keyValues" :key="key">
        <dt>{{ key }}</dt>
        <dd>{{ display(value) }}</dd>
      </template>
    </dl>
    <strong v-else-if="body.kind === 'banner'" class="application-body__banner">{{ display(body.value) }}</strong>
    <ul v-else-if="body.kind === 'choice'" class="application-body__choice">
      <li v-for="(item, index) in listItems" :key="item.id ?? index">
        {{ display(item.value) }}
      </li>
    </ul>
    <p
      v-else-if="body.kind === 'status'"
      class="application-body__status"
      :data-tone="body.state?.validation ?? 'neutral'"
      role="status"
    >
      {{ display(body.value) }}
    </p>
    <a
      v-else-if="body.kind === 'artifact' && artifact"
      class="application-body__artifact"
      :href="`/artifact/${encodeURIComponent(artifact.handle)}`"
    >
      {{ artifact.name }}
    </a>
    <a
      v-else-if="body.kind === 'media' && media"
      class="application-body__artifact"
      :href="`/artifact/${encodeURIComponent(media.handle)}`"
    >
      {{ media.caption }}
    </a>
    <p v-else class="application-body__unsupported" role="alert">
      Unable to render {{ body.kind }} content.
    </p>

    <div v-if="body.actions?.length" class="application-body__actions">
      <button
        v-for="action in body.actions"
        :key="action.id"
        type="button"
        :disabled="runtime.stale
          || action.enabled === false
          || action.state?.enabled === false
          || runtime.isActionPending(action.id)"
        :aria-busy="runtime.isActionPending(action.id) || undefined"
        v-bind="semanticDataAttributes(action.semantic)"
        @click="runtime.dispatch({
          action: action.id,
          input: {},
          session_id: runtime.frame.session_id,
          frame_revision: runtime.frame.revision,
        })"
      >
        {{ action.semantic.name }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.application-body__prose,
.application-body__status {
  margin: 0;
  white-space: pre-line;
}
.application-body__heading {
  margin: 0;
}
.application-body__code {
  margin: 0;
  overflow-x: auto;
  white-space: pre-wrap;
}
.application-body__kv {
  display: grid;
  grid-template-columns: minmax(8rem, auto) minmax(0, 1fr);
  gap: 0.35rem 0.75rem;
  margin: 0;
}
.application-body__kv dd {
  margin: 0;
}
.application-body__banner {
  display: block;
  padding: 0.5rem 0.625rem;
  border-inline-start: 3px solid var(--k-fg-accent, #1d4ed8);
}
.application-body__list {
  margin: 0;
  padding-inline-start: 1.25rem;
}
.application-body__list .application-body {
  display: inline;
}
.application-body__table-wrap {
  overflow-x: auto;
}
.application-body__table {
  width: 100%;
  border-collapse: collapse;
}
.application-body__table th,
.application-body__table td {
  padding: 0.5rem;
  border-bottom: 1px solid var(--k-border, #d8dce3);
  text-align: left;
}
.application-body__status {
  padding-inline-start: 0.625rem;
  border-inline-start: 3px solid var(--k-fg-muted, #596273);
}
.application-body__status[data-tone="valid"],
.application-body__status[data-tone="success"] {
  border-color: var(--k-success, #16794b);
}
.application-body__status[data-tone="pending"],
.application-body__status[data-tone="warning"] {
  border-color: var(--k-warning, #a86600);
}
.application-body__status[data-tone="invalid"],
.application-body__status[data-tone="error"],
.application-body__unsupported {
  color: var(--k-danger, #b42318);
}
.application-body__artifact {
  color: var(--k-fg-accent, #1d4ed8);
}
.application-body__actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.5rem;
}
</style>
