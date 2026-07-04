<template>
  <div class="graph-node">
    <Handle id="target-top" type="target" :position="Position.Top" />
    <Handle id="source-top" type="source" :position="Position.Top" />
    <Handle id="target-right" type="target" :position="Position.Right" />
    <Handle id="source-right" type="source" :position="Position.Right" />
    <Handle id="target-bottom" type="target" :position="Position.Bottom" />
    <Handle id="source-bottom" type="source" :position="Position.Bottom" />
    <Handle id="target-left" type="target" :position="Position.Left" />
    <Handle id="source-left" type="source" :position="Position.Left" />

    <div class="graph-node__label">{{ data.label }}</div>
    <div class="graph-node__ref">{{ data.ref }}</div>
  </div>
</template>

<script setup>
import { Handle, Position } from "@vue-flow/core";

defineProps({
  data: {
    type: Object,
    required: true,
  },
});
</script>
