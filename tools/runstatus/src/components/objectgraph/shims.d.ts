// edge-label-position.js and use-animated-edge-coordinates.js are copied
// unmodified from the two merged W5.0 viewer prototypes
// (docs/proposals/project-object-graph/viewer/src/graph/ and the former
// .artifacts/graph-viewer-library-research/ Vue Flow + ELK mockup) —
// deliberately kept as plain JS rather than ported to strict TS, which would
// be a large, low-value rewrite of visual/layout code with no runtime
// behavior change. This shim keeps vue-tsc's project-wide --build from
// choking on their untyped internals without loosening typing anywhere else
// in the app. The sibling .vue files (GraphCanvas/GraphNode/CycleEdge/
// SpokeEdge) are ALSO plain-JS SFCs for the same reason, but vue-tsc's .vue
// module resolution doesn't honor ambient `declare module` overrides the way
// it does for .js — importers of those use an inline @ts-expect-error
// instead (see ObjectGraphPage.vue).
declare module "./edge-label-position.js" {
  export function labelStyle(...args: unknown[]): Record<string, unknown>;
}
declare module "./use-animated-edge-coordinates.js" {
  export function useAnimatedEdgeCoordinates(...args: unknown[]): unknown;
}
