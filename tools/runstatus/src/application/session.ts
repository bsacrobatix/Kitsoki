import type { ApplicationFrame } from "./types.js";

interface ApplicationSessionRPC {
  post<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
}

interface StoryHeader {
  readonly path: string;
  readonly app_id: string;
}

interface SessionCreated {
  readonly session_id: string;
}

// Resolve the current application session or create one from the backend's
// canonical story catalog. The generated application shell supplies an app id,
// never an absolute source path, so production bundles remain relocatable.
export async function ensureApplicationSession(
  rpc: ApplicationSessionRPC,
  applicationId: string,
  currentSessionId: string | null
): Promise<string | null> {
  if (!applicationId) return currentSessionId;

  if (currentSessionId) {
    try {
      const frame = await rpc.post<ApplicationFrame>("runstatus.application.frame", {
        session_id: currentSessionId,
      });
      if (frame.application_id === applicationId) return currentSessionId;
    } catch {
      // The current session belongs to a non-application story or is no longer live.
    }
  }

  const stories = await rpc.post<StoryHeader[]>("runstatus.stories.list");
  const matches = stories.filter((story) => story.app_id === applicationId);
  if (matches.length !== 1) {
    throw new Error(
      matches.length === 0
        ? `Application story "${applicationId}" is not available.`
        : `Application story "${applicationId}" is ambiguous.`
    );
  }
  const created = await rpc.post<SessionCreated>("runstatus.session.new", {
    story_path: matches[0].path,
  });
  return created.session_id;
}
