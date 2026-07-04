<template>
  <div class="graph-canvas-host" :aria-busy="loading ? 'true' : 'false'">
    <div v-if="loading" class="loading">Laying out graph...</div>
    <VueFlow
      v-model:nodes="nodes"
      v-model:edges="edges"
      :nodes-draggable="true"
      :nodes-connectable="false"
      :edges-updatable="false"
      :elements-selectable="true"
      :min-zoom="0.14"
      :max-zoom="1.6"
      @node-click="onNodeClick"
      @edge-click="onEdgeClick"
      @pane-ready="onPaneReady"
    >
      <template #node-kitsoki="nodeProps">
        <GraphNode v-bind="nodeProps" />
      </template>
      <template #edge-cycle="edgeProps">
        <CycleEdge v-bind="edgeProps" />
      </template>
      <template #edge-spoke="edgeProps">
        <SpokeEdge v-bind="edgeProps" />
      </template>
      <Background pattern-color="#d6dde8" :gap="18" />
      <Controls />
    </VueFlow>
  </div>
</template>

<script setup>
import { computed, nextTick, ref, watch } from "vue";
import { MarkerType, useVueFlow, VueFlow } from "@vue-flow/core";
import { Background } from "@vue-flow/background";
import { Controls } from "@vue-flow/controls";
import ELK from "elkjs/lib/elk.bundled.js";
import CycleEdge from "./CycleEdge.vue";
import GraphNode from "./GraphNode.vue";
import SpokeEdge from "./SpokeEdge.vue";

const props = defineProps({
  graph: { type: Object, required: true },
  direction: { type: String, default: "RIGHT" },
  radius: { type: Number, default: Infinity },
  focusId: { type: String, default: "" },
});
const emit = defineEmits(["update:focusId", "element-selected"]);

const elk = new ELK();
const NODE_WIDTH = 210;
const NODE_HEIGHT = 78;

const nodes = ref([]);
const edges = ref([]);
const loading = ref(false);
const paneReady = ref(false);
const { setCenter } = useVueFlow();

const activeFocusId = computed(() => {
  if (props.graph.nodes.some((node) => node.id === props.focusId)) return props.focusId;
  return preferredFocusNode(props.graph)?.id || "";
});
const visibleGraph = computed(() => buildNeighborhoodGraph(props.graph, activeFocusId.value, props.radius));

watch(
  [() => props.graph, () => props.direction, () => props.radius, activeFocusId],
  relayout,
  { immediate: true },
);

function onPaneReady() {
  paneReady.value = true;
  scheduleFocusView({ duration: 0 });
}

async function relayout() {
  loading.value = true;
  const laidOut = await layoutGraph(visibleGraph.value, props.direction);
  nodes.value = laidOut.nodes;
  edges.value = laidOut.edges;
  loading.value = false;
  await nextTick();
  if (paneReady.value) scheduleFocusView({ duration: 220 });
}

function scheduleFocusView({ duration = 220, delay = 0 } = {}) {
  window.setTimeout(() => {
    requestAnimationFrame(() => centerOnCurrentNode(duration));
  }, delay);
}

function centerOnCurrentNode(duration = 520) {
  const focusedNode = nodes.value.find((node) => node.id === activeFocusId.value) || nodes.value[0];
  if (!focusedNode) {
    return;
  }
  setCenter(
    focusedNode.position.x + NODE_WIDTH / 2,
    focusedNode.position.y + NODE_HEIGHT / 2,
    { zoom: readableZoom(), duration },
  );
}

function readableZoom() {
  if (props.radius === Infinity) {
    return 0.62;
  }
  if (props.radius === 3) {
    return 0.72;
  }
  if (props.radius === 2) {
    return 0.84;
  }
  return 0.96;
}

function graphToFlow(sourceGraph, layoutDirection) {
  const denseForwardLabels = sourceGraph.edges.length > 14;
  const flowNodes = sourceGraph.nodes.map((node) => ({
    id: node.id,
    type: "kitsoki",
    position: { x: 0, y: 0 },
    data: {
      label: node.label,
      kind: node.kind,
      status: node.status || "",
      ref: `${node.ref.kind}:${node.ref.ref}`,
      attrs: node.attrs || {},
    },
    class: [
      "k-node",
      `k-node--${node.kind}`,
      node.status ? `k-node--${node.status}` : "",
      node.attrs?.has_agent ? "k-node--agent" : "",
      node.attrs?.unreachable ? "k-node--unreachable" : "",
      node.attrs?.focus_current ? "k-node--focus-current" : "",
      node.attrs?.focus_frontier ? "k-node--focus-frontier" : "",
    ].filter(Boolean).join(" "),
    targetPosition: "left",
    sourcePosition: "right",
  }));

  const nodeById = new Map(flowNodes.map((node) => [node.id, node]));
  const flowEdges = sourceGraph.edges.map((edge) => {
    const handles = handlesForVisibleEdge(edge, nodeById, layoutDirection);
    return {
      id: edge.id,
      source: edge.source,
      target: edge.target,
      sourceHandle: handles.sourceHandle,
      targetHandle: handles.targetHandle,
      label: shouldShowEdgeLabel(edge, denseForwardLabels) ? edge.label : "",
      type: edge.attrs?.route === "loop" ? "cycle" : "spoke",
      animated: edge.status === "active",
      markerEnd: MarkerType.ArrowClosed,
      class: [
        "k-edge",
        `k-edge--${edge.kind}`,
        edge.attrs?.route ? `k-edge--route-${edge.attrs.route}` : "",
        edge.status ? `k-edge--${edge.status}` : "",
      ].filter(Boolean).join(" "),
      data: { ...(edge.attrs || {}), original_label: edge.label, direction: layoutDirection },
    };
  });

  return { nodes: flowNodes, edges: flowEdges };
}

function shouldShowEdgeLabel(edge, denseForwardLabels) {
  const route = edge.attrs?.route || "forward";
  if (route === "loop" || route === "backtrack" || edge.source === edge.target) {
    return true;
  }
  return !denseForwardLabels;
}

async function layoutGraph(sourceGraph, layoutDirection) {
  const flow = graphToFlow(sourceGraph, layoutDirection);
  const elkGraph = {
    id: "root",
    layoutOptions: {
      "elk.algorithm": sourceGraph.layout_hints?.default || "layered",
      "elk.direction": layoutDirection || sourceGraph.layout_hints?.rankdir || "RIGHT",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.layered.cycleBreaking.strategy": "GREEDY",
      "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
      "elk.layered.crossingMinimization.forceNodeModelOrder": "true",
      "elk.layered.nodePlacement.strategy": "NETWORK_SIMPLEX",
      "elk.layered.spacing.nodeNodeBetweenLayers": "150",
      "elk.layered.spacing.edgeNodeBetweenLayers": "52",
      "elk.layered.spacing.edgeEdgeBetweenLayers": "28",
      "elk.spacing.nodeNode": "80",
      "elk.spacing.edgeNode": "34",
      "elk.spacing.edgeEdge": "24",
      "elk.padding": "[top=36,left=36,bottom=36,right=36]",
    },
    children: flow.nodes.map((node) => ({
      id: node.id,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    })),
    edges: flow.edges
      .filter((edge) => edge.source !== edge.target)
      .map((edge) => ({
        id: edge.id,
        sources: [edge.source],
        targets: [edge.target],
      })),
  };

  const result = await elk.layout(elkGraph);
  const positions = new Map((result.children || []).map((node) => [node.id, node]));
  return {
    nodes: layoutFocusedSpokes(flow.nodes.map((node) => {
      const pos = positions.get(node.id);
      return {
        ...node,
        position: { x: pos?.x || 0, y: pos?.y || 0 },
      };
    }), layoutDirection),
    edges: flow.edges,
  };
}

function layoutFocusedSpokes(flowNodes, layoutDirection) {
  const vertical = layoutDirection === "DOWN" || layoutDirection === "UP";
  const reverse = layoutDirection === "LEFT" || layoutDirection === "UP";
  const rankAttr = vertical ? "y" : "x";
  const laneAttr = vertical ? "x" : "y";
  const rankedNodes = flowNodes
    .map((node) => ({ node, rank: Number(node.data.attrs?.focus_rank ?? node.data.attrs?.distance) }))
    .filter((item) => Number.isFinite(item.rank));

  if (rankedNodes.length !== flowNodes.length) {
    return flowNodes;
  }

  const lanes = new Map();
  for (const item of rankedNodes) {
    const lane = lanes.get(item.rank) || [];
    lane.push(item.node);
    lane.sort(compareNodesForLane);
    lanes.set(item.rank, lane);
  }

  const rankGap = vertical ? NODE_HEIGHT + 185 : NODE_WIDTH + 210;
  const laneGap = vertical ? NODE_WIDTH + 92 : NODE_HEIGHT + 84;
  const stackRankGap = vertical ? NODE_HEIGHT + 86 : NODE_WIDTH + 96;
  const rankSign = reverse ? -1 : 1;

  return flowNodes.map((node) => {
    const rank = Number(node.data.attrs.focus_rank ?? node.data.attrs.distance);
    const lane = lanes.get(rank) || [node];
    const laneIndex = lane.findIndex((item) => item.id === node.id);
    const stackSize = laneStackSize(lane.length);
    const stackIndex = Math.floor(laneIndex / stackSize);
    const indexInStack = laneIndex % stackSize;
    const stackCount = Math.ceil(lane.length / stackSize);
    const itemsInStack = Math.min(stackSize, lane.length - stackIndex * stackSize);
    const centeredLane = indexInStack - (itemsInStack - 1) / 2;
    const centeredStack = stackIndex - (stackCount - 1) / 2;
    const stackOffset = rank === 0 ? 0 : Math.abs(centeredStack) * stackRankGap * Math.sign(rank || 1);
    const laneOffset = stackCount > 1 ? centeredStack * 18 : 0;
    return {
      ...node,
      position: {
        ...node.position,
        [rankAttr]: (rank * rankGap + stackOffset) * rankSign,
        [laneAttr]: centeredLane * laneGap + laneOffset,
      },
    };
  });
}

function laneStackSize(count) {
  if (count <= 5) {
    return count || 1;
  }
  if (count <= 10) {
    return 5;
  }
  return 4;
}

function handlesForVisibleEdge(edge, nodeById, layoutDirection) {
  if (edge.source === edge.target || edge.attrs?.route === "loop") {
    return { sourceHandle: "source-bottom", targetHandle: "target-top" };
  }

  const source = nodeById.get(edge.source);
  const target = nodeById.get(edge.target);
  const sourceRank = Number(source?.data.attrs?.focus_rank ?? source?.data.attrs?.distance ?? 0);
  const targetRank = Number(target?.data.attrs?.focus_rank ?? target?.data.attrs?.distance ?? 0);
  const delta = targetRank - sourceRank;
  const forwardInView = delta >= 0;

  if (layoutDirection === "DOWN") {
    return forwardInView
      ? { sourceHandle: "source-bottom", targetHandle: "target-top" }
      : { sourceHandle: "source-top", targetHandle: "target-bottom" };
  }
  if (layoutDirection === "UP") {
    return forwardInView
      ? { sourceHandle: "source-top", targetHandle: "target-bottom" }
      : { sourceHandle: "source-bottom", targetHandle: "target-top" };
  }
  if (layoutDirection === "LEFT") {
    return forwardInView
      ? { sourceHandle: "source-left", targetHandle: "target-right" }
      : { sourceHandle: "source-right", targetHandle: "target-left" };
  }
  return forwardInView
    ? { sourceHandle: "source-right", targetHandle: "target-left" }
    : { sourceHandle: "source-left", targetHandle: "target-right" };
}

function onNodeClick({ node }) {
  emit("update:focusId", node.id);
  emit("element-selected", {
    type: "node",
    id: node.id,
    label: node.data.label,
    kind: node.data.kind,
    ref: node.data.ref,
    attrs: node.data.attrs,
  });
}

function onEdgeClick({ edge }) {
  emit("element-selected", {
    type: "edge",
    id: edge.id,
    label: edge.label || edge.data?.original_label || "(unlabelled)",
    kind: edge.class?.match(/k-edge--([a-z-]+)/)?.[1] || "edge",
    source: edge.source,
    target: edge.target,
    attrs: edge.data || {},
  });
}

function buildNeighborhoodGraph(sourceGraph, focusId, radius) {
  if (!focusId) {
    return sourceGraph;
  }

  const unlimited = radius === Infinity;
  const depth = shortestUndirectedDepths(sourceGraph, focusId);
  const visibleIds = new Set(sourceGraph.nodes
    .filter((node) => unlimited || (depth.get(node.id) ?? Infinity) <= radius)
    .map((node) => node.id));
  const ranks = focusRanks(sourceGraph, focusId, depth);
  const nodes = sourceGraph.nodes
    .filter((node) => visibleIds.has(node.id))
    .map((node) => {
      const focusDepth = depth.get(node.id) ?? 0;
      return {
        ...node,
        attrs: {
          ...node.attrs,
          focus_depth: focusDepth,
          focus_rank: ranks.get(node.id) ?? focusDepth,
          focus_current: node.id === focusId,
          focus_frontier: !unlimited && focusDepth === radius,
        },
      };
    });
  const edges = sourceGraph.edges.filter((edge) => {
    if (!visibleIds.has(edge.source) || !visibleIds.has(edge.target)) {
      return false;
    }
    if (unlimited) {
      return true;
    }
    if (edge.source === edge.target || edge.attrs?.route === "loop") {
      return (depth.get(edge.source) ?? Infinity) <= radius;
    }
    const sourceDepth = depth.get(edge.source) ?? Infinity;
    const targetDepth = depth.get(edge.target) ?? Infinity;
    return Math.min(sourceDepth, targetDepth) <= Math.max(0, radius - 1);
  });

  return {
    ...sourceGraph,
    graph_id: `${sourceGraph.graph_id}#focus=${focusId}`,
    nodes,
    edges,
  };
}

function preferredFocusNode(sourceGraph) {
  return sourceGraph.nodes.find((node) => Number(node.attrs?.distance) === 0) || sourceGraph.nodes[0];
}

function shortestUndirectedDepths(sourceGraph, focusId) {
  const adjacency = new Map(sourceGraph.nodes.map((node) => [node.id, []]));
  for (const edge of sourceGraph.edges) {
    adjacency.get(edge.source)?.push(edge.target);
    adjacency.get(edge.target)?.push(edge.source);
  }
  const depth = new Map([[focusId, 0]]);
  const queue = [focusId];
  for (let index = 0; index < queue.length; index += 1) {
    const nodeId = queue[index];
    const nextDepth = depth.get(nodeId) + 1;
    for (const nextId of adjacency.get(nodeId) || []) {
      if (!depth.has(nextId)) {
        depth.set(nextId, nextDepth);
        queue.push(nextId);
      }
    }
  }
  return depth;
}

function focusRanks(sourceGraph, focusId, depth) {
  const outgoing = directedDepths(sourceGraph, focusId, "out");
  const incoming = directedDepths(sourceGraph, focusId, "in");
  const ranks = new Map();

  for (const node of sourceGraph.nodes) {
    if (node.id === focusId) {
      ranks.set(node.id, 0);
      continue;
    }
    const outDepth = outgoing.get(node.id);
    const inDepth = incoming.get(node.id);
    if (Number.isFinite(outDepth) && (!Number.isFinite(inDepth) || outDepth <= inDepth)) {
      ranks.set(node.id, outDepth);
      continue;
    }
    if (Number.isFinite(inDepth)) {
      ranks.set(node.id, -inDepth);
      continue;
    }
    ranks.set(node.id, depth.get(node.id) ?? 0);
  }

  return ranks;
}

function directedDepths(sourceGraph, focusId, directionKind) {
  const adjacency = new Map(sourceGraph.nodes.map((node) => [node.id, []]));
  for (const edge of sourceGraph.edges) {
    if (directionKind === "out") {
      adjacency.get(edge.source)?.push(edge.target);
    } else {
      adjacency.get(edge.target)?.push(edge.source);
    }
  }
  const depth = new Map([[focusId, 0]]);
  const queue = [focusId];
  for (let index = 0; index < queue.length; index += 1) {
    const nodeId = queue[index];
    const nextDepth = depth.get(nodeId) + 1;
    for (const nextId of adjacency.get(nodeId) || []) {
      if (!depth.has(nextId)) {
        depth.set(nextId, nextDepth);
        queue.push(nextId);
      }
    }
  }
  return depth;
}

function compareNodesForLane(a, b) {
  const distanceA = Number(a.data.attrs?.distance ?? 0);
  const distanceB = Number(b.data.attrs?.distance ?? 0);
  if (distanceA !== distanceB) {
    return distanceA - distanceB;
  }
  return a.data.label.localeCompare(b.data.label);
}
</script>
