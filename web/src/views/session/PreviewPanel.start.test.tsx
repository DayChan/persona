// @vitest-environment jsdom
// Waiting for a started dev server: the 1.5 s checks of /preview/servers after Start, and what Cancel does to one that is out.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The first read (the panel opening) answers at once with the server not running; every later read waits for the test.
const hub = vi.hoisted(() => ({ calls: 0, reads: [] as { answer: (listening: boolean) => void; fail: () => void }[] }));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  const reply = (listening: boolean) => ({ servers: [{ name: "web", port: 5173, listening, command: ["npm", "run", "dev"] }] });
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (path.includes("/preview/servers")) {
        hub.calls++;
        if (hub.calls === 1) return reply(false);
        return new Promise((resolve, reject) =>
          hub.reads.push({ answer: (listening) => resolve(reply(listening)), fail: () => reject(new real.ApiError(502, "node offline", { error: "node offline" })) }),
        );
      }
      if (path.includes("/preview/start")) return { pane: "pane", terminal: "term", port: 5173 };
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  const state = {
    ...real.getState(),
    sessions: { m1: [{ machine_id: "m1", session_id: "s1", workspace_id: "w1", cwd: "/project" }] },
    machines: [{ id: "m1", name: "box", online: true }],
  };
  return { ...real, useStore: <T,>(select: (s: typeof state) => T): T => select(state) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { t } from "../../i18n";
import { toast } from "../../ui/atoms";
import { PreviewPanel } from "./PreviewPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let visibility: DocumentVisibilityState;

beforeEach(() => {
  hub.calls = 0;
  hub.reads = [];
  visibility = "visible";
  sessionStorage.clear();
  vi.mocked(toast).mockClear();
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("WebSocket", class { binaryType = ""; readyState = 0; send() {} close() {} addEventListener() {} removeEventListener() {} });
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(document, "visibilityState");
});

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
};
// The panel's own buttons are in `host`; the Start dialog is a portal on the body.
const button = (scope: ParentNode, label: string) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement;
const tick = async (ms: number) => {
  await act(async () => void vi.advanceTimersByTime(ms));
  await flush();
};

/** Opens the panel on the server list, starts "web" and waits on its "Starting…" screen. */
async function startWeb() {
  act(() => root.render(<PreviewPanel mid="m1" sid="s1" cwd="/project" onClose={() => {}} />));
  await flush();
  expect(host.textContent).toContain(t("preview.choose"));
  await act(async () => button(host, t("preview.start_short")).click());
  await act(async () => button(document.body, t("preview.start")).click());
  await flush();
  expect(host.textContent).toContain(t("preview.starting", { name: "web" }));
}

describe("PreviewPanel while a started server comes up", () => {
  it("stays on the server list when the check out at Cancel finds the server listening", async () => {
    await startWeb();
    await tick(1500);
    expect(hub.reads).toHaveLength(1);

    await act(async () => button(host, t("preview.cancel")).click());
    expect(host.textContent).toContain(t("preview.choose"));
    await act(async () => hub.reads[0].answer(true));
    await flush();

    expect(host.textContent).toContain(t("preview.choose"));
    expect(host.textContent).not.toContain("localhost:5173");
    expect(host.querySelector("[data-preview-canvas]")).toBeNull();
    expect(sessionStorage.getItem("persona.preview.m1/s1")).toBeNull();
  });

  it("stays on the server list when the check out at Cancel fails", async () => {
    await startWeb();
    await tick(1500);
    await act(async () => button(host, t("preview.cancel")).click());
    await act(async () => hub.reads[0].fail());
    await flush();

    expect(host.textContent).toContain(t("preview.choose"));
    expect(host.textContent).not.toContain(t("preview.load_failed"));
  });

  it("reads again only after the last read answered, and says the server is slow once", async () => {
    await startWeb();
    await tick(1500);
    await tick(91_000);
    expect(hub.reads).toHaveLength(1);

    await act(async () => hub.reads[0].answer(false));
    await flush();
    expect(toast).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith(t("preview.start_slow", { name: "web", port: 5173 }), "error");
    expect(host.textContent).toContain(t("preview.choose"));
    await tick(4500);
    expect(hub.reads).toHaveLength(1);
  });

  it("opens the page once a later check finds it listening", async () => {
    await startWeb();
    await tick(1500);
    await act(async () => hub.reads[0].answer(false));
    await flush();
    await tick(1500);
    expect(hub.reads).toHaveLength(2);

    await act(async () => hub.reads[1].answer(true));
    await flush();
    expect(host.textContent).toContain("box · localhost:5173");
    expect(sessionStorage.getItem("persona.preview.m1/s1")).toBe(JSON.stringify({ port: 5173, path: "/" }));
  });

  it("does not check while the tab is hidden", async () => {
    await startWeb();
    visibility = "hidden";
    await tick(6000);
    expect(hub.reads).toHaveLength(0);

    visibility = "visible";
    await tick(1500);
    expect(hub.reads).toHaveLength(1);
  });
});
