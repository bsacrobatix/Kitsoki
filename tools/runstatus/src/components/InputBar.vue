<template>
  <div class="input-bar">
    <div v-if="actionIntents.length" class="input-bar__actions" data-testid="intent-actions">
      <button
        v-for="intent in actionIntents"
        :key="intent.name"
        class="input-bar__action-btn"
        type="button"
        :disabled="pending"
        :data-testid="`intent-btn-${intent.name}`"
        @click="fireIntent(intent)"
      >
        {{ intent.title || intent.name }}
      </button>
    </div>

    <form
      v-if="textIntents.length"
      class="input-bar__composer"
      data-testid="composer"
      :data-active-intent="selectedTextName"
      @submit.prevent="send"
    >
      <select
        v-if="textIntents.length > 1"
        v-model="selectedTextName"
        class="input-bar__select"
        data-testid="composer-select"
        :disabled="pending"
      >
        <option v-for="intent in textIntents" :key="intent.name" :value="intent.name">
          {{ intent.title || intent.name }}
        </option>
      </select>

      <input
        v-model="draft"
        class="input-bar__input"
        type="text"
        data-testid="composer-input"
        :placeholder="placeholder"
        :disabled="pending"
      />

      <button
        class="input-bar__send"
        type="submit"
        data-testid="composer-send"
        :disabled="pending || !draft.trim()"
      >
        Send
      </button>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { IntentInfo } from "../types.js";

const props = defineProps<{
  intents: IntentInfo[];
  pending?: boolean;
}>();

const emit = defineEmits<{
  (e: "send", text: string, intentName: string): void;
  (e: "intent", name: string, slots: Record<string, unknown>): void;
}>();

/** Intents with no free-text slot and no slots at all -> plain action buttons. */
const actionIntents = computed(() =>
  props.intents.filter((i) => !i.text_slot && !i.has_slots),
);

/** Intents that bind a single free-text slot -> the composer input. */
const textIntents = computed(() => props.intents.filter((i) => !!i.text_slot));

const selectedTextName = ref<string>("");

// Default the selected text intent to the first one; keep it valid as intents change.
watch(
  textIntents,
  (list) => {
    if (!list.length) {
      selectedTextName.value = "";
      return;
    }
    if (!list.some((i) => i.name === selectedTextName.value)) {
      selectedTextName.value = list[0].name;
    }
  },
  { immediate: true },
);

const activeTextIntent = computed<IntentInfo | undefined>(() =>
  textIntents.value.find((i) => i.name === selectedTextName.value),
);

const placeholder = computed(() => {
  const it = activeTextIntent.value;
  if (!it) return "Type a message…";
  return `${it.title || it.name}…`;
});

const draft = ref("");

function fireIntent(intent: IntentInfo) {
  if (props.pending) return;
  emit("intent", intent.name, {});
}

function send() {
  const text = draft.value.trim();
  const it = activeTextIntent.value;
  if (props.pending || !text || !it) return;
  // Emit both: a high-level `send` (text + intent name) and the structured
  // `intent` carrying the text in the intent's declared slot.
  emit("send", text, it.name);
  emit("intent", it.name, { [it.text_slot as string]: text });
  draft.value = "";
}
</script>

<style scoped>
.input-bar {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px 18px;
  background: #14171d;
  border-top: 1px solid #2a2f3a;
}

.input-bar__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.input-bar__action-btn {
  appearance: none;
  border: 1px solid #3a4250;
  background: #1f2530;
  color: #e6e9ef;
  font-size: 13px;
  font-weight: 600;
  padding: 7px 16px;
  border-radius: 8px;
  cursor: pointer;
  transition:
    background 0.12s ease,
    border-color 0.12s ease;
}

.input-bar__action-btn:hover:not(:disabled) {
  background: #2a3340;
  border-color: #4a5568;
}

.input-bar__action-btn:disabled,
.input-bar__send:disabled,
.input-bar__input:disabled,
.input-bar__select:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.input-bar__composer {
  display: flex;
  align-items: stretch;
  gap: 8px;
}

.input-bar__select {
  background: #1f2530;
  color: #e6e9ef;
  border: 1px solid #3a4250;
  border-radius: 8px;
  padding: 0 10px;
  font-size: 13px;
}

.input-bar__input {
  flex: 1 1 auto;
  background: #0f1115;
  color: #e6e9ef;
  border: 1px solid #3a4250;
  border-radius: 8px;
  padding: 10px 14px;
  font-size: 14px;
  outline: none;
}

.input-bar__input:focus {
  border-color: #2563eb;
}

.input-bar__send {
  appearance: none;
  border: none;
  background: #2563eb;
  color: #fff;
  font-size: 14px;
  font-weight: 600;
  padding: 0 20px;
  border-radius: 8px;
  cursor: pointer;
}

.input-bar__send:hover:not(:disabled) {
  background: #1d4ed8;
}
</style>
