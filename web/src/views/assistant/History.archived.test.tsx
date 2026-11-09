// @vitest-environment jsdom
// "All chats"' "Archived" follows the archive mark the hub pushes on the live sessions, both ways: a conversation brought back
// on another device (or by a message sent to it, or by Persona) leaves "Archived" without the page being opened again, and
// one archived here shows there at once, before the hub has pushed the sessions again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../../lib/types";

const hub = vi.hoisted(() => ({
  fetched: [] as { session_id: string; archived?: boolean }[],
  state: { assistant: null as unknown, sessions: {} as Record<string, unknown[]> },
  listeners: new Set<() => void>(),
  posted: [] as string[],
  /** When set, the archive POST waits for it. */
  hold: null as Promise<void> | null,
  /** When set, the hub is unreachable: every request fails, the archive POST and the reload after it. */
  down: false,
}));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string, init?: { method?: string }) => {
      if (init?.method === "POST") hub.posted.push(path);
      if (hub.down) throw new real.ApiError(0, "offline", {});
      if (path === "/assistant?archived=1") return { conversations: hub.fetched };
      if (init?.method === "POST") {
        await hub.hold;
        return { ok: true };
      }
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  const { useSyncExternalStore } = await import("react");
  return {
    ...real,
    refreshAssistant: async () => {},
    // the real state, with this test's assistant and sessions
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T =>
      useSyncExternalStore(
        (l) => (hub.listeners.add(l), () => void hub.listeners.delete(l)),
        () => select({ ...real.getState(), ...hub.state } as ReturnType<typeof real.getState>),
      ),
  };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { History } from "./History";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

const conv = (sid: string, p: Partial<Session> = {}): Session => ({
  machine_id: "server", session_id: sid, title: `对话 ${sid}`, cwd: "/home/me/.persona/assistant", status: "idle", assistant: true,
  updated_at: "2026-11-01T09:00:00Z", ...p,
} as Session);
const push = async (sessions: Session[]) => {
  hub.state = { ...hub.state, sessions: { server: sessions } };
  await act(async () => hub.listeners.forEach((l) => l()));
};

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const archivedHeading = () => [...host.querySelectorAll("button[aria-expanded]")].find((b) => b.textContent?.includes("已归档"))?.textContent ?? "";
const mount = async () => {
  root = createRoot(host);
  act(() => root.render(<History onToggleSidebar={() => {}} />));
  await flush();
};
/** Picks `label` in the menu of the row titled `对话 <sid>`. */
const choose = async (sid: string, label: string) => {
  const row = [...host.querySelectorAll("a")].find((a) => a.textContent?.includes(`对话 ${sid}`))!.parentElement!;
  await act(async () => (row.querySelector('button[aria-label="更多"]') as HTMLButtonElement).click());
  const item = [...document.querySelectorAll('[role="menuitem"], button')].find((b) => b.textContent?.trim() === label) as HTMLElement;
  await act(async () => item.click());
  await flush();
};

beforeEach(async () => {
  hub.posted = [];
  hub.hold = null;
  hub.down = false;
  hub.fetched = [conv("main"), conv("back", { archived: true } as Partial<Session>), conv("keep")];
  hub.state = {
    assistant: { machine_id: "server", main: "main", conversations: [conv("main"), conv("keep")] },
    sessions: { server: [conv("main"), conv("back", { archived: true } as Partial<Session>), conv("keep")] },
  };
  host = document.createElement("div");
  document.body.append(host);
  await mount();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("全部对话's 已归档", () => {
  it("lets go of a conversation brought back elsewhere", async () => {
    expect(archivedHeading()).toContain("已归档（1）");
    await push([conv("main"), conv("back"), conv("keep")]); // the hub's push: no mark any more
    expect(archivedHeading()).toBe("");
  });

  it("shows one archived here there at once, before the hub pushes the sessions again", async () => {
    let release!: () => void;
    hub.hold = new Promise<void>((r) => (release = r));
    const row = [...host.querySelectorAll("a")].find((a) => a.textContent?.includes("对话 keep"))!.parentElement!;
    await act(async () => (row.querySelector('button[aria-label="更多"]') as HTMLButtonElement).click());
    const item = [...document.querySelectorAll('[role="menuitem"], button')].find((b) => b.textContent?.trim() === "归档") as HTMLElement;
    await act(async () => item.click());
    await flush();
    expect(hub.posted).toEqual(["/assistant/conversations/keep/archive"]);
    expect(archivedHeading()).toContain("已归档（2）"); // the live session still has no mark
    hub.fetched = hub.fetched.map((c) => (c.session_id === "keep" ? { ...c, archived: true } : c));
    await push([conv("main"), conv("back", { archived: true } as Partial<Session>), conv("keep", { archived: true } as Partial<Session>)]);
    await act(async () => release());
    await flush();
    expect(archivedHeading()).toContain("已归档（2）");
  });

  it("keeps one archived here when the hub answers before it pushes the sessions again", async () => {
    const row = [...host.querySelectorAll("a")].find((a) => a.textContent?.includes("对话 keep"))!.parentElement!;
    await act(async () => (row.querySelector('button[aria-label="更多"]') as HTMLButtonElement).click());
    const item = [...document.querySelectorAll('[role="menuitem"], button')].find((b) => b.textContent?.trim() === "归档") as HTMLElement;
    hub.fetched = hub.fetched.map((c) => (c.session_id === "keep" ? { ...c, archived: true } : c));
    await act(async () => item.click());
    await flush(); // answered and read again; the live session still has no mark
    expect(archivedHeading()).toContain("已归档（2）");
    await push([conv("main"), conv("back", { archived: true } as Partial<Session>), conv("keep", { archived: true } as Partial<Session>)]);
    expect(archivedHeading()).toContain("已归档（2）");
    await push([conv("main"), conv("back", { archived: true } as Partial<Session>), conv("keep")]); // brought back elsewhere later
    expect(archivedHeading()).toContain("已归档（1）");
  });
});

// One the node no longer lists goes only by what was read: when the hub refuses the change and cannot be read again
// either (unreachable), the row stays where it was.
describe("全部对话 when the hub is unreachable", () => {
  beforeEach(async () => {
    act(() => root.unmount());
    hub.fetched = [...hub.fetched, conv("old"), conv("gone", { archived: true } as Partial<Session>)];
    await mount();
  });

  it("puts one back when archiving it fails", async () => {
    expect(archivedHeading()).toContain("已归档（2）");
    hub.down = true;
    await choose("old", "归档");
    expect(hub.posted).toEqual(["/assistant/conversations/old/archive"]);
    expect(archivedHeading()).toContain("已归档（2）");
  });

  it("keeps one archived when restoring it fails", async () => {
    await act(async () => ([...host.querySelectorAll("button[aria-expanded]")].find((b) => b.textContent?.includes("已归档")) as HTMLButtonElement).click());
    hub.down = true;
    await choose("gone", "取消归档");
    expect(hub.posted).toEqual(["/assistant/conversations/gone/unarchive"]);
    expect(archivedHeading()).toContain("已归档（2）");
  });
});
