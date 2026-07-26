import * as vscode from "vscode";
import {
  applicationBundleFrameHTML,
  type ApplicationBundle,
} from "./application-host";

// Application presentations are editor documents, distinct from the Kitsoki
// chat/trace/graph views. Keeping this adapter separate preserves the core
// surfaces' no-WebviewPanel invariant.
export function openApplicationBundlePanel(bundle: ApplicationBundle): vscode.WebviewPanel {
  const panel = vscode.window.createWebviewPanel(
    "kitsoki.application",
    `Kitsoki: ${bundle.application_id}`,
    vscode.ViewColumn.Active,
    { enableScripts: true },
  );
  panel.webview.html = applicationBundleFrameHTML(bundle);
  return panel;
}
