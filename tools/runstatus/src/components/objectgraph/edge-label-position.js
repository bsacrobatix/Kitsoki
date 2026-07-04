const NODE_WIDTH = 210;
const NODE_HEIGHT = 78;
const LABEL_HEIGHT = 30;
const NODE_GUTTER = 36;

export function labelWidthFor(text) {
  return Math.max(86, String(text || "").length * 7.2 + 30);
}

export function labelStyle({ sourceX, sourceY, targetX, targetY, label, route, direction, nodes }) {
  const anchor = pickLabelAnchor({
    sourceX,
    sourceY,
    targetX,
    targetY,
    labelWidth: labelWidthFor(label),
    route,
    direction,
    nodes,
  });

  return {
    transform: `translate(-50%, -50%) translate(${anchor.x}px, ${anchor.y}px)`,
  };
}

function pickLabelAnchor({ sourceX, sourceY, targetX, targetY, labelWidth, route, direction, nodes }) {
  const dx = targetX - sourceX;
  const dy = targetY - sourceY;
  const length = Math.hypot(dx, dy) || 1;
  const ux = dx / length;
  const uy = dy / length;
  const px = -uy;
  const py = ux;
  const routeSign = route === "backtrack" ? -1 : 1;
  const directionSign = direction === "LEFT" || direction === "UP" ? -1 : 1;
  const side = routeSign * directionSign;
  const midpoint = { x: sourceX + dx * 0.5, y: sourceY + dy * 0.5 };
  const nodeRects = visibleNodeRects(nodes);
  const baseOffsets = [58, 86, 118, 154, 196, 248, 310];
  const alongOffsets = [0, -0.16, 0.16, -0.28, 0.28, -0.4, 0.4];
  const candidates = [];

  if (Math.abs(dx) < 6 && Math.abs(dy) < 6) {
    addRadialCandidates(candidates, midpoint, [92, 126, 168, 218, 278, 348, 432]);
  } else {
    for (const along of alongOffsets) {
      const center = {
        x: midpoint.x + ux * length * along,
        y: midpoint.y + uy * length * along,
      };
      for (const offset of baseOffsets) {
        candidates.push({
          x: center.x + px * offset * side,
          y: center.y + py * offset * side,
        });
        candidates.push({
          x: center.x - px * offset * side,
          y: center.y - py * offset * side,
        });
      }
    }
    addRadialCandidates(candidates, midpoint, [76, 112, 156, 214, 284]);
    addRadialCandidates(candidates, { x: sourceX + dx * 0.34, y: sourceY + dy * 0.34 }, [92, 146, 208]);
    addRadialCandidates(candidates, { x: sourceX + dx * 0.66, y: sourceY + dy * 0.66 }, [92, 146, 208]);
  }

  let best = candidates[0] || midpoint;
  let bestScore = Infinity;
  for (const candidate of candidates) {
    const rect = labelRect(candidate, labelWidth);
    const score = nodeRects.reduce((sum, nodeRect) => sum + overlapArea(rect, nodeRect), 0);
    if (score === 0) {
      return candidate;
    }
    if (score < bestScore) {
      best = candidate;
      bestScore = score;
    }
  }
  return best;
}

function addRadialCandidates(candidates, center, radii) {
  const angles = [
    -90, 90, -45, 45, -135, 135, 0, 180,
    -22, 22, -68, 68, -112, 112, -158, 158,
  ];
  for (const radius of radii) {
    for (const angle of angles) {
      const radians = angle * Math.PI / 180;
      candidates.push({
        x: center.x + Math.cos(radians) * radius,
        y: center.y + Math.sin(radians) * radius,
      });
    }
  }
}

function visibleNodeRects(nodes) {
  const list = Array.isArray(nodes?.value) ? nodes.value : Array.isArray(nodes) ? nodes : [];
  return list
    .filter((node) => !node.hidden)
    .map((node) => {
      const width = node.dimensions?.width || node.width || NODE_WIDTH;
      const height = node.dimensions?.height || node.height || NODE_HEIGHT;
      return {
        left: node.position.x - NODE_GUTTER,
        top: node.position.y - NODE_GUTTER,
        right: node.position.x + width + NODE_GUTTER,
        bottom: node.position.y + height + NODE_GUTTER,
      };
    });
}

function labelRect(anchor, width) {
  return {
    left: anchor.x - width / 2,
    top: anchor.y - LABEL_HEIGHT / 2,
    right: anchor.x + width / 2,
    bottom: anchor.y + LABEL_HEIGHT / 2,
  };
}

function overlapArea(a, b) {
  const width = Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left));
  const height = Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top));
  return width * height;
}
