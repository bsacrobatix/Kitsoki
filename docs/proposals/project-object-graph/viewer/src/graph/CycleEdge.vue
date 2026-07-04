<template>
  <g class="vue-flow__edge k-edge k-edge--route-loop">
    <path
      class="vue-flow__edge-path"
      :d="path"
      :marker-end="markerEnd"
      fill="none"
    />
  </g>
  <EdgeLabelRenderer>
    <div
      v-if="label"
      class="k-edge-label"
      :style="labelStyleValue"
      :data-edge-label-id="id"
      data-edge-route="loop"
    >
      {{ label }}
    </div>
  </EdgeLabelRenderer>
</template>

<script setup>
import { computed } from "vue";
import { EdgeLabelRenderer, useVueFlow } from "@vue-flow/core";
import { labelStyle } from "./edge-label-position.js";
import { useAnimatedEdgeCoordinates } from "./use-animated-edge-coordinates.js";

const props = defineProps({
  id: { type: String, required: true },
  sourceX: { type: Number, required: true },
  sourceY: { type: Number, required: true },
  targetX: { type: Number, required: true },
  targetY: { type: Number, required: true },
  markerEnd: { type: String, default: "" },
  label: { type: String, default: "" },
});

const { nodes } = useVueFlow();
const coords = useAnimatedEdgeCoordinates(props);
const path = computed(() => {
  if (isSameNodeLoop(coords)) {
    const rightThirdX = coords.sourceX + 35;
    return [
      `M ${coords.sourceX} ${coords.sourceY}`,
      `L ${rightThirdX} ${coords.sourceY}`,
      `L ${rightThirdX} ${coords.targetY}`,
      `L ${coords.targetX} ${coords.targetY}`,
    ].join(" ");
  }

  const lane = 112;
  const busY = Math.max(coords.sourceY, coords.targetY) + lane;
  const busX = Math.max(coords.sourceX, coords.targetX) + lane;
  return [
    `M ${coords.sourceX} ${coords.sourceY}`,
    `L ${busX} ${coords.sourceY}`,
    `L ${busX} ${busY}`,
    `L ${coords.targetX} ${busY}`,
    `L ${coords.targetX} ${coords.targetY}`,
  ].join(" ");
});

const labelStyleValue = computed(() => labelStyle({
  sourceX: props.sourceX,
  sourceY: props.sourceY,
  targetX: props.targetX,
  targetY: props.targetY,
  label: props.label,
  route: "loop",
  direction: "RIGHT",
  nodes,
}));

function isSameNodeLoop(points) {
  return Math.abs(points.sourceX - points.targetX) < 8 && Math.abs(points.sourceY - points.targetY) < 120;
}
</script>
