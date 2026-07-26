import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { test } from "node:test";
import {
  ApplicationCommandHost,
  type ApplicationBackend,
} from "../src/application-host";

// conformance-consumer:vscode-ts
const fixturePath = resolve(
  process.cwd(),
  "../../internal/applicationconformance/application-conformance-v1.json",
);
const fixture = JSON.parse(readFileSync(fixturePath, "utf8")) as {
  schema: "application-conformance/v1";
  application_id: string;
  session_id: string;
  page: string;
  frame_revision: number;
  action: { id: string; semantic_ref: string; input: Record<string, unknown> };
  feedback: {
    ref: string; instruction: string; kind: string; idempotency_key: string; excluded: string[];
  };
};

test("shared fixture drives actual VS Code action and feedback adapters", async () => {
  assert.equal(fixture.schema, "application-conformance/v1");
  const calls: { method: string; params: Record<string, unknown> }[] = [];
  const commands = new Map<string, (input?: unknown) => Promise<void>>();
  const backend: ApplicationBackend = {
    async rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
      calls.push({ method, params });
      if (method === "runstatus.session.app") {
        return {
          application: { surfaces: { vscode: { native: { commands: [fixture.action.id] } } } },
        } as T;
      }
      if (method === "runstatus.application.frame") {
        return {
          application_id: fixture.application_id,
          session_id: fixture.session_id,
          revision: fixture.frame_revision,
          page: fixture.page,
          actions: [{
            id: fixture.action.id,
            semantic: {
              ref: fixture.action.semantic_ref,
              name: "Open",
              description: "Open the selected item.",
            },
          }],
        } as T;
      }
      if (method === "runstatus.application.vscode_action") {
        return { schema: "application-outcome/v1", outcome: "ok" } as T;
      }
      if (method === "runstatus.application.feedback") {
        return {
          report: {
            anchor: {
              semantic_element: {
                plugin: "kitsoki.application",
                ref: fixture.feedback.ref,
              },
            },
          },
          receipt: { ref: fixture.feedback.idempotency_key, deduped: false },
        } as T;
      }
      throw new Error(`unexpected method ${method}`);
    },
  };
  const host = new ApplicationCommandHost(
    backend,
    (id, callback) => {
      commands.set(id, callback);
      return { dispose: () => commands.delete(id) };
    },
    () => {},
    async () => fixture.action.input,
  );
  await host.refresh(fixture.session_id);
  await commands.get(fixture.action.id)!();
  const feedback = await host.submitFeedback(
    fixture.session_id,
    fixture.feedback.ref,
    fixture.feedback.instruction,
    fixture.feedback.kind,
    fixture.feedback.idempotency_key,
  );
  assert.equal(feedback.report.anchor.semantic_element.ref, fixture.feedback.ref);
  const encoded = JSON.stringify(calls);
  for (const excluded of fixture.feedback.excluded) assert.doesNotMatch(encoded, new RegExp(excluded.replaceAll("/", "\\/")));
});
