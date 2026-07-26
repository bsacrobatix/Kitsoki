<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import type { ApplicationRuntime } from "./runtime.js";
import type { ApplicationElement, JSONValue } from "./types.js";

interface FieldOption {
  readonly value: string;
  readonly label: string;
}

interface WizardField {
  readonly id: string;
  readonly label: string;
  readonly description?: string;
  readonly type: "text" | "textarea" | "number" | "select" | "checkbox";
  readonly required?: boolean;
  readonly min?: number;
  readonly max?: number;
  readonly pattern?: string;
  readonly placeholder?: string;
  readonly options?: readonly FieldOption[];
}

const props = defineProps<{
  body: ApplicationElement;
  runtime: ApplicationRuntime;
}>();

const values = reactive<Record<string, string | number | boolean>>({});
const errors = ref<Record<string, string>>({});
const config = computed(() => isRecord(props.body.props) ? props.body.props : {});
const fields = computed<readonly WizardField[]>(() => {
  const declared = config.value.fields;
  if (!Array.isArray(declared)) return [];
  return declared.flatMap((field) => normalizeField(field));
});
const submitAction = computed(() => {
  if (typeof config.value.submit_action === "string") return config.value.submit_action;
  return props.body.actions?.[0]?.id ?? "";
});

function normalizeField(value: JSONValue): WizardField[] {
  if (!isRecord(value) || typeof value.id !== "string" || typeof value.label !== "string") {
    return [];
  }
  const type = typeof value.type === "string" && ["text", "textarea", "number", "select", "checkbox"].includes(value.type)
    ? value.type as WizardField["type"]
    : "text";
  const options = Array.isArray(value.options)
    ? value.options.flatMap((option) =>
      isRecord(option) && typeof option.value === "string" && typeof option.label === "string"
        ? [{ value: option.value, label: option.label }]
        : [])
    : undefined;
  return [{
    id: value.id,
    label: value.label,
    type,
    description: typeof value.description === "string" ? value.description : undefined,
    required: value.required === true,
    min: typeof value.min === "number" ? value.min : undefined,
    max: typeof value.max === "number" ? value.max : undefined,
    pattern: typeof value.pattern === "string" ? value.pattern : undefined,
    placeholder: typeof value.placeholder === "string" ? value.placeholder : undefined,
    options,
  }];
}

function validate(): boolean {
  const next: Record<string, string> = {};
  for (const field of fields.value) {
    const value = values[field.id];
    if (field.required && (value === undefined || value === "" || value === false)) {
      next[field.id] = `${field.label} is required.`;
      continue;
    }
    if (field.type === "number" && typeof value === "number") {
      if (field.min !== undefined && value < field.min) next[field.id] = `${field.label} must be at least ${field.min}.`;
      if (field.max !== undefined && value > field.max) next[field.id] = `${field.label} must be at most ${field.max}.`;
    }
    if (field.pattern && typeof value === "string" && value && !new RegExp(field.pattern).test(value)) {
      next[field.id] = `${field.label} has an invalid format.`;
    }
  }
  errors.value = next;
  return Object.keys(next).length === 0;
}

function submit(): void {
  if (!submitAction.value || !validate()) return;
  void props.runtime.dispatch({
    action: submitAction.value,
    input: { ...values },
    session_id: props.runtime.frame.session_id,
    frame_revision: props.runtime.frame.revision,
  });
}

function updateValue(field: WizardField, event: Event): void {
  const target = event.target as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
  if (field.type === "checkbox") {
    values[field.id] = (target as HTMLInputElement).checked;
  } else if (field.type === "number") {
    values[field.id] = target.value === "" ? "" : Number(target.value);
  } else {
    values[field.id] = target.value;
  }
  if (errors.value[field.id]) {
    const next = { ...errors.value };
    delete next[field.id];
    errors.value = next;
  }
}

function fieldValue(id: string): string | number {
  const value = values[id];
  return typeof value === "string" || typeof value === "number" ? value : "";
}

function isRecord(value: unknown): value is Readonly<Record<string, JSONValue>> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
</script>

<template>
  <form class="wizard-form" data-testid="application-wizard-form" novalidate @submit.prevent="submit">
    <label v-for="field in fields" :key="field.id" class="wizard-form__field">
      <span>{{ field.label }}</span>
      <small v-if="field.description">{{ field.description }}</small>
      <textarea
        v-if="field.type === 'textarea'"
        :value="fieldValue(field.id)"
        :placeholder="field.placeholder"
        :aria-invalid="Boolean(errors[field.id])"
        @input="updateValue(field, $event)"
      />
      <select
        v-else-if="field.type === 'select'"
        :value="fieldValue(field.id)"
        :aria-invalid="Boolean(errors[field.id])"
        @change="updateValue(field, $event)"
      >
        <option value="">Select…</option>
        <option v-for="option in field.options" :key="option.value" :value="option.value">
          {{ option.label }}
        </option>
      </select>
      <input
        v-else-if="field.type === 'checkbox'"
        type="checkbox"
        :checked="Boolean(values[field.id])"
        :aria-invalid="Boolean(errors[field.id])"
        @change="updateValue(field, $event)"
      />
      <input
        v-else
        :type="field.type"
        :value="fieldValue(field.id)"
        :min="field.min"
        :max="field.max"
        :placeholder="field.placeholder"
        :aria-invalid="Boolean(errors[field.id])"
        @input="updateValue(field, $event)"
      />
      <span v-if="errors[field.id]" class="wizard-form__error" role="alert">{{ errors[field.id] }}</span>
    </label>
    <button type="submit" :disabled="runtime.stale || !submitAction">
      {{ typeof config.submit_label === "string" ? config.submit_label : "Continue" }}
    </button>
  </form>
</template>

<style scoped>
.wizard-form {
  display: grid;
  gap: 0.875rem;
}
.wizard-form__field {
  display: grid;
  gap: 0.3rem;
  font-weight: 600;
}
.wizard-form__field small {
  color: var(--k-fg-muted, #596273);
  font-weight: 400;
}
.wizard-form input:not([type="checkbox"]),
.wizard-form select,
.wizard-form textarea {
  min-height: 2.4rem;
  padding: 0.5rem 0.625rem;
  border: 1px solid var(--k-border, #d8dce3);
  border-radius: 4px;
  background: var(--k-paper-bg, #fff);
  color: inherit;
}
.wizard-form textarea {
  min-height: 6rem;
  resize: vertical;
}
.wizard-form__error {
  color: var(--k-danger, #b42318);
  font-size: 0.875rem;
  font-weight: 400;
}
.wizard-form button {
  justify-self: start;
  min-height: 2.4rem;
  padding: 0.45rem 0.8rem;
  border: 1px solid var(--k-button-bg, #1d4ed8);
  border-radius: 6px;
  background: var(--k-button-bg, #1d4ed8);
  color: #fff;
}
</style>
