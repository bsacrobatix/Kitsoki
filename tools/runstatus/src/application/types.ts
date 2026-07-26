import type { Component } from "vue";

export const APPLICATION_FRAME_SCHEMA = "application-frame/v1" as const;

export type JSONPrimitive = string | number | boolean | null;
export type JSONValue = JSONPrimitive | { readonly [key: string]: JSONValue } | readonly JSONValue[];

export type ApplicationSemanticKind =
  | "application"
  | "navigation"
  | "page"
  | "region"
  | "card"
  | "component"
  | "field"
  | "action"
  | "status"
  | "artifact"
  | "handler";

export interface ApplicationProvenance {
  readonly story: string;
  readonly member: string;
  readonly program_node?: string;
  readonly generated?: boolean;
}

export interface ApplicationSemanticRelationship {
  readonly kind: string;
  readonly ref: string;
}

export interface ApplicationSemanticNode {
  readonly ref: string;
  readonly kind: ApplicationSemanticKind;
  readonly name: string;
  readonly description: string;
  readonly source: ApplicationProvenance;
  readonly relationships?: readonly ApplicationSemanticRelationship[];
}

export interface ApplicationNodeState {
  readonly visible?: boolean;
  readonly enabled?: boolean;
  readonly selected?: boolean;
  readonly validation?: string;
  readonly values?: Readonly<Record<string, string>>;
}

export interface ApplicationWorkflow {
  readonly state: string;
  readonly allowed_intents?: readonly string[];
  readonly budget_state?: string;
  readonly degradation?: string;
}

export interface ApplicationNavigationItem {
  readonly id: string;
  readonly page: string;
  readonly semantic: ApplicationSemanticNode;
  readonly state?: ApplicationNodeState;
}

export interface ApplicationPageDescriptor {
  readonly id: string;
  readonly semantic: ApplicationSemanticNode;
  readonly current?: boolean;
}

export interface ApplicationComponentDescriptor {
  readonly id: string;
  readonly semantic: ApplicationSemanticNode;
  readonly fallback?: string;
}

export interface ApplicationHandlerDescriptor {
  readonly id: string;
  readonly semantic: ApplicationSemanticNode;
}

export interface ApplicationAction {
  readonly id: string;
  readonly handler?: string;
  readonly intent?: string;
  readonly target_state?: string;
  readonly routing_mode?: "exact" | "synonym" | "semantic" | "llm" | "off";
  readonly input_schema?: JSONValue;
  readonly input_schema_ref?: string;
  readonly enabled: boolean;
  readonly semantic: ApplicationSemanticNode;
  readonly state?: ApplicationNodeState;
}

/**
 * Finite, serializable frame content. `props` and `value` deliberately remain
 * JSON: their precise shape belongs to the built-in or story component schema,
 * not to the presentation runtime.
 */
export interface ApplicationElement {
  readonly id?: string;
  readonly kind: string;
  readonly component?: string;
  readonly props?: JSONValue;
  readonly value?: JSONValue;
  readonly semantic?: ApplicationSemanticNode;
  readonly state?: ApplicationNodeState;
  readonly items?: readonly ApplicationElement[];
  readonly actions?: readonly ApplicationAction[];
}

export type ApplicationBody = ApplicationElement;
export type ApplicationComponentBody = ApplicationElement & {
  readonly kind: "component";
  readonly component: string;
};

export interface ApplicationCard {
  readonly id: string;
  readonly semantic: ApplicationSemanticNode;
  readonly body?: readonly ApplicationElement[];
  readonly actions?: readonly ApplicationAction[];
  readonly state?: ApplicationNodeState;
}

export interface ApplicationRegion {
  readonly id: string;
  readonly semantic: ApplicationSemanticNode;
  readonly cards?: readonly ApplicationCard[];
  readonly state?: ApplicationNodeState;
}

export interface ApplicationFrameError {
  readonly code: string;
  readonly message: string;
  readonly semantic_ref?: string;
}

export interface ApplicationCapabilities {
  readonly presentation?: readonly string[];
  readonly actions?: readonly string[];
}

export interface ApplicationFrameData {
  readonly value: JSONValue;
  readonly sensitivity: "public" | "internal" | "sensitive" | "secret";
  readonly policy: "include" | "redact" | "hash";
}

/** Exact TypeScript projection of internal/application.Frame's JSON contract. */
export interface ApplicationFrame {
  readonly schema: typeof APPLICATION_FRAME_SCHEMA;
  readonly application_id: string;
  readonly session_id: string;
  readonly revision: number;
  readonly page: string;
  readonly page_semantic: ApplicationSemanticNode;
  readonly semantic: ApplicationSemanticNode;
  readonly workflow: ApplicationWorkflow;
  readonly data?: Readonly<Record<string, ApplicationFrameData>>;
  readonly navigation?: readonly ApplicationNavigationItem[];
  readonly pages?: readonly ApplicationPageDescriptor[];
  readonly components?: readonly ApplicationComponentDescriptor[];
  readonly handlers?: readonly ApplicationHandlerDescriptor[];
  readonly actions?: readonly ApplicationAction[];
  readonly regions?: readonly ApplicationRegion[];
  readonly errors?: readonly ApplicationFrameError[];
  readonly capabilities?: ApplicationCapabilities;
}

export interface ApplicationActionEnvelope {
  readonly action: string;
  readonly input?: JSONValue;
  readonly session_id: string;
  readonly frame_revision: number;
  readonly actor?: string;
  readonly routing_mode?: "exact" | "synonym" | "semantic" | "llm" | "off";
  readonly idempotency_key?: string;
}

export interface ApplicationActionResult {
  readonly ok: boolean;
  readonly schema?: "application-outcome/v1";
  readonly handler?: string;
  readonly outcome?: string;
  readonly output?: JSONValue;
  readonly receipt?: ApplicationReceipt;
  readonly frame?: ApplicationFrame;
  readonly children?: readonly ApplicationChildRun[];
  readonly join?: ApplicationJoinState;
  readonly error?: string | ApplicationOutcomeError;
  readonly selected_implementor?: string;
}

export interface ApplicationChildRun {
  readonly id: string;
  readonly status: string;
  readonly outcome?: string;
}

export interface ApplicationJoinState {
  readonly status: string;
  readonly pending?: readonly string[];
  readonly completed?: readonly string[];
}

export interface ApplicationOutcomeError {
  readonly code: string;
  readonly message: string;
}

export interface ApplicationReceipt {
  readonly schema: "application-receipt/v1";
  readonly id: string;
  readonly handler_id: string;
  readonly semantic_ref: string;
  readonly session_id?: string;
  readonly actor?: string;
  readonly effect: string;
  readonly routing: JSONValue;
  readonly budget: JSONValue;
  readonly input_digest: string;
  readonly output_digest: string;
  readonly transport: string;
  readonly event_id?: string;
  readonly event_mode?: string;
  readonly frame_revision?: number;
  readonly outcome: string;
  readonly idempotency_key?: string;
  readonly replayed?: boolean;
  readonly replay_of?: string;
  readonly selected_implementor?: string;
}

export type ApplicationActionDispatcher = (
  envelope: ApplicationActionEnvelope
) => Promise<ApplicationActionResult | void> | ApplicationActionResult | void;

export interface ApplicationComponentProps {
  readonly frame: ApplicationFrame;
  readonly body: ApplicationComponentBody;
  readonly dispatch: ApplicationActionDispatcher;
}

export interface ApplicationComponentRegistration {
  readonly component?: Component;
  readonly fallback?: ApplicationElement;
}

export type ApplicationComponentRegistrySource = Readonly<
  Record<string, Component | ApplicationComponentRegistration>
>;
