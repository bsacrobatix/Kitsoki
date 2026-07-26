import type {
  ApplicationAction,
  ApplicationActionEnvelope,
  ApplicationFrame,
} from "./types.js";

export interface NativeSurfaceDeclaration {
  readonly reuse?: string;
  readonly projection?: string;
  readonly commands?: readonly string[];
}

export interface VSCodeCommandProjection {
  readonly id: string;
  readonly name: string;
  readonly description: string;
  readonly semantic_ref: string;
  readonly envelope: ApplicationActionEnvelope;
}

export interface VSCodeNativeProjection {
  readonly reuse?: string;
  readonly commands: readonly VSCodeCommandProjection[];
}

export function projectVSCodeNative(
  frame: ApplicationFrame,
  surface: NativeSurfaceDeclaration = {}
): VSCodeNativeProjection {
  const actions = allActions(frame);
  return {
    reuse: surface.reuse,
    commands: (surface.commands ?? []).map((id) => {
      const action = actions.get(id);
      if (!action) throw new Error(`VS Code command ${id} has no frame action`);
      return {
        id,
        name: action.semantic.name,
        description: action.semantic.description,
        semantic_ref: action.semantic.ref,
        envelope: {
          action: action.id,
          input: {},
          session_id: frame.session_id,
          frame_revision: frame.revision,
          routing_mode: action.routing_mode,
        },
      };
    }),
  };
}

function allActions(frame: ApplicationFrame): Map<string, ApplicationAction> {
  const actions = new Map<string, ApplicationAction>();
  for (const action of frame.actions ?? []) actions.set(action.id, action);
  for (const region of frame.regions ?? []) {
    for (const card of region.cards ?? []) {
      for (const action of card.actions ?? []) actions.set(action.id, action);
      for (const body of card.body ?? []) {
        collectElementActions(body, actions);
      }
    }
  }
  return actions;
}

function collectElementActions(
  body: import("./types.js").ApplicationElement,
  actions: Map<string, ApplicationAction>
): void {
  for (const action of body.actions ?? []) actions.set(action.id, action);
  for (const child of body.items ?? []) collectElementActions(child, actions);
}
