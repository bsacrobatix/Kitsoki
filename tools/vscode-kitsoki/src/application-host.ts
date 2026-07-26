export interface Disposable {
  dispose(): void;
}

export interface ApplicationBackend {
  rpc<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
}

export type CommandRegistrar = (
  id: string,
  callback: (input?: unknown) => Promise<void>
) => Disposable;

export type CommandInputProvider = (
  command: RegisteredApplicationCommand
) => Promise<Record<string, unknown> | undefined>;

export type CurrentSessionSubscriber = (
  onChange: (sessionId: string | null) => void
) => Disposable;

interface ApplicationAction {
  readonly id: string;
  readonly routing_mode?: string;
  readonly input_schema?: Record<string, unknown>;
  readonly input_schema_ref?: string;
  readonly semantic: {
    readonly ref: string;
    readonly name: string;
    readonly description: string;
  };
}

interface ApplicationElement {
  readonly actions?: readonly ApplicationAction[];
  readonly items?: readonly ApplicationElement[];
}

interface ApplicationFrame {
  readonly session_id: string;
  readonly revision: number;
  readonly page: string;
  readonly actions?: readonly ApplicationAction[];
  readonly regions?: readonly {
    readonly cards?: readonly {
      readonly actions?: readonly ApplicationAction[];
      readonly body?: readonly ApplicationElement[];
    }[];
  }[];
}

interface ApplicationDefinition {
  readonly app?: { readonly id?: string };
  readonly Application?: {
    readonly surfaces?: {
      readonly vscode?: {
        readonly reuse?: string;
        readonly native?: {
          readonly commands?: readonly string[];
        };
      };
    };
  };
  readonly application?: ApplicationDefinition["Application"];
}

export interface ApplicationBundle {
  readonly schema: "application-bundle/v1";
  readonly application_id: string;
  readonly digest: string;
  readonly entry: string;
  readonly files: readonly string[];
  readonly entryURL: string;
}

// applicationBundleFrameHTML mounts the backend-served production bundle in a
// VS Code-owned panel. The iframe keeps the bundle on its backend origin, so
// its existing JSON-RPC/SSE transport works without copying assets or weakening
// the main extension SPA's CSP.
export function applicationBundleFrameHTML(bundle: ApplicationBundle): string {
  const entry = new URL(bundle.entryURL);
  if (entry.protocol !== "http:" && entry.protocol !== "https:") {
    throw new Error(`unsupported application bundle URL protocol ${entry.protocol}`);
  }
  const origin = entry.origin.replace(/&/g, "&amp;").replace(/"/g, "&quot;");
  const source = entry.href.replace(/&/g, "&amp;").replace(/"/g, "&quot;");
  return `<!doctype html>
<html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; frame-src ${origin}; style-src 'unsafe-inline';">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>html,body,iframe{border:0;height:100%;margin:0;padding:0;width:100%;overflow:hidden}</style>
</head><body><iframe src="${source}" title="Kitsoki application"></iframe></body></html>`;
}

export interface RegisteredApplicationCommand {
  readonly id: string;
  readonly name: string;
  readonly description: string;
  readonly semanticRef: string;
  readonly inputSchema?: Record<string, unknown>;
  readonly inputSchemaRef?: string;
  readonly envelope: {
    readonly action: string;
    readonly input: Record<string, unknown>;
    readonly session_id: string;
    readonly frame_revision: number;
    readonly routing_mode?: string;
  };
}

export interface ApplicationFeedbackSubmission {
  readonly report: {
    readonly anchor: {
      readonly kind: "semantic_element";
      readonly semantic_element: {
        readonly plugin: "kitsoki.application";
        readonly ref: string;
        readonly data: Readonly<Record<string, unknown>>;
      };
    };
  };
  readonly receipt: { readonly ref: string; readonly deduped: boolean };
}

export interface ApplicationFeedbackTarget {
  readonly sessionId: string;
  readonly ref: string;
  readonly name: string;
  readonly description: string;
  readonly current: boolean;
}

export type ApplicationFeedbackTargetSelector = (
  targets: readonly ApplicationFeedbackTarget[],
) => Promise<ApplicationFeedbackTarget | undefined>;

export type ApplicationFeedbackInstructionProvider = (
  target: ApplicationFeedbackTarget,
) => Promise<string | undefined>;

export async function reportApplicationFeedback(
  host: ApplicationCommandHost,
  selectTarget: ApplicationFeedbackTargetSelector,
  requestInstruction: ApplicationFeedbackInstructionProvider,
): Promise<ApplicationFeedbackSubmission | undefined> {
  const target = await selectTarget(host.feedbackTargets());
  if (!target) return undefined;
  const instruction = (await requestInstruction(target))?.trim();
  if (!instruction) return undefined;
  return host.submitFeedback(target.sessionId, target.ref, instruction);
}

export class ApplicationCommandHost implements Disposable {
  private registrations: Disposable[] = [];
  private commands: RegisteredApplicationCommand[] = [];
  private currentSessionSubscription: Disposable | undefined;
  private refreshSequence = Promise.resolve();
  private applicationBundle: ApplicationBundle | undefined;
  private focusedSemanticRef = "";

  constructor(
    private readonly backend: ApplicationBackend,
    private readonly register: CommandRegistrar,
    private readonly onError: (message: string) => void = () => {},
    private readonly inputProvider: CommandInputProvider = async () => ({}),
    private readonly loadBundle?: (applicationId: string) => Promise<ApplicationBundle>,
  ) {}

  descriptors(): readonly RegisteredApplicationCommand[] {
    return this.commands;
  }

  bundle(): ApplicationBundle | undefined {
    return this.applicationBundle;
  }

  feedbackTargets(): readonly ApplicationFeedbackTarget[] {
    return this.commands
      .map((command) => ({
        sessionId: command.envelope.session_id,
        ref: command.semanticRef,
        name: command.name,
        description: command.description,
        current: command.semanticRef === this.focusedSemanticRef,
      }))
      .sort((left, right) => Number(right.current) - Number(left.current));
  }

  watchCurrentSession(subscribe: CurrentSessionSubscriber): void {
    this.currentSessionSubscription?.dispose();
    this.currentSessionSubscription = subscribe((sessionId) => {
      this.refreshSequence = this.refreshSequence
        .then(() => this.refresh(sessionId))
        .catch((error) => this.onError(`refresh application commands: ${(error as Error).message}`));
    });
  }

  async refreshCurrent(): Promise<void> {
    const current = await this.backend.rpc<{ session_id: string | null }>(
      "runstatus.session.current",
      {},
    );
    await this.refresh(current.session_id);
  }

  submitFeedback(
    sessionId: string,
    ref: string,
    instruction: string,
    kind = "bug",
    idempotencyKey = "",
  ): Promise<ApplicationFeedbackSubmission> {
    return this.backend.rpc<ApplicationFeedbackSubmission>("runstatus.application.feedback", {
      session_id: sessionId,
      ref,
      instruction,
      kind,
      ...(idempotencyKey ? { idempotency_key: idempotencyKey } : {}),
    });
  }

  async refresh(sessionId: string | null): Promise<void> {
    this.clear();
    if (!sessionId) return;
    const [definition, frame] = await Promise.all([
      this.backend.rpc<ApplicationDefinition>("runstatus.session.app", { session_id: sessionId }),
      this.backend.rpc<ApplicationFrame>("runstatus.application.frame", { session_id: sessionId }),
    ]);
    const application = definition.application ?? definition.Application;
    const applicationID = definition.app?.id;
    if (applicationID && application?.surfaces?.vscode?.reuse === "web" && this.loadBundle) {
      try {
        this.applicationBundle = await this.loadBundle(applicationID);
      } catch (error) {
        this.onError(`load application bundle: ${(error as Error).message}`);
      }
    }
    const declared = application?.surfaces?.vscode?.native?.commands ?? [];
    const actions = collectActions(frame);

    this.commands = declared.map((id) => {
      const action = actions.get(id);
      if (!action) throw new Error(`VS Code command ${id} has no frame action`);
      return {
        id,
        name: action.semantic.name,
        description: action.semantic.description,
        semanticRef: action.semantic.ref,
        ...(action.input_schema ? { inputSchema: action.input_schema } : {}),
        ...(action.input_schema_ref ? { inputSchemaRef: action.input_schema_ref } : {}),
        envelope: {
          action: action.id,
          input: {},
          session_id: frame.session_id,
          frame_revision: frame.revision,
          ...(action.routing_mode ? { routing_mode: action.routing_mode } : {}),
        },
      };
    });
    for (const command of this.commands) {
      this.registrations.push(this.register(command.id, async (providedInput) => {
        try {
          this.focusedSemanticRef = command.semanticRef;
          const input = isRecord(providedInput)
            ? providedInput
            : await this.inputProvider(command);
          if (input === undefined) return;
          await this.backend.rpc("runstatus.application.vscode_action", {
            ...command.envelope,
            input,
            page: frame.page,
          });
          await this.refresh(command.envelope.session_id);
        } catch (error) {
          this.onError(`${command.name}: ${(error as Error).message}`);
        }
      }));
    }
  }

  dispose(): void {
    this.currentSessionSubscription?.dispose();
    this.currentSessionSubscription = undefined;
    this.clear();
  }

  private clear(): void {
    for (const registration of this.registrations) registration.dispose();
    this.registrations = [];
    this.commands = [];
    this.applicationBundle = undefined;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function collectActions(frame: ApplicationFrame): Map<string, ApplicationAction> {
  const actions = new Map<string, ApplicationAction>();
  for (const action of frame.actions ?? []) actions.set(action.id, action);
  for (const region of frame.regions ?? []) {
    for (const card of region.cards ?? []) {
      for (const action of card.actions ?? []) actions.set(action.id, action);
      for (const body of card.body ?? []) {
        collectBodyActions(body, actions);
      }
    }
  }
  return actions;
}

function collectBodyActions(body: ApplicationElement, actions: Map<string, ApplicationAction>): void {
  for (const action of body.actions ?? []) actions.set(action.id, action);
  for (const child of body.items ?? []) collectBodyActions(child, actions);
}
