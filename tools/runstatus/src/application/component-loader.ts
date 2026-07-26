import type { Component } from "vue";
import type { ApplicationComponentRegistrySource } from "./types.js";

let installed: ApplicationComponentRegistrySource = {};

export function installApplicationComponents(
  components: Readonly<Record<string, unknown>>
): void {
  const next: Record<string, Component> = {};
  for (const [name, component] of Object.entries(components)) {
    if (!name.trim()) {
      throw new Error("application component names must not be empty");
    }
    if (!isVueComponent(component)) {
      throw new Error(`application component ${name} does not export a Vue component`);
    }
    next[name] = component;
  }
  installed = Object.freeze(next);
}

export function installedApplicationComponents(): ApplicationComponentRegistrySource {
  return installed;
}

function isVueComponent(value: unknown): value is Component {
  return typeof value === "function"
    || (typeof value === "object" && value !== null);
}
