// @vitest-environment jsdom
// Selection/lifecycle regression tests, not real transport or browser acceptance.
import { act, StrictMode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const gate = vi.hoisted(() => ({ ready: false, features: [] as string[], listeners: new Set<() => void>(), draws: vi.fn() }));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  useStore: (select: (state: { features: string[] }) => unknown) => select({ features: gate.features }),
  appSocketReady: () => gate.ready,
  onAppSocket: (listener: () => void) => { gate.listeners.add(listener); return () => { gate.listeners.delete(listener); }; },
}));
vi.mock("../../lib/extension-ui", async (original) => ({
  ...(await original<typeof import("../../lib/extension-ui")>()),
  renderUI: (...args: unknown[]) => { gate.draws(...args); return Promise.resolve({ sections: [{ items: [{ type: "text", text: "Legacy whole page" }] }] }); },
}));
vi.mock("./NativeExtensionUI", () => ({ NativeExtensionUI: function NativePage() {
  const [identity] = useState(() => "retained-native-page");
  return <div data-native-page={identity}>Native page</div>;
} }));
import { ExtensionUI } from "./ExtensionView";
let host: HTMLDivElement, root: Root;
async function hello(features: string[]) {
  await act(async () => { gate.features = features; gate.ready = true; gate.listeners.forEach((fn) => fn()); });
}
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  gate.ready = false; gate.features = []; gate.listeners.clear(); gate.draws.mockClear();
  localStorage.setItem("persona.token", "synthetic-token");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); localStorage.removeItem("persona.token"); vi.useRealTimers(); });
describe("extension page waits for authenticated hello", () => {
  it("bounds failed WebSocket upgrades, mounts one HTTP page and retains it after a late hello", async () => {
    vi.useFakeTimers();
    await act(async () => root.render(<ExtensionUI extension="example" ui="main" history={false} />));
    await act(async () => vi.advanceTimersByTime(4999));
    expect(gate.draws).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTime(1));
    expect(gate.draws).toHaveBeenCalledTimes(1);
    expect(host.textContent).toContain("Legacy whole page");
    await hello(["extension_ui_stream_v1"]);
    expect(gate.draws).toHaveBeenCalledTimes(1);
    expect(host.querySelector("[data-native-page]")).toBeNull();
  });
  it("does not allocate an invisible HTTP page while the native hello is in flight, even in StrictMode", async () => {
    await act(async () => root.render(<StrictMode><ExtensionUI extension="example" ui="main" history={false} /></StrictMode>));
    expect(gate.draws).not.toHaveBeenCalled();
    await hello(["extension_ui_stream_v1"]);
    // The native page is its own chunk: no HTTP page is allocated while it loads.
    expect(gate.draws).not.toHaveBeenCalled();
    await act(async () => { await import("./NativeExtensionUI"); });
    expect(host.textContent).toBe("Native page");
    expect(gate.draws).not.toHaveBeenCalled();
    const native = host.querySelector("[data-native-page]");
    await act(async () => { gate.ready = false; gate.listeners.forEach((fn) => fn()); });
    expect(host.querySelector("[data-native-page]")).toBe(native);
    await hello(["extension_ui_stream_v1"]);
    expect(host.querySelector("[data-native-page]")).toBe(native);
  });
  it("uses the unchanged HTTP whole-page path after a real legacy hello without the stream feature", async () => {
    await act(async () => root.render(<ExtensionUI extension="example" ui="main" history={false} />));
    expect(gate.draws).not.toHaveBeenCalled();
    await hello([]);
    expect(gate.draws).toHaveBeenCalledTimes(1);
    expect(host.textContent).toContain("Legacy whole page");
  });
  it("does not opt embedded slots into native page sessions", async () => {
    await act(async () => root.render(<ExtensionUI extension="example" ui="main" embedded />));
    expect(gate.draws).toHaveBeenCalledTimes(1);
    expect(host.textContent).toContain("Legacy whole page");
  });
});
