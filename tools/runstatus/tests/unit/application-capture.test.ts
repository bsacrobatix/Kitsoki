import { describe, expect, it } from "vitest";
import {
  startApplicationCaptureClient,
  type ApplicationCaptureRequest,
} from "../../src/application/capture.js";
import type { ApplicationActionEnvelope, ApplicationFrame } from "../../src/application/types.js";

describe("application capture client", () => {
  it("dispatches action IDs in order, follows revision evolution, and acknowledges once", async () => {
    let frame = applicationFrame(4);
    let onMessage: ((raw: string) => void) | null = null;
    const calls: Array<{ method: string; params: Record<string, unknown> }> = [];
    const dispatched: ApplicationActionEnvelope[] = [];
    const rpc = {
      async post<T>(method: string, params: Record<string, unknown>): Promise<T> {
        calls.push({ method, params });
        if (method.endsWith(".subscribe")) return { subscription_id: "sub-1" } as T;
        return { ok: true } as T;
      },
    };
    const controller = startApplicationCaptureClient({
      sessionId: "session",
      rpc,
      frame: () => frame,
      dispatch: async (envelope) => {
        dispatched.push(envelope);
        frame = applicationFrame(frame.revision + 1);
        return {
          ok: true,
          frame,
          receipt: applicationReceipt(envelope, `ar_${dispatched.length}`),
        };
      },
      recording: () => ({
        schemaVersion: 1,
        source: "test",
        viewport: { width: 100, height: 100 },
        startTime: 1,
        endTime: 2,
        durationMs: 1,
        events: [{ type: 4, timestamp: 1 }, { type: 2, timestamp: 2 }],
      }),
      openStream: (_id, handler) => {
        onMessage = handler;
        return () => undefined;
      },
    });
    await controller.attach();
    const request: ApplicationCaptureRequest = {
      schema: "kitsoki/application-capture/v1",
      request_id: "request",
      app_id: "app",
      session_id: "session",
      revision: 4,
      scenario_ref: "kitsoki://scenario/one",
      action_ids: ["open", "confirm"],
    };
    onMessage?.(JSON.stringify({
      method: "runstatus.application.capture.request",
      params: request,
    }));
    await eventually(() => calls.some((call) => call.method.endsWith(".ack")));

    expect(dispatched.map((item) => [item.action, item.frame_revision, item.idempotency_key])).toEqual([
      ["open", 4, "request:0"],
      ["confirm", 5, "request:1"],
    ]);
    const ack = calls.find((call) => call.method.endsWith(".ack"));
    expect(ack?.params.receipts).toEqual([
      {
        index: 0, action_id: "open", receipt_id: "ar_1",
        handler_id: "handler.open", semantic_ref: "semantic.open",
        idempotency_key: "request:0", frame_revision: 4,
      },
      {
        index: 1, action_id: "confirm", receipt_id: "ar_2",
        handler_id: "handler.confirm", semantic_ref: "semantic.confirm",
        idempotency_key: "request:1", frame_revision: 5,
      },
    ]);
    controller.dispose();
  });

  it("deduplicates a re-emitted pending request while it is executing", async () => {
    const frame = applicationFrame(2);
    let onMessage: ((raw: string) => void) | null = null;
    let dispatches = 0;
    let release: (() => void) | null = null;
    const blocked = new Promise<void>((resolve) => {
      release = resolve;
    });
    const controller = startApplicationCaptureClient({
      sessionId: "session",
      rpc: {
        async post<T>(method: string): Promise<T> {
          return (method.endsWith(".subscribe") ? { subscription_id: "sub" } : { ok: true }) as T;
        },
      },
      frame: () => frame,
      dispatch: async (envelope) => {
        dispatches++;
        await blocked;
        return { ok: true, receipt: applicationReceipt(envelope, "ar_once") };
      },
      recording: () => ({
        schemaVersion: 1, source: "test", viewport: { width: 1, height: 1 },
        startTime: 1, endTime: 2, durationMs: 1,
        events: [{ type: 4 }, { type: 2 }],
      }),
      openStream: (_id, handler) => {
        onMessage = handler;
        return () => undefined;
      },
    });
    await controller.attach();
    const raw = JSON.stringify({
      method: "runstatus.application.capture.request",
      params: {
        schema: "kitsoki/application-capture/v1",
        request_id: "same", app_id: "app", session_id: "session",
        revision: 2, scenario_ref: "kitsoki://scenario", action_ids: ["save"],
      },
    });
    onMessage?.(raw);
    onMessage?.(raw);
    await eventually(() => dispatches === 1);
    expect(dispatches).toBe(1);
    release?.();
    controller.dispose();
  });

  it("reports dispatch failure immediately with partial canonical receipts", async () => {
    let frame = applicationFrame(10);
    let onMessage: ((raw: string) => void) | null = null;
    const calls: Array<{ method: string; params: Record<string, unknown> }> = [];
    let dispatches = 0;
    const controller = startApplicationCaptureClient({
      sessionId: "session",
      rpc: {
        async post<T>(method: string, params: Record<string, unknown>): Promise<T> {
          calls.push({ method, params });
          return (method.endsWith(".subscribe") ? { subscription_id: "sub" } : { ok: true }) as T;
        },
      },
      frame: () => frame,
      dispatch: async (envelope) => {
        dispatches++;
        if (dispatches === 1) {
          frame = applicationFrame(11);
          return { ok: true, receipt: applicationReceipt(envelope, "ar_first") };
        }
        return { ok: false, error: "denied" };
      },
      openStream: (_id, handler) => {
        onMessage = handler;
        return () => undefined;
      },
    });
    await controller.attach();
    onMessage?.(JSON.stringify({
      method: "runstatus.application.capture.request",
      params: {
        schema: "kitsoki/application-capture/v1",
        request_id: "failure", app_id: "app", session_id: "session",
        revision: 10, scenario_ref: "kitsoki://scenario",
        action_ids: ["open", "confirm"],
      },
    }));
    await eventually(() => calls.some((call) => call.method.endsWith(".fail")));
    const failure = calls.find((call) => call.method.endsWith(".fail"));
    expect(failure?.params.reason).toBe("application_action_failed");
    expect(failure?.params.receipts).toEqual([{
      index: 0, action_id: "open", receipt_id: "ar_first",
      handler_id: "handler.open", semantic_ref: "semantic.open",
      idempotency_key: "failure:0", frame_revision: 10,
    }]);
    expect(calls.some((call) => call.method.endsWith(".ack"))).toBe(false);
    controller.dispose();
  });
});

function applicationFrame(revision: number): ApplicationFrame {
  return {
    schema: "application-frame/v1",
    application_id: "app",
    session_id: "session",
    revision,
    page: "main",
    page_semantic: { ref: "page", kind: "page", label: "Page" },
    semantic: { ref: "app", kind: "application", label: "App" },
    workflow: { current: "main" },
  } as ApplicationFrame;
}

function applicationReceipt(envelope: ApplicationActionEnvelope, id: string) {
  return {
    schema: "application-receipt/v1" as const,
    id,
    handler_id: `handler.${envelope.action}`,
    semantic_ref: `semantic.${envelope.action}`,
    session_id: envelope.session_id,
    actor: "alice",
    effect: "write",
    routing: {},
    budget: {},
    input_digest: "sha256:input",
    output_digest: "sha256:output",
    transport: "web",
    frame_revision: envelope.frame_revision,
    outcome: "ok",
    idempotency_key: envelope.idempotency_key,
  };
}

async function eventually(check: () => boolean): Promise<void> {
  for (let index = 0; index < 100; index++) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
  throw new Error("condition was not reached");
}
