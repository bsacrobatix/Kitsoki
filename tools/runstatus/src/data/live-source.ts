import type {
  SessionHeader,
  AppDef,
  MermaidSnapshot,
  TraceEvent,
  TurnResult,
} from "../types.js";
import type { DataSource, TraceCursor } from "./source.js";
import { JsonRpcClient } from "../transport/jsonrpc.js";

/**
 * DataSource backed by the kitsoki HTTP JSON-RPC + SSE endpoint.
 */
export class LiveSource implements DataSource {
  private readonly client: JsonRpcClient;

  constructor(base = "/") {
    this.client = new JsonRpcClient(base);
  }

  listSessions(): Promise<SessionHeader[]> {
    return this.client.post<SessionHeader[]>("runstatus.sessions.list", {});
  }

  getSession(sessionId: string): Promise<SessionHeader> {
    return this.client.post<SessionHeader>("runstatus.session.get", {
      session_id: sessionId,
    });
  }

  getApp(sessionId: string): Promise<AppDef> {
    return this.client.post<AppDef>("runstatus.session.app", {
      session_id: sessionId,
    });
  }

  getMermaid(sessionId: string, detail?: string): Promise<MermaidSnapshot> {
    const params: Record<string, unknown> = { session_id: sessionId };
    if (detail !== undefined) params["detail"] = detail;
    return this.client.post<MermaidSnapshot>(
      "runstatus.session.mermaid",
      params
    );
  }

  getTrace(
    sessionId: string,
    cursor?: TraceCursor
  ): Promise<{ events: TraceEvent[]; last_turn: number }> {
    const params: Record<string, unknown> = { session_id: sessionId };
    if (cursor?.since_turn !== undefined)
      params["since_turn"] = cursor.since_turn;
    if (cursor?.until_turn !== undefined)
      params["until_turn"] = cursor.until_turn;
    if (cursor?.limit !== undefined) params["limit"] = cursor.limit;
    return this.client.post<{ events: TraceEvent[]; last_turn: number }>(
      "runstatus.session.trace",
      params
    );
  }

  subscribe(
    sessionId: string,
    onEvent: (e: TraceEvent) => void
  ): () => void {
    return this.client.subscribe(sessionId, onEvent, (sinceТurn) =>
      this.getTrace(sessionId, { since_turn: sinceТurn })
    );
  }

  // ── Write/read RPCs ────────────────────────────────────────────────────
  //
  // The live server hosts a single in-process session, so the write/read RPCs
  // take no session_id (the engine resolves the one live session). We still
  // pass session_id for parity with the read RPCs; the server ignores it.

  view(sessionId: string): Promise<TurnResult> {
    return this.client.post<TurnResult>("runstatus.session.view", {
      session_id: sessionId,
    });
  }

  submit(
    sessionId: string,
    intent: string,
    slots: Record<string, unknown> = {}
  ): Promise<TurnResult> {
    return this.client.post<TurnResult>("runstatus.session.submit", {
      session_id: sessionId,
      intent,
      slots,
    });
  }

  sendTurn(sessionId: string, input: string): Promise<TurnResult> {
    return this.client.post<TurnResult>("runstatus.session.turn", {
      session_id: sessionId,
      input,
    });
  }

  continueTurn(
    sessionId: string,
    slots: Record<string, unknown>
  ): Promise<TurnResult> {
    return this.client.post<TurnResult>("runstatus.session.continue", {
      session_id: sessionId,
      slots,
    });
  }

  offpath(sessionId: string, input: string): Promise<{ answer: string }> {
    return this.client.post<{ answer: string }>("runstatus.session.offpath", {
      session_id: sessionId,
      input,
    });
  }
}
