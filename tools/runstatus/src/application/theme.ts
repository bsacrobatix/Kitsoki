import type { CSSProperties } from "vue";

export interface ApplicationTheme {
  readonly background?: string;
  readonly foreground?: string;
  readonly muted?: string;
  readonly accent?: string;
  readonly danger?: string;
  readonly success?: string;
  readonly warning?: string;
  readonly border?: string;
  readonly paper?: string;
}

const variables: Readonly<Record<keyof ApplicationTheme, string>> = {
  background: "--k-bg",
  foreground: "--k-fg",
  muted: "--k-fg-muted",
  accent: "--k-fg-accent",
  danger: "--k-danger",
  success: "--k-success",
  warning: "--k-warning",
  border: "--k-border",
  paper: "--k-paper-bg",
};

let installed: ApplicationTheme = {};

export function installApplicationTheme(theme: ApplicationTheme): void {
  const input = theme as Readonly<Record<string, unknown>>;
  for (const token of Object.keys(input)) {
    if (!(token in variables)) {
      throw new Error(`Application theme token "${token}" is not scoped.`);
    }
  }
  const normalized: Record<string, string> = {};
  for (const token of Object.keys(variables) as (keyof ApplicationTheme)[]) {
    const value = input[token];
    if (value === undefined) continue;
    if (typeof value !== "string" || value.trim() === "") {
      throw new Error(`Application theme token "${token}" must be a non-empty string.`);
    }
    normalized[token] = value;
  }
  installed = Object.freeze(normalized);
}

export function installedApplicationTheme(): ApplicationTheme {
  return installed;
}

export function applicationThemeStyle(theme: ApplicationTheme): CSSProperties {
  const style: Record<string, string> = {};
  for (const [token, variable] of Object.entries(variables)) {
    const value = theme[token as keyof ApplicationTheme];
    if (value) style[variable] = value;
  }
  return style;
}
