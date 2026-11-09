// @vitest-environment jsdom
// Settings › Devices › Connect a new device: while the code is live the dialog looks for the device that used it every 3 s,
// only while the tab is shown, and once right away on coming back so a device paired meanwhile shows at once.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ reads: 0, tokens: [] as { id: string; name?: string }[] }));
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: vi.fn(async (path: string) => {
    if (path === "/auth/tokens") return (hub.reads++, { tokens: [...hub.tokens] });
    if (path === "/auth/pair-codes") return { token: "jp_x", expires_at: new Date(Date.now() + 600_000).toISOString(), pair_url: "persona://pair?token=jp_x" };
    throw new Error(`unexpected ${path}`);
  }),
}));
import { t } from "../../i18n";
import { ConnectDevice } from "./ConnectDevice";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let visibility: DocumentVisibilityState = "visible";
const onPaired = vi.fn();
const text = () => document.body.textContent ?? "";
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const wait = async (ms: number) => {
  await act(async () => { vi.advanceTimersByTime(ms); });
  await flush();
};
const show = async (v: DocumentVisibilityState) => {
  visibility = v;
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  await flush();
};
const paired = (name: string) => t("settings-connectdevice.paired", { name });

async function openDialog() {
  await act(async () => root.render(<ConnectDevice open onClose={() => {}} onPaired={onPaired} />));
  await flush();
}

beforeEach(() => {
  hub.reads = 0;
  hub.tokens = [{ id: "phone", name: "Phone" }];
  visibility = "visible";
  onPaired.mockClear();
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }));
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  Reflect.deleteProperty(document, "visibilityState");
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("ConnectDevice", () => {
  it("looks for the device that used the code every 3 s and names it", async () => {
    await openDialog();
    expect(hub.reads).toBe(1); // the devices there before the code
    await wait(3000);
    expect(hub.reads).toBe(2);
    expect(onPaired).not.toHaveBeenCalled();
    hub.tokens.push({ id: "mac", name: "Work Mac" });
    await wait(3000);
    expect(hub.reads).toBe(3);
    expect(text()).toContain(paired("Work Mac"));
    expect(onPaired).toHaveBeenCalledTimes(1);
  });

  it("does not look while the tab is hidden", async () => {
    await openDialog();
    await show("hidden");
    await wait(9000);
    expect(hub.reads).toBe(1);
  });

  it("looks once right away on coming back, so a device paired meanwhile shows at once", async () => {
    await openDialog();
    await show("hidden");
    await wait(9000);
    hub.tokens.push({ id: "mac", name: "Work Mac" });
    await show("visible");
    expect(hub.reads).toBe(2);
    expect(text()).toContain(paired("Work Mac"));
    expect(onPaired).toHaveBeenCalledTimes(1);
    await wait(9000); // paired: nothing left to look for
    expect(hub.reads).toBe(2);
  });
});
