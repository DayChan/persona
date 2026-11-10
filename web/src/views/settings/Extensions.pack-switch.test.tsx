// @vitest-environment jsdom
// A pack's Enabled switch (#1830): on a hub where it takes the members along, switching it off names them first and
// changes nothing until confirmed; on an older hub it switches the pack alone and says so.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ detail: null as unknown, enabled: vi.fn() }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: () => Promise.resolve(hub.detail),
  setExtensionEnabled: (...args: unknown[]) => {
    hub.enabled(...args);
    return Promise.resolve({ ...(hub.detail as HubExtensionDetail), enabled: false, pack_off: ["base", "addon"] });
  },
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { Extensions } from "./Extensions";
import { enableFooter } from "./ExtensionSettingsPage";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const pack = (over: Partial<HubExtensionDetail> = {}): HubExtensionDetail => ({
  id: "group", name: "示例合集", kind: "extension_pack", status: "running", source: "installed", enabled: true,
  extension_pack: [{ id: "base" }, { id: "addon" }, { id: "gone" }], pack_members_follow: true,
  pack_on: [{ id: "base", name: "基础" }, { id: "addon", name: "附加" }], ...over,
});
const enabledSwitch = () => document.querySelector<HTMLButtonElement>('[data-extension-section="enabled"] [role=switch]')!;
const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === text);

beforeEach(() => {
  hub.enabled.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("合集的启用开关", () => {
  it("names the members and switches nothing until confirmed", async () => {
    hub.detail = pack();
    act(() => root.render(<Extensions open="group" onOpen={() => {}} />));
    await flush();
    expect(host.textContent).toContain("停用合集会一起停用它的成员");
    act(() => enabledSwitch().click());
    expect(document.body.textContent).toContain("停用「示例合集」和它的成员？");
    expect(document.body.textContent).toContain("它的成员也会一起停用：基础、附加。");
    expect(hub.enabled).not.toHaveBeenCalled();
    act(() => button("一起停用")!.click());
    await flush();
    expect(hub.enabled).toHaveBeenCalledWith("group", false, false);
    expect(document.body.textContent).not.toContain("停用「示例合集」和它的成员？");
    expect(host.textContent).toContain("重新启用合集会重新启用 base、addon");
  });

  it("cancelling leaves everything as it was", async () => {
    hub.detail = pack();
    act(() => root.render(<Extensions open="group" onOpen={() => {}} />));
    await flush();
    act(() => enabledSwitch().click());
    act(() => button("取消")!.click());
    expect(document.body.textContent).not.toContain("停用「示例合集」和它的成员？");
    expect(hub.enabled).not.toHaveBeenCalled();
  });

  it("switches at once on a hub that switches the pack alone, and says the members are switched on their own", async () => {
    hub.detail = pack({ pack_members_follow: undefined });
    act(() => root.render(<Extensions open="group" onOpen={() => {}} />));
    await flush();
    expect(host.textContent).toContain("停用合集只会停用合集本身");
    act(() => enabledSwitch().click());
    await flush();
    expect(hub.enabled).toHaveBeenCalledWith("group", false, false);
  });

  it("switches at once when no installed member is on, with nothing to name", async () => {
    hub.detail = pack({ pack_on: undefined });
    act(() => root.render(<Extensions open="group" onOpen={() => {}} />));
    await flush();
    act(() => enabledSwitch().click());
    expect(document.body.textContent).not.toContain("停用「示例合集」和它的成员？");
    await flush();
    expect(hub.enabled).toHaveBeenCalledWith("group", false, false);
  });

  it("keeps the ordinary footnote for an extension that is not a pack", () => {
    expect(enableFooter({ id: "x", name: "X", kind: "process", status: "running", enabled: true })).toContain("配置和密钥会保留");
  });
});
