/**
 * Unit tests for the global inbox Pinia store. The LiveSource is a fake (no
 * live server, no SSE): the store's job is to fold the global notification
 * feed into an unread list + counts, and to mutate read/dismiss OPTIMISTICALLY
 * while reconciling with the RPC result.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { setActivePinia, createPinia } from "pinia";
import { useInboxStore } from "../../src/stores/inbox.js";
import type {
  LiveSource,
  Notification,
  NotificationFrame,
} from "../../src/data/live-source.js";

function notif(over: Partial<Notification> = {}): Notification {
  return {
    ID: "n1",
    SessionID: "s1",
    CreatedAt: new Date().toISOString(),
    Severity: "info",
    Title: "Turn ready",
    Body: "",
    TeleportState: "idle",
    TeleportSlots: null,
    TeleportProposalID: "",
    TeleportJobID: "",
    OriginKind: "",
    OriginRef: "",
    ReadAt: null,
    DismissedAt: null,
    SnoozedUntil: null,
    OriginURL: null,
    ...over,
  };
}

function frame(over: Partial<NotificationFrame> = {}): NotificationFrame {
  return {
    session_id: "s1",
    notification: notif(),
    unread: 1,
    needs_attention: 0,
    ...over,
  };
}

function fakeSource(overrides: Record<string, unknown> = {}): LiveSource {
  return {
    readNotification: vi.fn().mockResolvedValue({ ok: true }),
    dismissNotification: vi.fn().mockResolvedValue({ ok: true }),
    ...overrides,
  } as unknown as LiveSource;
}

describe("inbox store", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("a notification frame prepends the item and sets counts", () => {
    const inbox = useInboxStore();
    inbox.onFrame(frame({ unread: 1, needs_attention: 0 }));
    expect(inbox.notifications).toHaveLength(1);
    expect(inbox.notifications[0].ID).toBe("n1");
    expect(inbox.unread).toBe(1);
    expect(inbox.needsAttention).toBe(0);
  });

  it("a second frame prepends (newest first) and takes the fresh counts", () => {
    const inbox = useInboxStore();
    inbox.onFrame(frame({ notification: notif({ ID: "n1" }), unread: 1 }));
    inbox.onFrame(
      frame({
        notification: notif({ ID: "n2", Severity: "action_required" }),
        unread: 2,
        needs_attention: 1,
      })
    );
    expect(inbox.notifications.map((n) => n.ID)).toEqual(["n2", "n1"]);
    expect(inbox.unread).toBe(2);
    expect(inbox.needsAttention).toBe(1);
    expect(inbox.hasNeedsAttention).toBe(true);
  });

  it("toasts only for success / action_required", () => {
    const inbox = useInboxStore();
    inbox.onFrame(frame({ notification: notif({ ID: "i", Severity: "info" }) }));
    expect(inbox.toast).toBeNull();
    inbox.onFrame(
      frame({ notification: notif({ ID: "s", Severity: "success" }) })
    );
    expect(inbox.toast?.ID).toBe("s");
    inbox.onFrame(
      frame({ notification: notif({ ID: "a", Severity: "action_required" }) })
    );
    expect(inbox.toast?.ID).toBe("a");
  });

  it("de-dupes a repeated push by id", () => {
    const inbox = useInboxStore();
    inbox.onFrame(frame({ notification: notif({ ID: "n1" }), unread: 1 }));
    inbox.onFrame(frame({ notification: notif({ ID: "n1" }), unread: 1 }));
    expect(inbox.notifications).toHaveLength(1);
  });

  it("markRead optimistically decrements unread and calls the RPC", async () => {
    const inbox = useInboxStore();
    const src = fakeSource();
    inbox.onFrame(
      frame({ notification: notif({ ID: "n1", Severity: "action_required" }), unread: 1, needs_attention: 1 })
    );
    await inbox.markRead(src, "s1", "n1");
    expect(inbox.unread).toBe(0);
    expect(inbox.needsAttention).toBe(0);
    expect(inbox.notifications[0].ReadAt).toBeTruthy();
    expect(src.readNotification).toHaveBeenCalledWith("s1", "n1");
  });

  it("markRead is a no-op on an already-read item", async () => {
    const inbox = useInboxStore();
    const src = fakeSource();
    inbox.onFrame(frame({ notification: notif({ ID: "n1" }), unread: 1 }));
    await inbox.markRead(src, "s1", "n1");
    expect(inbox.unread).toBe(0);
    await inbox.markRead(src, "s1", "n1");
    expect(inbox.unread).toBe(0); // not driven negative
  });

  it("dismiss optimistically removes the item, adjusts counts, calls RPC", async () => {
    const inbox = useInboxStore();
    const src = fakeSource();
    inbox.onFrame(
      frame({ notification: notif({ ID: "n1", Severity: "action_required" }), unread: 1, needs_attention: 1 })
    );
    await inbox.dismiss(src, "s1", "n1");
    expect(inbox.notifications).toHaveLength(0);
    expect(inbox.unread).toBe(0);
    expect(inbox.needsAttention).toBe(0);
    expect(src.dismissNotification).toHaveBeenCalledWith("s1", "n1");
  });

  it("dismiss restores the item when the RPC reports !ok", async () => {
    const inbox = useInboxStore();
    const src = fakeSource({
      dismissNotification: vi.fn().mockResolvedValue({ ok: false }),
    });
    inbox.onFrame(frame({ notification: notif({ ID: "n1" }), unread: 1 }));
    await inbox.dismiss(src, "s1", "n1");
    expect(inbox.notifications.map((n) => n.ID)).toEqual(["n1"]);
  });

  it("toggle / close drive the panel open state", () => {
    const inbox = useInboxStore();
    expect(inbox.open).toBe(false);
    inbox.toggle();
    expect(inbox.open).toBe(true);
    inbox.close();
    expect(inbox.open).toBe(false);
  });
});
