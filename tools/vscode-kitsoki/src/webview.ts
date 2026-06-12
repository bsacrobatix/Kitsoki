// webview.ts — renders the bundled singlefile SPA into a VS Code webview and
// wires the postMessage relay to the shared backend.
//
// The primary surface is an EDITOR-AREA WebviewPanel (KitsokiPanel) so the chat
// is front-and-center in the wide editor, not crushed into the narrow sidebar.
// Inside that webview the SPA auto-enables its embed layout (chat dominant + a
// hint rail that maximizes trace/graph). mountSpa() is the one shared code path:
// relay wiring + nonce/CSP + backend start.

import * as vscode from 'vscode';
import * as fs from 'node:fs';
import * as crypto from 'node:crypto';
import { Relay, type InboundEnvelope, type OutboundEnvelope } from './relay';
import type { Backend } from './backend';

/**
 * Additive, webview-only theme shim. Maps SPA tokens to VS Code theme vars AND
 * re-themes the agent room-view "paper" card to the dark editor chrome.
 *
 * The SPA renders the agent room view (`.chat-bubble--agent` → `ViewElement`) as
 * a deliberate LIGHT "paper" card on the dark chat pane — that is the intended
 * look in the browser (a sheet of paper on a dark desk). Inside VS Code, however,
 * a large white card against the dark editor chrome reads as an unthemed panel
 * and breaks the "native theming" the embed promises. This shim is scoped to the
 * webview only (it is never injected into the browser SPA), so it darkens the
 * paper card for the embed without altering the web UI.
 *
 * The room-view palette (ViewElement.vue / ChatTranscript.vue) is `scoped` with
 * hardcoded light hex values, so these overrides use VS Code theme vars at high
 * specificity (`!important`) to flip the card surface dark and its text light.
 */
const THEME_SHIM = `
:root {
  --kitsoki-bg: var(--vscode-editor-background);
  --kitsoki-fg: var(--vscode-editor-foreground);
  --kitsoki-focus: var(--vscode-focusBorder);
  --kitsoki-border: var(--vscode-panel-border);
}
html, body { background: var(--vscode-editor-background); color: var(--vscode-editor-foreground); }

/* ── Agent room-view "paper" card → dark editor surface (embed only) ──────── */
.chat-bubble--agent {
  background: var(--vscode-editorWidget-background, var(--vscode-editor-background)) !important;
  color: var(--vscode-editor-foreground) !important;
  border: 1px solid var(--vscode-panel-border, var(--vscode-widget-border)) !important;
}
/* Flip the light room-view text/keys/headings to the editor foreground so they
   stay legible on the now-dark card. (Scoped classes → high-specificity host.) */
html body .chat-bubble--agent .ve-prose,
html body .chat-bubble--agent .ve-heading,
html body .chat-bubble--agent .ve-list,
html body .chat-bubble--agent .ve-kv,
html body .chat-bubble--agent .ve-kv-value,
html body .chat-bubble--agent .ve-bold,
html body .chat-bubble--agent .chat-view,
html body .chat-bubble--agent .chat-view .cv-h,
html body .chat-bubble--agent .chat-view strong {
  color: var(--vscode-editor-foreground) !important;
}
html body .chat-bubble--agent .ve-kv-key,
html body .chat-bubble--agent .ve-list-hint,
html body .chat-bubble--agent .ve-media-fallback {
  color: var(--vscode-descriptionForeground, var(--vscode-editor-foreground)) !important;
  opacity: 0.85;
}
/* Markdown table (the 5-day forecast) + its cells → dark grid. The v-html nodes
   carry no scoped attribute, so plain descendant selectors reach them. */
html body .chat-bubble--agent .chat-view table,
html body .chat-bubble--agent .chat-view th,
html body .chat-bubble--agent .chat-view td {
  background: transparent !important;
  color: var(--vscode-editor-foreground) !important;
  border-color: var(--vscode-panel-border, var(--vscode-widget-border)) !important;
}
/* Inline code on the dark card. */
html body .chat-bubble--agent .ve-inline-code,
html body .chat-bubble--agent .chat-view code {
  background: var(--vscode-textCodeBlock-background, rgba(255,255,255,0.08)) !important;
  color: var(--vscode-textPreformat-foreground, var(--vscode-editor-foreground)) !important;
}
`;

/** Read the bundled singlefile SPA and inject a per-render CSP + nonce + theme. */
export function renderSpaHtml(webview: vscode.Webview, extensionUri: vscode.Uri): string {
  const indexPath = vscode.Uri.joinPath(extensionUri, 'media', 'spa', 'index.html').fsPath;
  let html = fs.readFileSync(indexPath, 'utf8');
  const nonce = crypto.randomBytes(16).toString('base64');

  const csp = [
    `default-src 'none'`,
    `script-src 'nonce-${nonce}'`,
    // style-src uses 'unsafe-inline' ALONE (no nonce). The SPA is Vue: it
    // injects <style> elements at runtime with no nonce, and a nonce in
    // style-src makes the browser IGNORE 'unsafe-inline' — so a nonce here
    // would refuse every runtime-injected style and strip the UI's styling.
    // Inline styles cannot execute code, so 'unsafe-inline' is the safe and
    // standard webview posture; the script nonce stays strict.
    `style-src 'unsafe-inline'`,
    `img-src ${webview.cspSource} data: blob:`,
    `font-src ${webview.cspSource}`,
  ].join('; ');

  // Add a nonce to every inline <script> the singlefile bundle inlines (the
  // script-src policy requires it). Styles need no nonce under 'unsafe-inline'.
  html = html.replace(/<script(?![^>]*\bnonce=)/g, `<script nonce="${nonce}"`);

  const cspMeta = `<meta http-equiv="Content-Security-Policy" content="${csp}">`;
  const themeTag = `<style>${THEME_SHIM}</style>`;

  if (/<head[^>]*>/i.test(html)) {
    html = html.replace(/<head[^>]*>/i, (m) => `${m}\n${cspMeta}\n${themeTag}`);
  } else {
    html = `${cspMeta}\n${themeTag}\n${html}`;
  }
  return html;
}

/** Minimal error page shown when the backend never comes up. */
export function renderError(message: string): string {
  return `<!DOCTYPE html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline';">
</head><body style="font-family: sans-serif; padding: 1rem; color: var(--vscode-errorForeground);">
<h3>Kitsoki backend failed to start</h3>
<pre>${message.replace(/[<&]/g, (c) => (c === '<' ? '&lt;' : '&amp;'))}</pre>
<p>Run <code>Kitsoki: Restart Backend</code> after fixing the binary path / settings.</p>
</body></html>`;
}

/**
 * Wire a webview to the shared backend: a postMessage relay (host side of the
 * BridgeTransport protocol), then bring the backend up and render the SPA. The
 * returned Disposable tears down the relay + message subscription. Shared by the
 * editor panel and any other webview surface so they can never drift.
 */
export function mountSpa(
  webview: vscode.Webview,
  extensionUri: vscode.Uri,
  backend: Backend,
  out: vscode.OutputChannel,
): vscode.Disposable {
  const mediaRoot = vscode.Uri.joinPath(extensionUri, 'media');
  webview.options = { enableScripts: true, localResourceRoots: [mediaRoot] };

  const relay = new Relay({
    base: '', // set once the backend is ready (below)
    post: (env: OutboundEnvelope) => {
      void webview.postMessage(env);
    },
    log: (line) => out.appendLine(line),
  });

  const sub = webview.onDidReceiveMessage((msg: InboundEnvelope) => relay.handle(msg));

  void backend
    .start()
    .then((base) => {
      relay.setBase(base);
      webview.html = renderSpaHtml(webview, extensionUri);
    })
    .catch((err: Error) => {
      out.appendLine(`[webview] backend start failed: ${err.message}`);
      webview.html = renderError(err.message);
    });

  return new vscode.Disposable(() => {
    relay.dispose();
    sub.dispose();
  });
}

/**
 * KitsokiPanel — the singleton editor-area WebviewPanel that hosts the chat
 * front-and-center. reveal() creates it on first use and re-focuses it after;
 * the SPA inside auto-enables its embed layout (hint rail) because the webview
 * exposes acquireVsCodeApi.
 */
export class KitsokiPanel {
  private static current: KitsokiPanel | undefined;

  static reveal(
    extensionUri: vscode.Uri,
    backend: Backend,
    out: vscode.OutputChannel,
  ): void {
    if (KitsokiPanel.current) {
      KitsokiPanel.current.panel.reveal(vscode.ViewColumn.Active);
      return;
    }
    const panel = vscode.window.createWebviewPanel(
      'kitsoki.chatPanel',
      'Kitsoki',
      vscode.ViewColumn.Active,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(extensionUri, 'media')],
      },
    );
    KitsokiPanel.current = new KitsokiPanel(panel, extensionUri, backend, out);
  }

  private readonly mount: vscode.Disposable;

  private constructor(
    private readonly panel: vscode.WebviewPanel,
    extensionUri: vscode.Uri,
    backend: Backend,
    out: vscode.OutputChannel,
  ) {
    this.mount = mountSpa(panel.webview, extensionUri, backend, out);
    panel.onDidDispose(() => {
      this.mount.dispose();
      KitsokiPanel.current = undefined;
    });
  }
}
