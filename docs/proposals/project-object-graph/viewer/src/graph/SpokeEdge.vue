<template>
  <g :class="edgeClass">
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
      class="k-edge-label k-spoke-label"
      :style="labelStyleValue"
      :data-edge-label-id="id"
      :data-edge-route="route"
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
  data: { type: Object, default: () => ({}) },
});

const route = computed(() => props.data?.route || "forward");
const { nodes } = useVueFlow();
const coords = useAnimatedEdgeCoordinates(props);
const edgeClass = computed(() => [
  "vue-flow__edge",
  "k-edge",
  "k-spoke-edge",
  `k-edge--route-${route.value}`,
  props.data?.sequence !== undefined ? "k-edge--observed-transition" : "",
].filter(Boolean).join(" "));

const path = computed(() => {
  const direction = props.data?.direction || "RIGHT";
  if (route.value === "forward" || !travelsAgainstLayout(coords, direction)) {
    return forwardPath(coords, props.data?.direction || "RIGHT");
  }

  return returnLanePath(coords, direction, laneOffset(props.id));
});

const labelStyleValue = computed(() => labelStyle({
  sourceX: props.sourceX,
  sourceY: props.sourceY,
  targetX: props.targetX,
  targetY: props.targetY,
  label: props.label,
  route: route.value,
  direction: props.data?.direction || "RIGHT",
  nodes,
}));

function forwardPath(points, direction) {
  const horizontal = direction === "RIGHT" || direction === "LEFT";
  const sx = points.sourceX;
  const sy = points.sourceY;
  const tx = points.targetX;
  const ty = points.targetY;

  if (horizontal) {
    if (Math.abs(sy - ty) < 1) {
      return [`M ${sx} ${sy}`, `L ${tx} ${ty}`].join(" ");
    }
    const midX = (sx + tx) / 2;
    return [
      `M ${sx} ${sy}`,
      `L ${midX} ${sy}`,
      `L ${midX} ${ty}`,
      `L ${tx} ${ty}`,
    ].join(" ");
  }

  if (Math.abs(sx - tx) < 1) {
    return [`M ${sx} ${sy}`, `L ${tx} ${ty}`].join(" ");
  }
  const midY = (sy + ty) / 2;
  return [
    `M ${sx} ${sy}`,
    `L ${sx} ${midY}`,
    `L ${tx} ${midY}`,
    `L ${tx} ${ty}`,
  ].join(" ");
}

function returnLanePath(points, direction, lane) {
  const horizontal = direction === "RIGHT" || direction === "LEFT";
  const sx = points.sourceX;
  const sy = points.sourceY;
  const tx = points.targetX;
  const ty = points.targetY;

  if (horizontal) {
    const busY = Math.max(sy, ty) + lane;
    return [
      `M ${sx} ${sy}`,
      `L ${sx} ${busY}`,
      `L ${tx} ${busY}`,
      `L ${tx} ${ty}`,
    ].join(" ");
  }

  const busX = Math.max(sx, tx) + lane;
  return [
    `M ${sx} ${sy}`,
    `L ${busX} ${sy}`,
    `L ${busX} ${ty}`,
    `L ${tx} ${ty}`,
  ].join(" ");
}

function travelsAgainstLayout(points, direction) {
  if (direction === "RIGHT") {
    return points.targetX < points.sourceX;
  }
  if (direction === "LEFT") {
    return points.targetX > points.sourceX;
  }
  if (direction === "DOWN") {
    return points.targetY < points.sourceY;
  }
  return points.targetY > points.sourceY;
}

function laneOffset(id) {
  let hash = 0;
  for (const char of String(id)) {
    hash = (hash * 31 + char.charCodeAt(0)) % 997;
  }
  return 64 + (hash % 4) * 22;
}
</script>
