// @vitest-environment jsdom
// A boolean field with x-confirm asks its sentence before it is turned on: Cancel leaves it off, Turn On sets it in the
// draft (saved with Save, like any other field), and turning it off is not asked.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HubExtensionDetail } from "../../lib/extensions";

const hub = vi.hoisted(() => ({ detail: null as unknown }));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionDetail: vi.fn(() => Promise.resolve(hub.detail)),
  setExtensionConfig: vi.fn((_id: string, config: unknown) => Promise.resolve({ ...(hub.detail as object), config })),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { asksFirst, setExtensionConfig } from "../../lib/extensions";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

const SENTENCE = "Replies go out as you.";
const chat = (config: Record<string, boolean>): HubExtensionDetail => ({
  id: "example-chat",
  name: "Example chat",
  kind: "process",
  status: "running",
  source: "installed",
  enabled: true,
  form: { fields: [{ name: "agent", type: "boolean", title: "Reply as me", confirm: SENTENCE }] },
  config,
  secrets: [],
});

const toggle = () => host.querySelector<HTMLButtonElement>('[data-field="agent"] [role="switch"]')!;
const dialog = () => document.querySelector('[role="dialog"]');
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);

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

describe("x-confirm on a boolean field", () => {
  it("asks before turning it on; Cancel leaves it off, Turn On sets it and Save saves it", async () => {
    hub.detail = chat({});
    act(() => root.render(<Extensions open="example-chat" onOpen={() => {}} />));
    await flush();

    act(() => toggle().click());
    expect(dialog()?.textContent).toContain(SENTENCE);
    expect(dialog()?.textContent).toContain("Reply as me");
    act(() => button("取消")!.click());
    await flush();
    expect(toggle().getAttribute("aria-checked")).toBe("false");

    act(() => toggle().click());
    act(() => button("打开")!.click());
    await flush();
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    await act(async () => button("保存")!.click());
    await flush();
    expect(setExtensionConfig).toHaveBeenCalledWith("example-chat", { agent: true });
  });

  it("does not ask before turning it off", async () => {
    hub.detail = chat({ agent: true });
    act(() => root.render(<Extensions open="example-chat" onOpen={() => {}} />));
    await flush();
    act(() => toggle().click());
    expect(dialog()).toBeNull();
    expect(toggle().getAttribute("aria-checked")).toBe("false");
  });
});

describe("asksFirst", () => {
  it("asks only when turning on a field with a sentence", () => {
    expect(asksFirst({ name: "a", type: "boolean", confirm: SENTENCE }, true)).toBe(true);
    expect(asksFirst({ name: "a", type: "boolean", confirm: SENTENCE }, false)).toBe(false);
    expect(asksFirst({ name: "a", type: "boolean" }, true)).toBe(false);
    expect(asksFirst({ name: "a", type: "boolean", confirm: "" }, true)).toBe(false);
  });
});
