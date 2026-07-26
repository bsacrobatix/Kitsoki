import type {
  ApplicationAction,
  ApplicationElement,
  ApplicationFrame,
  ApplicationSemanticNode,
} from "./types.js";

export const APPLICATION_SEMANTIC_PLUGIN = "kitsoki.application";

export interface ApplicationSemanticElement {
  readonly plugin: typeof APPLICATION_SEMANTIC_PLUGIN;
  readonly ref: string;
  readonly semantic_kind: string;
  readonly label: string;
  readonly description: string;
  readonly data: {
    readonly application_id: string;
    readonly frame_revision: number;
    readonly program_node?: string;
    readonly story?: string;
    readonly member?: string;
  };
}

export interface ApplicationSemanticInspection {
  readonly kind: "semantic_element";
  readonly semantic_element: ApplicationSemanticElement;
  readonly element: HTMLElement;
}

export function semanticDataAttributes(
  semantic: ApplicationSemanticNode | undefined
): Record<string, string> {
  if (!semantic) {
    return {};
  }
  return {
    "data-semantic-ref": semantic.ref,
    "data-semantic-kind": semantic.kind,
    "data-semantic-name": semantic.name,
    "data-semantic-description": semantic.description,
    ...(semantic.source.program_node ? { "data-program-node": semantic.source.program_node } : {}),
    ...(semantic.source?.story ? { "data-source-story": semantic.source.story } : {}),
    ...(semantic.source?.member ? { "data-source-member": semantic.source.member } : {}),
  };
}

function findActionSemantic(
  actions: readonly ApplicationAction[] | undefined,
  ref: string
): ApplicationSemanticNode | undefined {
  return actions?.find((action) => action.semantic.ref === ref)?.semantic;
}

function findElementSemantic(
  elements: readonly ApplicationElement[] | undefined,
  ref: string
): ApplicationSemanticNode | undefined {
  for (const element of elements ?? []) {
    if (element.semantic?.ref === ref) return element.semantic;
    const action = findActionSemantic(element.actions, ref);
    if (action) return action;
    const child = findElementSemantic(element.items, ref);
    if (child) return child;
  }
  return undefined;
}

export function findApplicationSemanticNode(
  frame: ApplicationFrame,
  ref: string
): ApplicationSemanticNode | undefined {
  if (frame.semantic.ref === ref) return frame.semantic;
  if (frame.page_semantic.ref === ref) return frame.page_semantic;
  for (const page of frame.pages ?? []) {
    if (page.semantic.ref === ref) return page.semantic;
  }
  for (const component of frame.components ?? []) {
    if (component.semantic.ref === ref) return component.semantic;
  }
  for (const handler of frame.handlers ?? []) {
    if (handler.semantic.ref === ref) return handler.semantic;
  }
  const frameAction = findActionSemantic(frame.actions, ref);
  if (frameAction) return frameAction;
  for (const nav of frame.navigation ?? []) {
    if (nav.semantic.ref === ref) return nav.semantic;
  }
  for (const region of frame.regions ?? []) {
    if (region.semantic.ref === ref) return region.semantic;
    for (const card of region.cards ?? []) {
      if (card.semantic.ref === ref) return card.semantic;
      const body = findElementSemantic(card.body, ref);
      if (body) return body;
      const action = findActionSemantic(card.actions, ref);
      if (action) return action;
    }
  }
  return undefined;
}

function targetElement(target: EventTarget | null): HTMLElement | null {
  if (target instanceof HTMLElement) return target;
  if (target instanceof Node) return target.parentElement;
  return null;
}

/**
 * Resolve the nearest rendered semantic root. Canonical names, descriptions,
 * provenance, and graph identity come from the frame when it is available;
 * data attributes are a deterministic fallback for detached developer tools.
 */
export function inspectApplicationSemanticElement(
  target: EventTarget | null,
  frame: ApplicationFrame
): ApplicationSemanticInspection | null {
  const element = targetElement(target)?.closest<HTMLElement>("[data-semantic-ref]");
  if (!element) return null;

  const ref = element.dataset.semanticRef;
  if (!ref) return null;
  const semantic = findApplicationSemanticNode(frame, ref);
  const semanticKind = semantic?.kind ?? element.dataset.semanticKind;
  const label = semantic?.name ?? element.dataset.semanticName;
  const description = semantic?.description ?? element.dataset.semanticDescription;
  if (!semanticKind || !label || !description) return null;

  return {
    kind: "semantic_element",
    semantic_element: {
      plugin: APPLICATION_SEMANTIC_PLUGIN,
      ref,
      semantic_kind: semanticKind,
      label,
      description,
      data: {
        application_id: frame.application_id,
        frame_revision: frame.revision,
        ...(semantic?.source.program_node || element.dataset.programNode
          ? { program_node: semantic?.source.program_node ?? element.dataset.programNode }
          : {}),
        ...(semantic?.source?.story || element.dataset.sourceStory
          ? { story: semantic?.source?.story ?? element.dataset.sourceStory }
          : {}),
        ...(semantic?.source?.member || element.dataset.sourceMember
          ? { member: semantic?.source?.member ?? element.dataset.sourceMember }
          : {}),
      },
    },
    element,
  };
}
