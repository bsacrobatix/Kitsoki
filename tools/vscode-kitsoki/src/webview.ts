// webview.ts — KitsokiViewProvider serves the bundled singlefile SPA into a
// WebviewView (used for both the sidebar chat view and the panel trace view),
// injects a per-resolve nonce + CSP, and wires the postMessage relay to the
// shared backend.

import * as vscode from 'vscode';
import * as fs from 'node:fs';
import * as crypto from 'node:crypto';
import { Relay, type InboundEnvelope, type OutboundEnvelope } from './relay';
import type { Backend } from './backend';

/** Additive, webview-only theme shim mapping SPA tokens to VS Code theme vars. */
const THEME_SHIM = `
:root {
  --kitsoki-bg: var(--vscode-editor-background);
  --kitsoki-fg: var(--vscode-editor-foreground);
  --kitsoki-focus: var(--vscode-focusBorder);
  --kitsoki-border: var(--vscode-panel-border);
}
html, body { background: var(--vscode-editor-background); color: var(--vscode-editor-foreground); }
`;

export class KitsokiViewProvider implements vscode.WebviewViewProvider {
  constructor(
    private readonly extensionUri: vscode.Uri,
    private readonly backend: Backend,
    private readonly out: vscode.OutputChannel,
  ) {}

  resolveWebviewView(view: vscode.WebviewView): void {
    const mediaRoot = vscode.Uri.joinPath(this.extensionUri, 'media');
    view.webview.options = {
      enableScripts: true,
      localResourceRoots: [mediaRoot],
    };

    const relay = new Relay({
      base: '', // set once the backend is ready (see below)
      post: (env: OutboundEnvelope) => {
        void view.webview.postMessage(env);
      },
      log: (line) => this.out.appendLine(line),
    });

    const sub = view.webview.onDidReceiveMessage((msg: InboundEnvelope) => {
      relay.handle(msg);
    });

    view.onDidDispose(() => {
      relay.dispose();
      sub.dispose();
    });

    // Bring up the shared backend, then point the relay at it and render.
    void this.backend
      .start()
      .then((base) => {
        relay.setBase(base);
        view.webview.html = this.renderHtml(view.webview);
      })
      .catch((err: Error) => {
        this.out.appendLine(`[webview] backend start failed: ${err.message}`);
        view.webview.html = this.renderError(err.message);
      });
  }

  private renderHtml(webview: vscode.Webview): string {
    const indexPath = vscode.Uri.joinPath(this.extensionUri, 'media', 'spa', 'index.html').fsPath;
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

  private renderError(message: string): string {
    return `<!DOCTYPE html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline';">
</head><body style="font-family: sans-serif; padding: 1rem; color: var(--vscode-errorForeground);">
<h3>Kitsoki backend failed to start</h3>
<pre>${message.replace(/[<&]/g, (c) => (c === '<' ? '&lt;' : '&amp;'))}</pre>
<p>Run <code>Kitsoki: Restart Backend</code> after fixing the binary path / settings.</p>
</body></html>`;
  }
}
