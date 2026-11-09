// @vitest-environment jsdom
// "New agent"'s ultracode switch (#3428): under the thinking effort for Claude Code on a hub that has it, off each time
// the sheet opens (never remembered), grayed with why when the computer's Claude Code is too old, and sent only when on.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../lib/types";

const state = vi.hoisted(() => ({ loaded: true, features: ["ultracode"] as string[], machines: [] as Machine[] }));
vi.mock("../lib/store", () => ({
  getState: () => state,
  useStore: (selector: (value: typeof state) => unknown) => selector(state),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
const launched = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock("../lib/new-agent", async (original) => ({ ...(await original<typeof import("../lib/new-agent")>()), launchAgent: (input: Record<string, unknown>) => { launched.push(input); return "local-1"; } }));

import { NewAgent } from "./NewAgent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
let launcher = { name: "claude", agent: "claude" };
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const theSwitch = () => document.body.querySelector<HTMLButtonElement>('[data-testid="new-agent-ultracode"] [role="switch"]');
const button = (text: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;

async function open(key: string) {
  act(() => root.render(<NewAgent key={key} open onClose={() => {}} />));
  await flush();
}

beforeEach(() => {
  state.features = ["ultracode"];
  state.machines = [{ id: "mac", name: "Mac", online: true, claude_version: "2.1.320" }];
  launcher = { name: "claude", agent: "claude" };
  launched.length = 0;
  localStorage.clear();
  localStorage.setItem("new.cwd.mac", "/code/app");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) return json({ error: "not found" }, 404);
    if (url.includes("/launchers/") && url.includes("/models")) return json({ models: [] });
    if (url.includes("/launchers")) return json({ launchers: [launcher] });
    if (url.includes("/usage/windows")) return json({ accounts: [] });
    return json({ path: "/code", entries: [] });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("New agent's ultracode switch", () => {
  it("is off, and a start with it left off sends nothing", async () => {
    await open("a");
    expect(theSwitch()!.getAttribute("aria-checked")).toBe("false");
    expect(document.body.querySelector('[data-testid="new-agent-ultracode"]')!.textContent).toContain("每个任务都编排多 Agent workflow · 很费额度 · 只对本会话");
    act(() => button("启动").click());
    expect(launched).toHaveLength(1);
    expect(launched[0].ultracode).toBeUndefined();
  });

  it("turned on, the start asks for it", async () => {
    await open("b");
    act(() => theSwitch()!.click());
    expect(theSwitch()!.getAttribute("aria-checked")).toBe("true");
    act(() => button("启动").click());
    expect(launched[0]).toMatchObject({ machineId: "mac", cwd: "/code/app", ultracode: true });
  });

  it("is off again the next time the sheet opens: the choice is not remembered", async () => {
    act(() => root.render(<NewAgent key="c" open onClose={() => {}} />));
    await flush();
    act(() => theSwitch()!.click());
    act(() => root.render(<NewAgent key="c" open={false} onClose={() => {}} />));
    act(() => root.render(<NewAgent key="c" open onClose={() => {}} />));
    await flush();
    expect(theSwitch()!.getAttribute("aria-checked")).toBe("false");
    expect(JSON.stringify({ ...localStorage })).not.toContain("ultracode");
  });

  it("grayed with why when this computer's Claude Code is too old, and not sent", async () => {
    state.machines = [{ id: "mac", name: "Mac", online: true, claude_version: "2.1.250" }];
    await open("d");
    expect(theSwitch()!.disabled).toBe(true);
    expect(document.body.querySelector('[data-testid="new-agent-ultracode"] [data-ultracode-line]')!.textContent).toBe("这台电脑的 Claude Code 是 2.1.250，要 2.1.284 或更新才能开 ultracode");
    act(() => button("启动").click());
    expect(launched[0].ultracode).toBeUndefined();
  });

  it("not there on an older hub, nor for a Codex launcher", async () => {
    state.features = [];
    await open("e");
    expect(theSwitch()).toBeNull();
    act(() => root.unmount());
    root = createRoot(host);
    state.features = ["ultracode"];
    launcher = { name: "codex", agent: "codex" };
    await open("f");
    expect(theSwitch()).toBeNull();
  });
});
