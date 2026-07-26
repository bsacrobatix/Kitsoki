import { buildSessionEnvelope, snapshotSessionEvents, type RrwebEnvelope } from "../data/session-capture.js";
import type {
  ApplicationActionEnvelope,
  ApplicationActionResult,
  ApplicationFrame,
} from "./types.js";

export interface ApplicationCaptureRequest {
  readonly schema: "kitsoki/application-capture/v1";
  readonly request_id: string;
  readonly app_id: string;
  readonly session_id: string;
  readonly revision: number;
  readonly scenario_ref: string;
  readonly action_ids: readonly string[];
}

export interface ApplicationCaptureRPC {
  post(method: string, params: Record<string, unknown>): Promise<unknown>;
}

export interface ApplicationCaptureDependencies {
  readonly sessionId: string;
  readonly rpc: ApplicationCaptureRPC;
  readonly frame: () => ApplicationFrame | null;
  readonly dispatch: (envelope: ApplicationActionEnvelope) => Promise<ApplicationActionResult | void>;
  readonly recording?: () => RrwebEnvelope;
  readonly openStream: (subscriptionId: string, onMessage: (raw: string) => void) => () => void;
}

export interface ApplicationCaptureController {
  attach(): Promise<void>;
  dispose(): void;
}

export function startApplicationCaptureClient(
  deps: ApplicationCaptureDependencies,
): ApplicationCaptureController {
  let subscriptionId = "";
  let closeStream: (() => void) | null = null;
  let disposed = false;
  let attaching: Promise<void> | null = null;
  const processing = new Set<string>();

  const attach = async (): Promise<void> => {
    if (disposed || attaching) return attaching ?? Promise.resolve();
    const frame = deps.frame();
    if (!frame || frame.session_id !== deps.sessionId) return;
    attaching = (async () => {
      if (!subscriptionId) {
        const subscribed = await deps.rpc.post(
          "runstatus.application.capture.subscribe",
          { session_id: deps.sessionId },
        ) as { subscription_id: string };
        if (disposed) return;
        subscriptionId = subscribed.subscription_id;
        closeStream = deps.openStream(subscriptionId, onMessage);
        return;
      }
      await deps.rpc.post("runstatus.application.capture.attach", {
        session_id: deps.sessionId,
      });
    })().finally(() => {
      attaching = null;
    });
    return attaching;
  };

  const onMessage = (raw: string): void => {
    try {
      const message = JSON.parse(raw) as {
        method?: string;
        params?: ApplicationCaptureRequest;
      };
      if (message.method !== "runstatus.application.capture.request" || !message.params) return;
      void execute(message.params);
    } catch {
      // Malformed or unrelated SSE frames do not affect the application.
    }
  };

  const execute = async (request: ApplicationCaptureRequest): Promise<void> => {
    if (disposed || processing.has(request.request_id)) return;
    const initial = deps.frame();
    if (
      !initial ||
      request.app_id !== initial.application_id ||
      request.session_id !== initial.session_id ||
      request.action_ids.length === 0
    ) return;
    if (request.revision !== initial.revision) {
      await deps.rpc.post("runstatus.application.capture.fail", {
        session_id: request.session_id,
        request_id: request.request_id,
        app_id: request.app_id,
        revision: request.revision,
        reason: "application_surface_revision_changed",
        receipts: [],
      });
      return;
    }
    processing.add(request.request_id);
    const receipts: Array<{
      index: number;
      action_id: string;
      receipt_id: string;
      handler_id: string;
      semantic_ref: string;
      idempotency_key: string;
      frame_revision: number;
    }> = [];
    try {
      const fail = async (reason: string): Promise<void> => {
        await deps.rpc.post("runstatus.application.capture.fail", {
          session_id: request.session_id,
          request_id: request.request_id,
          app_id: request.app_id,
          revision: request.revision,
          reason,
          receipts,
        });
      };
      for (const [index, actionId] of request.action_ids.entries()) {
        const frame = deps.frame();
        if (!frame || frame.application_id !== request.app_id || frame.session_id !== request.session_id) {
          await fail("application_surface_changed");
          return;
        }
        const revision = frame.revision;
        const idempotencyKey = `${request.request_id}:${index}`;
        const outcome = await deps.dispatch({
          action: actionId,
          input: {},
          session_id: request.session_id,
          frame_revision: revision,
          idempotency_key: idempotencyKey,
        });
        if (!outcome || !outcome.ok) {
          await fail("application_action_failed");
          return;
        }
        const receipt = outcome.receipt;
        if (
          !receipt ||
          !receipt.idempotency_key ||
          receipt.frame_revision !== revision
        ) {
          await fail("canonical_action_receipt_missing");
          return;
        }
        receipts.push({
          index,
          action_id: actionId,
          receipt_id: receipt.id,
          handler_id: receipt.handler_id,
          semantic_ref: receipt.semantic_ref,
          idempotency_key: receipt.idempotency_key,
          frame_revision: revision,
        });
        await attach();
      }
      await deps.rpc.post("runstatus.application.capture.ack", {
        session_id: request.session_id,
        request_id: request.request_id,
        app_id: request.app_id,
        revision: request.revision,
        receipts,
        recording: (deps.recording ?? (() => buildSessionEnvelope(snapshotSessionEvents())))(),
      });
    } catch {
      try {
        await deps.rpc.post("runstatus.application.capture.fail", {
          session_id: request.session_id,
          request_id: request.request_id,
          app_id: request.app_id,
          revision: request.revision,
          reason: "application_capture_client_error",
          receipts,
        });
      } catch {
        // The server will preserve pending/interrupted truth if the connection is gone.
      }
    } finally {
      processing.delete(request.request_id);
    }
  };

  return {
    attach,
    dispose() {
      disposed = true;
      closeStream?.();
      closeStream = null;
      if (subscriptionId) {
        void deps.rpc.post("runstatus.application.capture.unsubscribe", {
          subscription_id: subscriptionId,
        });
      }
    },
  };
}

export function openApplicationCaptureEventStream(
  subscriptionId: string,
  onMessage: (raw: string) => void,
): () => void {
  const stream = new EventSource(
    `/rpc/application-captures?subscription_id=${encodeURIComponent(subscriptionId)}`,
  );
  stream.onmessage = (event) => onMessage(event.data);
  return () => stream.close();
}
