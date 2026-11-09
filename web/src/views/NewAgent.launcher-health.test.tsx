// @vitest-environment jsdom
// "New agent"'s launchers with their health on the computer (#1643): one this computer's terminal can't run is grayed out
// with why and never hidden, is not the one picked by default, opens how to fix it instead of being picked, and "Try
// anyway" picks it after all. An older hub sends no health and nothing changes.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Launcher, Machine } from "../lib/types";

const state = vi.hoisted(() => ({ loaded: true, features: [] as string[], machines: [] as Machine[] }));
vi.mock("../lib/store", () => ({
  useStore: (selector: (value: typeof state) => unknown) => selector(state),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
const launched = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock("../lib/new-agent", async (original) => ({ ...(await original<typeof import("../lib/new-agent")>()), launchAgent: (input: Record<string, unknown>) => { launched.push(input); return "local-1"; } }));
const navigated = vi.hoisted(() => [] as unknown[]);
vi.mock("../lib/router", async (original) => ({ ...(await original<typeof import("../lib/router")>()), navigate: (to: unknown) => { navigated.push(to); } }));

import { NewAgent } from "./NewAgent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const BLOCKED: Launcher = { name: "work_gw", health: { state: "blocked", kind: "not_in_terminal", error: "bash: work_gw: command not found", checked_at: "2026-11-09T08:00:00Z" } };
const OK: Launcher = { name: "claude", agent: "claude", health: { state: "ok", checked_at: "2026-11-09T08:00:00Z" } };
let launchers: Launcher[] = [];
let purpose: unknown = null;
let host: HTMLDivElement;
let root: Root;
let closed = 0;
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const q = <T extends Element = HTMLElement>(sel: string) => document.body.querySelector<T & HTMLElement>(sel);
const button = (text: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;
const chip = (name: string) => [...document.body.querySelectorAll("button[aria-pressed]")].find((b) => b.textContent?.trim() === name) as HTMLButtonElement | undefined;

async function open(key: string) {
  act(() => root.render(<NewAgent key={key} open onClose={() => { closed++; }} />));
  await flush();
}

beforeEach(() => {
  state.machines = [{ id: "mac", name: "Mac", online: true }];
  launchers = [BLOCKED, OK];
  purpose = null;
  launched.length = 0;
  navigated.length = 0;
  closed = 0;
  localStorage.clear();
  localStorage.setItem("new.cwd.mac", "/code/app");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) return purpose ? json(purpose) : json({ error: "not found" }, 404);
    if (url.includes("/launchers/") && url.includes("/models")) return json({ models: [] });
    if (url.includes("/launchers")) return json({ launchers });
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

describe("New agent's launcher health", () => {
  it("grays out the launcher the terminal can't run, says why, and picks one that can start", async () => {
    localStorage.setItem("new.launcher.mac", "work_gw"); // the last one used is blocked here now
    await open("a");
    const blocked = chip("work_gw")!;
    expect(blocked.getAttribute("aria-disabled")).toBe("true");
    expect(blocked.getAttribute("aria-pressed")).toBe("false");
    expect(blocked.title).toBe("这台电脑的终端里用不了");
    expect(chip("claude")!.getAttribute("aria-pressed")).toBe("true");
    expect(chip("claude")!.getAttribute("aria-disabled")).toBeNull();
    const line = q('[data-testid="new-agent-blocked"]')!;
    expect(line.textContent).toContain("work_gw");
    expect(line.textContent).toContain("这台电脑的终端里用不了");
    expect(line.textContent).toContain("怎么修好");
    expect(q('[data-testid="launcher-blocked-help"]')).toBeNull();
  });

  it("choosing it shows how to fix it instead of picking it, and Try anyway picks it", async () => {
    await open("b");
    act(() => chip("work_gw")!.click());
    expect(chip("claude")!.getAttribute("aria-pressed")).toBe("true");
    const help = q('[data-testid="launcher-blocked-help"]')!;
    expect(help.textContent).toContain("终端里用不了");
    expect(help.textContent).toContain("「work_gw」");
    expect(help.textContent).toContain("~/.bashrc");
    expect(q('[data-testid="launcher-blocked-detail"]')!.textContent).toContain("bash: work_gw: command not found");
    act(() => button("仍然使用").click());
    expect(chip("work_gw")!.getAttribute("aria-pressed")).toBe("true");
    expect(q('[data-testid="launcher-blocked-help"]')).toBeNull();
    // the reason stays said while it is the one that will run
    expect(q('[data-testid="new-agent-blocked"]')!.textContent).toContain("这台电脑的终端里用不了");
    act(() => button("启动").click());
    expect(launched[0]).toMatchObject({ machineId: "mac", launcher: "work_gw" });
  });

  it("Add model provider opens Settings › Model providers with the wizard", async () => {
    await open("c");
    act(() => button("怎么修好").click());
    act(() => button("添加模型供应商").click());
    expect(closed).toBe(1);
    expect(navigated).toEqual(["/settings?tab=models&add=1"]);
  });

  it("lists a lone blocked launcher too, rather than hiding the field", async () => {
    launchers = [BLOCKED];
    await open("d");
    expect(chip("work_gw")!.getAttribute("aria-disabled")).toBe("true");
    expect(q('[data-testid="new-agent-blocked"]')).not.toBeNull();
  });

  it("shows other blocked classes the same way, without the provider button", async () => {
    launchers = [OK, { name: "team_gw", health: { state: "blocked", kind: "auth", error: "401 invalid api key" } }, { name: "slow", health: { state: "failing", kind: "timeout" } }];
    await open("e");
    expect(chip("team_gw")!.getAttribute("aria-disabled")).toBe("true");
    expect(chip("slow")!.getAttribute("aria-disabled")).toBeNull(); // still the hub's to retry
    act(() => chip("team_gw")!.click());
    const help = q('[data-testid="launcher-blocked-help"]')!;
    expect(help.textContent).toContain("这台电脑上用不了");
    expect(help.textContent).not.toContain("添加模型供应商");
  });

  it("marks Model settings' default when the launcher it runs can't start here, and still starts with it", async () => {
    purpose = { purpose: { id: "agent.new", effective: { provider: "launcher.work_gw" } }, providers: [] };
    launchers = [OK, BLOCKED];
    await open("g");
    const mark = q('[data-testid="new-agent-default-blocked"]')!;
    const def = mark.closest("button")!;
    // still the choice, not grayed out: it is Model settings' to change
    expect(def.getAttribute("aria-pressed")).toBe("true");
    expect(def.getAttribute("aria-disabled")).toBeNull();
    expect(def.title).toContain("这台电脑的终端里用不了");
    // the line for the launcher this start runs comes first and says so
    const rows = [...document.body.querySelectorAll('[data-testid="new-agent-blocked"] li')];
    expect(rows[0].hasAttribute("data-runs")).toBe(true);
    expect(rows[0].textContent).toContain("本次使用");
    expect(rows[0].textContent).toContain("work_gw");
    expect(rows[0].textContent).toContain("这台电脑的终端里用不了");
    act(() => button("启动").click());
    expect(launched[0]).toMatchObject({ machineId: "mac", launcher: "" });
  });

  it("no mark on the default when what it runs can start; the blocked one is still said", async () => {
    purpose = { purpose: { id: "agent.new", effective: { provider: "launcher.claude" } }, providers: [] };
    await open("h");
    expect(q('[data-testid="new-agent-default-blocked"]')).toBeNull();
    expect(q('[data-testid="new-agent-blocked"] li[data-runs]')).toBeNull();
    expect(q('[data-testid="new-agent-blocked"]')!.textContent).not.toContain("本次使用");
    expect(q('[data-testid="new-agent-blocked"]')!.textContent).toContain("work_gw");
  });

  it("names the launcher on each How to fix for a screen reader, and points at the help it opens", async () => {
    launchers = [OK, BLOCKED, { name: "team_gw", health: { state: "blocked", kind: "auth" } }];
    await open("i");
    const fixes = [...document.body.querySelectorAll<HTMLButtonElement>('[data-testid="new-agent-blocked"] button')];
    expect(fixes.map((b) => b.getAttribute("aria-label"))).toEqual(["怎么修好 work_gw", "怎么修好 team_gw"]);
    act(() => fixes[1].click());
    expect(fixes[1].getAttribute("aria-expanded")).toBe("true");
    const help = q('[data-testid="launcher-blocked-help"]')!;
    expect(fixes[1].getAttribute("aria-controls")).toBe(help.id);
    // the cause itself is in the help, whole, not only in the line that can be narrow
    expect(q('[data-testid="launcher-blocked-reason"]')!.textContent).toBe("Key 或登录被拒绝了");
  });

  it("an older hub's launchers (no health) are offered as before", async () => {
    launchers = [{ name: "claude" }, { name: "work_gw" }];
    await open("f");
    expect(q('[data-testid="new-agent-blocked"]')).toBeNull();
    expect(chip("work_gw")!.getAttribute("aria-disabled")).toBeNull();
    act(() => chip("work_gw")!.click());
    expect(chip("work_gw")!.getAttribute("aria-pressed")).toBe("true");
  });
});
