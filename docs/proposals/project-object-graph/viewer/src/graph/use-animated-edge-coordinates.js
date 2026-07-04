import { onBeforeUnmount, reactive, watch } from "vue";

const EDGE_MOTION_MS = 380;

export function useAnimatedEdgeCoordinates(props) {
  const coords = reactive({
    sourceX: props.sourceX,
    sourceY: props.sourceY,
    targetX: props.targetX,
    targetY: props.targetY,
  });
  let frame = 0;

  watch(
    () => [props.sourceX, props.sourceY, props.targetX, props.targetY],
    ([sourceX, sourceY, targetX, targetY], oldValues) => {
      if (!oldValues) {
        coords.sourceX = sourceX;
        coords.sourceY = sourceY;
        coords.targetX = targetX;
        coords.targetY = targetY;
        return;
      }

      cancelAnimationFrame(frame);
      const start = {
        sourceX: coords.sourceX,
        sourceY: coords.sourceY,
        targetX: coords.targetX,
        targetY: coords.targetY,
      };
      const end = { sourceX, sourceY, targetX, targetY };
      const startedAt = performance.now();

      const tick = (now) => {
        const progress = Math.min(1, (now - startedAt) / EDGE_MOTION_MS);
        const eased = easeOutCubic(progress);
        coords.sourceX = interpolate(start.sourceX, end.sourceX, eased);
        coords.sourceY = interpolate(start.sourceY, end.sourceY, eased);
        coords.targetX = interpolate(start.targetX, end.targetX, eased);
        coords.targetY = interpolate(start.targetY, end.targetY, eased);
        if (progress < 1) {
          frame = requestAnimationFrame(tick);
        }
      };

      frame = requestAnimationFrame(tick);
    },
    { immediate: true },
  );

  onBeforeUnmount(() => cancelAnimationFrame(frame));
  return coords;
}

function interpolate(start, end, progress) {
  return start + (end - start) * progress;
}

function easeOutCubic(progress) {
  return 1 - Math.pow(1 - progress, 3);
}
