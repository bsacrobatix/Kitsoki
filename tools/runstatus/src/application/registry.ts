import { markRaw, toRaw } from "vue";
import type {
  ApplicationBody,
  ApplicationComponentRegistration,
  ApplicationComponentRegistrySource,
} from "./types.js";

export interface ResolvedApplicationComponent {
  readonly registration?: ApplicationComponentRegistration;
  readonly fallback?: ApplicationBody;
}

export class ApplicationComponentRegistry {
  readonly #entries = new Map<string, ApplicationComponentRegistration>();

  constructor(source: ApplicationComponentRegistrySource = {}) {
    for (const [name, entry] of Object.entries(source)) {
      this.register(
        name,
        typeof entry === "object" && entry !== null && ("component" in entry || "fallback" in entry)
          ? entry as ApplicationComponentRegistration
          : { component: entry }
      );
    }
  }

  register(name: string, registration: ApplicationComponentRegistration): void {
    if (!name.trim()) {
      throw new Error("application component names must not be empty");
    }
    this.#entries.set(name, registration.component
      ? { ...registration, component: markRaw(toRaw(registration.component)) }
      : registration);
  }

  resolve(name: string, bodyFallback?: ApplicationBody): ResolvedApplicationComponent {
    const registration = this.#entries.get(name);
    return {
      registration,
      fallback: bodyFallback ?? registration?.fallback,
    };
  }
}

export function createApplicationComponentRegistry(
  source: ApplicationComponentRegistrySource = {}
): ApplicationComponentRegistry {
  return new ApplicationComponentRegistry(source);
}
