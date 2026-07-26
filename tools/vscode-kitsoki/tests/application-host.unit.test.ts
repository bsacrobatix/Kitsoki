import assert from "node:assert/strict";
import { test } from "node:test";
import {
  ApplicationCommandHost,
  applicationBundleFrameHTML,
  reportApplicationFeedback,
  type ApplicationBackend,
} from "../src/application-host";

test("application host registers declared commands with semantic and revision identity", async () => {
  const calls: { method: string; params: Record<string, unknown> }[] = [];
  const handlers = new Map<string, (input?: unknown) => Promise<void>>();
  const backend: ApplicationBackend = {
    async rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
      calls.push({ method, params });
      if (method === "runstatus.session.app") {
        return {
          application: {
            surfaces: { vscode: { reuse: "web", native: { commands: ["demo.open"] } } },
          },
        } as T;
      }
      if (method === "runstatus.application.frame") {
        return {
          session_id: "session-1",
          revision: 8,
          page: "home",
          actions: [{
            id: "demo.open",
            routing_mode: "exact",
            semantic: {
              ref: "demo.action.open",
              name: "Open",
              description: "Open the selected item.",
            },
          }],
        } as T;
      }
      if (method === "runstatus.application.vscode_action") return { ok: true } as T;
      throw new Error(`unexpected method ${method}`);
    },
  };
  const host = new ApplicationCommandHost(backend, (id, callback) => {
    handlers.set(id, callback);
    return { dispose: () => handlers.delete(id) };
  });
  await host.refresh("session-1");

  assert.deepEqual(host.descriptors()[0], {
    id: "demo.open",
    name: "Open",
    description: "Open the selected item.",
    semanticRef: "demo.action.open",
    envelope: {
      action: "demo.open",
      input: {},
      session_id: "session-1",
      frame_revision: 8,
      routing_mode: "exact",
    },
  });
  await handlers.get("demo.open")!();
  assert.deepEqual(
    calls.find((call) => call.method === "runstatus.application.vscode_action")?.params,
    {
      action: "demo.open",
      input: {},
      session_id: "session-1",
      frame_revision: 8,
      routing_mode: "exact",
      page: "home",
    },
  );
});

test("application host consumes the built web manifest and submits canonical feedback", async () => {
  const calls: { method: string; params: Record<string, unknown> }[] = [];
  const backend: ApplicationBackend = {
    async rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
      calls.push({ method, params });
      if (method === "runstatus.session.app") {
        return {
          app: { id: "demo" },
          application: { surfaces: { vscode: { reuse: "web" } } },
        } as T;
      }
      if (method === "runstatus.application.frame") {
        return { session_id: "session-1", revision: 4, page: "review" } as T;
      }
      if (method === "runstatus.application.feedback") {
        return {
          report: {
            anchor: {
              kind: "semantic_element",
              semantic_element: {
                plugin: "kitsoki.application",
                ref: "demo.card.review",
                data: { application_id: "demo", frame_revision: 4 },
              },
            },
          },
          receipt: { ref: "feedback-1", deduped: false },
        } as T;
      }
      throw new Error(`unexpected method ${method}`);
    },
  };
  const host = new ApplicationCommandHost(
    backend,
    () => ({ dispose() {} }),
    () => {},
    async () => ({}),
    async (applicationId) => ({
      schema: "application-bundle/v1",
      application_id: applicationId,
      digest: "sha256:demo",
      entry: "index.html",
      files: ["index.html", "assets/app.js"],
      entryURL: "http://127.0.0.1/application/demo/index.html",
    }),
  );
  await host.refresh("session-1");
  assert.equal(host.bundle()?.digest, "sha256:demo");
  const panel = applicationBundleFrameHTML(host.bundle()!);
  assert.match(panel, /frame-src http:\/\/127\.0\.0\.1/);
  assert.match(panel, /iframe src="http:\/\/127\.0\.0\.1\/application\/demo\/index\.html"/);
  const feedback = await host.submitFeedback(
    "session-1",
    "demo.card.review",
    "The review card is unclear.",
  );
  assert.equal(feedback.report.anchor.semantic_element.ref, "demo.card.review");
  assert.deepEqual(
    calls.find((call) => call.method === "runstatus.application.feedback")?.params,
    {
      session_id: "session-1",
      ref: "demo.card.review",
      instruction: "The review card is unclear.",
      kind: "bug",
    },
  );
});

test("operator feedback command reports the current native semantic target without frame data", async () => {
  const calls: { method: string; params: Record<string, unknown> }[] = [];
  const handlers = new Map<string, (input?: unknown) => Promise<void>>();
  const backend: ApplicationBackend = {
    async rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
      calls.push({ method, params });
      if (method === "runstatus.session.app") {
        return {
          application: {
            surfaces: { vscode: { native: { commands: ["demo.open"] } } },
          },
        } as T;
      }
      if (method === "runstatus.application.frame") {
        return {
          session_id: "session-1",
          revision: 8,
          page: "home",
          actions: [{
            id: "demo.open",
            semantic: {
              ref: "demo.action.open",
              name: "Open",
              description: "Open the selected item.",
            },
          }],
        } as T;
      }
      if (method === "runstatus.application.action") return { ok: true } as T;
      if (method === "runstatus.application.feedback") {
        return {
          report: {
            anchor: {
              kind: "semantic_element",
              semantic_element: {
                plugin: "kitsoki.application",
                ref: "demo.action.open",
                data: { application_id: "demo", frame_revision: 8 },
              },
            },
          },
          receipt: { ref: "feedback-vscode", deduped: false },
        } as T;
      }
      throw new Error(`unexpected method ${method}`);
    },
  };
  const host = new ApplicationCommandHost(backend, (id, callback) => {
    handlers.set(id, callback);
    return { dispose: () => handlers.delete(id) };
  });
  await host.refresh("session-1");
  await handlers.get("demo.open")!();

  const submission = await reportApplicationFeedback(
    host,
    async (targets) => {
      assert.equal(targets[0]?.current, true);
      return targets[0];
    },
    async () => "The selected action is unclear.",
  );

  assert.equal(submission?.report.anchor.semantic_element.ref, "demo.action.open");
  const request = calls.find((call) => call.method === "runstatus.application.feedback");
  assert.deepEqual(request?.params, {
    session_id: "session-1",
    ref: "demo.action.open",
    instruction: "The selected action is unclear.",
    kind: "bug",
  });
  assert.doesNotMatch(
    JSON.stringify(request),
    /secret-prop|api-token|\/Users\/operator|frame_revision/,
  );
});

test("application host obtains structured input for schema-backed commands", async () => {
  const calls: { method: string; params: Record<string, unknown> }[] = [];
  const handlers = new Map<string, (input?: unknown) => Promise<void>>();
  const backend: ApplicationBackend = {
    async rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
      calls.push({ method, params });
      if (method === "runstatus.session.app") {
        return {
          application: {
            surfaces: { vscode: { native: { commands: ["demo.open"] } } },
          },
        } as T;
      }
      if (method === "runstatus.application.frame") {
        return {
          session_id: "session-1",
          revision: 8,
          page: "home",
          actions: [{
            id: "demo.open",
            input_schema: {
              type: "object",
              required: ["item_id"],
              properties: { item_id: { type: "string" } },
            },
            semantic: {
              ref: "demo.action.open",
              name: "Open",
              description: "Open the selected item.",
            },
          }],
        } as T;
      }
      if (method === "runstatus.application.vscode_action") return { ok: true } as T;
      throw new Error(`unexpected method ${method}`);
    },
  };
  const host = new ApplicationCommandHost(
    backend,
    (id, callback) => {
      handlers.set(id, callback);
      return { dispose: () => handlers.delete(id) };
    },
    () => {},
    async () => ({ item_id: "chg-42" }),
  );
  await host.refresh("session-1");
  await handlers.get("demo.open")!();

  assert.deepEqual(
    calls.find((call) => call.method === "runstatus.application.vscode_action")?.params,
    {
      action: "demo.open",
      input: { item_id: "chg-42" },
      session_id: "session-1",
      frame_revision: 8,
      page: "home",
    },
  );
});

test("application host rejects native commands absent from the canonical frame", async () => {
  const backend: ApplicationBackend = {
    async rpc<T>(method: string): Promise<T> {
      if (method === "runstatus.session.app") {
        return { application: { surfaces: { vscode: { native: { commands: ["missing"] } } } } } as T;
      }
      return { session_id: "session-1", revision: 1, page: "home", actions: [] } as T;
    },
  };
  const host = new ApplicationCommandHost(backend, () => ({ dispose() {} }));
  await assert.rejects(() => host.refresh("session-1"), /has no frame action/);
});

test("current-session changes replace and dispose native command registrations", async () => {
  let onSession: ((sessionId: string | null) => void) | undefined;
  let subscriptionDisposed = false;
  const disposed: string[] = [];
  const active = new Set<string>();
  const backend: ApplicationBackend = {
    async rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
      const session = String(params.session_id);
      if (method === "runstatus.session.app") {
        return {
          application: {
            surfaces: { vscode: { native: { commands: [`${session}.open`] } } },
          },
        } as T;
      }
      return {
        session_id: session,
        revision: session === "session-1" ? 1 : 2,
        page: "home",
        regions: [{
          cards: [{
            body: [{
              items: [{
                actions: [{
                  id: `${session}.open`,
                  semantic: {
                    ref: `${session}.action.open`,
                    name: "Open",
                    description: "Open.",
                  },
                }],
              }],
            }],
          }],
        }],
      } as T;
    },
  };
  const host = new ApplicationCommandHost(backend, (id) => {
    active.add(id);
    return {
      dispose: () => {
        active.delete(id);
        disposed.push(id);
      },
    };
  });
  host.watchCurrentSession((callback) => {
    onSession = callback;
    return { dispose: () => { subscriptionDisposed = true; } };
  });

  onSession!("session-1");
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual([...active], ["session-1.open"]);
  onSession!("session-2");
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual([...active], ["session-2.open"]);
  assert.deepEqual(disposed, ["session-1.open"]);

  host.dispose();
  assert.equal(subscriptionDisposed, true);
  assert.deepEqual([...active], []);
});
