// extension.ts — activation entry point. Registers the two WebviewView
// providers (sidebar chat + panel trace), an OutputChannel, the kitsoki
// commands, and disposes the shared backend on deactivate.

import * as vscode from 'vscode';
import { Backend } from './backend';
import { KitsokiViewProvider } from './webview';

let backend: Backend | undefined;

export function activate(context: vscode.ExtensionContext): void {
  const out = vscode.window.createOutputChannel('Kitsoki');
  context.subscriptions.push(out);

  const cwd = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  backend = new Backend(out, cwd);
  context.subscriptions.push({ dispose: () => backend?.dispose() });

  const provider = new KitsokiViewProvider(context.extensionUri, backend, out);

  context.subscriptions.push(
    vscode.window.registerWebviewViewProvider('kitsoki.chat', provider, {
      webviewOptions: { retainContextWhenHidden: true },
    }),
    vscode.window.registerWebviewViewProvider('kitsoki.trace', provider, {
      webviewOptions: { retainContextWhenHidden: true },
    }),
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('kitsoki.openChat', async () => {
      await vscode.commands.executeCommand('kitsoki.chat.focus');
    }),
    vscode.commands.registerCommand('kitsoki.openTrace', async () => {
      await vscode.commands.executeCommand('kitsoki.trace.focus');
    }),
    vscode.commands.registerCommand('kitsoki.restartBackend', async () => {
      out.appendLine('[extension] restart backend requested');
      try {
        const base = await backend?.restart();
        void vscode.window.showInformationMessage(`Kitsoki backend restarted at ${base}`);
      } catch (e) {
        void vscode.window.showErrorMessage(`Kitsoki backend restart failed: ${(e as Error).message}`);
      }
    }),
  );
}

export function deactivate(): void {
  backend?.dispose();
  backend = undefined;
}
