// Pluggable Cytoscape layout registry for the project object graph views
// (GraphView.vue). Adding a new layout is one entry here plus a
// `cytoscape.use(...)` registration — no changes needed in GraphView itself
// or its callers (CatalogPanel's relationship graph, the full-graph modal).
import cytoscape from "cytoscape";
import cola from "cytoscape-cola";
import fcose from "cytoscape-fcose";
import klay from "cytoscape-klay";

cytoscape.use(fcose);
cytoscape.use(cola);
cytoscape.use(klay);

// fcose/cola/klay are third-party layout extensions with option shapes
// cytoscape's own (closed) LayoutOptions union doesn't know about; callers
// pass this straight to `cy.layout(...)`.
export type PluginLayoutOptions = { name: string } & Record<string, unknown>;

export interface GraphLayout {
  id: string;
  label: string;
  // `compound` layouts additionally honor a node's `data.parent` (group)
  // relationship — used by the full-graph view's "group by layer" mode.
  compound?: boolean;
  options: PluginLayoutOptions;
}

export const layouts: GraphLayout[] = [
  {
    id: "fcose",
    label: "fCoSE (force-directed)",
    compound: true,
    options: {
      name: "fcose",
      animate: false,
      quality: "proof",
      nodeSeparation: 90,
      idealEdgeLength: 120,
    },
  },
  {
    id: "cola",
    label: "Cola",
    options: {
      name: "cola",
      animate: false,
      nodeSpacing: 24,
      edgeLength: 140,
      handleDisconnected: true,
    },
  },
  {
    id: "cola-compound",
    label: "Cola (grouped)",
    compound: true,
    options: {
      name: "cola",
      animate: false,
      nodeSpacing: 28,
      edgeLength: 160,
      handleDisconnected: true,
      alignment: undefined,
    },
  },
  {
    id: "klay",
    label: "Klay (layered)",
    options: {
      name: "klay",
      klay: {
        direction: "RIGHT",
        spacing: 40,
        thoroughness: 7,
      },
    },
  },
  {
    id: "breadthfirst",
    label: "Breadth-first (focus radial)",
    options: {
      name: "breadthfirst",
      animate: false,
      spacingFactor: 1.4,
      circle: true,
    },
  },
];

export const defaultLayoutId = "fcose";

export function findLayout(id: string): GraphLayout {
  return layouts.find((layout) => layout.id === id) ?? layouts[0];
}
