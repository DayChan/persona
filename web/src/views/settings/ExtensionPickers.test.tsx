// @vitest-environment jsdom
// An extension's form picks one machine (format machine) and one launcher (format launcher) from the hub's lists by name
// instead of typing an id (#2385): x-os narrows the machines, hidden launchers are left out, a value naming none of them
// still shows, and launchers that could not be read are typed instead.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ detail: null as unknown, launchers: null as null | (() => Promise<unknown>) }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: vi.fn(() => Promise.resolve(hub.detail)),
  setExtensionConfig: vi.fn((_id: string, config: unknown) => Promise.resolve({ ...(hub.detail as object), config })),
}));
vi.mock("../../lib/launcher-list", async (original) => ({
  ...(await original<typeof import("../../lib/launcher-list")>()),
  loadLaunchers: vi.fn(() => hub.launchers!()),
}));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  const machines = [
    { id: "studio", name: "Mac Studio", online: true, os: "darwin" },
    { id: "server", name: "Home server", online: true, os: "linux" },
    { id: "pc", name: "Gaming PC", online: false, os: "windows" },
  ];
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), machines } as ReturnType<typeof real.getState>) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { pickerChoices, setExtensionConfig } from "../../lib/extensions";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

const chat = (config: Record<string, string>): HubExtensionDetail => ({
  id: "example-chat",
  name: "Example chat",
  kind: "process",
  status: "running",
  source: "installed",
  enabled: true,
  form: {
    fields: [
      { name: "agent_machine", type: "string", format: "machine", os: ["darwin", "linux"], title: "Where /agent runs", description: "Empty: the assistant's machine" },
      { name: "agent_launcher", type: "string", format: "launcher", title: "Launcher for /agent" },
    ],
  },
  config,
  secrets: [],
});

const launchers = [
  { name: "claude", source: "builtin" },
  { name: "work_gw", display_name: "Work gateway", source: "persona" },
  { name: "old_gw", source: "zshrc", hidden: true },
];

const select = (field: string) => host.querySelector<HTMLElement>(`[data-field="${field}"]`)!.querySelector("select");
const options = (s: HTMLSelectElement) => [...s.options].map((o) => `${o.value}=${o.textContent}`);
const choose = (s: HTMLSelectElement, value: string) => {
  s.value = value;
  s.dispatchEvent(new Event("change", { bubbles: true }));
};
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  vi.mocked(setExtensionConfig).mockClear();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("machine and launcher pickers", () => {
  it("lists the machines of the named systems and the shown launchers by name, and saves the ids", async () => {
    hub.detail = chat({});
    hub.launchers = () => Promise.resolve({ launchers });
    act(() => root.render(<Extensions open="example-chat" onOpen={() => {}} />));
    await flush();

    const machine = select("agent_machine")!;
    expect(options(machine)).toEqual(["=未设置", "studio=Mac Studio", "server=Home server"]);
    const launcher = select("agent_launcher")!;
    expect(options(launcher)).toEqual(["=未设置", "claude=claude", "work_gw=Work gateway (work_gw)"]);

    act(() => choose(machine, "server"));
    act(() => choose(launcher, "work_gw"));
    await act(async () => button("保存")!.click());
    await flush();
    expect(setExtensionConfig).toHaveBeenCalledWith("example-chat", { agent_machine: "server", agent_launcher: "work_gw" });
  });

  it("keeps a value that names no machine or launcher, to be changed", async () => {
    hub.detail = chat({ agent_machine: "old-mac", agent_launcher: "gone_gw" });
    hub.launchers = () => Promise.resolve({ launchers });
    act(() => root.render(<Extensions open="example-chat" onOpen={() => {}} />));
    await flush();
    expect(select("agent_machine")!.value).toBe("old-mac");
    expect(options(select("agent_machine")!)[1]).toBe("old-mac=old-mac（找不到这台机器）");
    expect(select("agent_launcher")!.value).toBe("gone_gw");
    expect(options(select("agent_launcher")!)[1]).toBe("gone_gw=gone_gw（找不到这个启动器）");
  });

  it("types the launcher when the hub's launchers could not be read", async () => {
    hub.detail = chat({ agent_launcher: "claude" });
    hub.launchers = () => Promise.reject(new Error("timeout"));
    act(() => root.render(<Extensions open="example-chat" onOpen={() => {}} />));
    await flush();
    expect(select("agent_launcher")).toBeNull();
    const input = host.querySelector<HTMLElement>('[data-field="agent_launcher"]')!.querySelector("input")!;
    expect(input.value).toBe("claude");
    expect(select("agent_machine")).not.toBeNull();
  });
});

describe("pickerChoices", () => {
  it("is null for other fields, and for launchers not in yet", () => {
    expect(pickerChoices({ name: "x", type: "string" }, undefined, [], [])).toBeNull();
    expect(pickerChoices({ name: "x", type: "string", format: "uri" }, undefined, [], [])).toBeNull();
    expect(pickerChoices({ name: "x", type: "array", items: { type: "string", format: "machine" } }, undefined, [], [])).toBeNull();
    expect(pickerChoices({ name: "x", type: "string", format: "launcher" }, undefined, [], null)).toBeNull();
    expect(pickerChoices({ name: "x", type: "string", format: "machine" }, undefined, [{ id: "a", name: "A" }], null)).toEqual([{ value: "a", label: "A" }]);
  });
});
