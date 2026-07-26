import type { InjectionKey } from "vue";
import { inject } from "vue";
import type {
  ApplicationActionDispatcher,
  ApplicationFrame,
} from "./types.js";
import type { ApplicationComponentRegistry } from "./registry.js";

export interface ApplicationRuntime {
  readonly frame: ApplicationFrame;
  readonly dispatch: ApplicationActionDispatcher;
  readonly components: ApplicationComponentRegistry;
  readonly stale: boolean;
  readonly isActionPending: (action: string) => boolean;
}

export const applicationRuntimeKey: InjectionKey<ApplicationRuntime> =
  Symbol("kitsoki.application.runtime");

export function useApplicationRuntime(): ApplicationRuntime {
  const runtime = inject(applicationRuntimeKey);
  if (!runtime) {
    throw new Error("useApplicationRuntime must be called inside an application renderer");
  }
  return runtime;
}
