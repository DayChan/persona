// @vitest-environment jsdom
// #5584: the Persona chat's model submenu is one choice of a set: exactly one row is checked, for the eye and for screen
// readers, even though the launcher's own default entry picks the same as "Default model".
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({
    matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});
vi.mock("../../lib/launcher-models", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/launcher-models")>()),
  machineLaunchers: vi.fn(async () => ({ launchers: [{ name: "Claude", agent: "claude", model: "opus" }] })),
  launcherModels: vi.fn(async () => ({
    locked: typedBefore.locked,
    models: [
      { id: "default", name: "Default (recommended)", description: "Use the default model (currently opus)", default: true },
      { id: "haiku", name: "Haiku", description: "Fastest for quick answers" },
      ...typedBefore.ids.map((id) => ({ id, name: id, description: "之前用过的模型 ID", custom: true })),
    ],
  })),
  forgetCustomModel: vi.fn(async () => {
    if (typedBefore.fail) throw new Error("数据库暂时不可用");
    return { ok: true };
  }),
}));
const typedBefore = vi.hoisted(() => ({ ids: [] as string[], fail: false, locked: undefined as string | undefined }));

import { AssistantLauncherChip } from "./AssistantChat";
import { pickedModel, pickModel } from "../../lib/assistant-chat";
import { forgetCustomModel } from "../../lib/launcher-models";
import { Toasts } from "../../ui/atoms";
import { getState } from "../../lib/store";
import type { Session } from "../../lib/types";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const session = { machine_id: "server", session_id: "s-models", assistant: true, agent: "claude" } as unknown as Session;
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  Object.assign(getState(), { features: ["typed_models"] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  pickModel("server/s-models", undefined);
  typedBefore.ids = [];
  typedBefore.fail = false;
  typedBefore.locked = undefined;
  vi.mocked(forgetCustomModel).mockClear();
});

async function openModels() {
  act(() => root.render(<AssistantLauncherChip session={session} />));
  await act(async () => host.querySelector<HTMLButtonElement>(`button[title="${t("assistant-assistantchat.launcher_chip_title")}"]`)!.click());
  const claude = [...document.querySelectorAll<HTMLButtonElement>('[role="menu"] button')].find((b) => b.textContent?.includes("Claude"))!;
  await act(async () => claude.click());
  return [...document.querySelectorAll<HTMLButtonElement>('[role="menu"] [role="menuitemradio"]')];
}

const checked = (rows: HTMLElement[]) => rows.filter((r) => r.getAttribute("aria-checked") === "true").map((r) => (r.querySelector("span.block") ?? r.querySelector("span"))!.textContent);

describe("Persona chat model submenu (#5584)", () => {
  it("checks only Default model when nothing is picked, not the launcher's default entry too", async () => {
    const rows = await openModels();
    expect(rows).toHaveLength(4); // Default model, the launcher's two, Other model… (#1644)
    expect(checked(rows)).toEqual([t("assistant-assistantchat.default_model")]);
    expect(rows.filter((r) => r.querySelector("svg.text-ink"))).toHaveLength(1);
  });

  it("checks the picked model alone", async () => {
    const rows = await openModels();
    // picking a launcher resets its model, so pick after the submenu is open (as choosing the row does)
    act(() => pickModel("server/s-models", "haiku"));
    expect(checked(rows)).toEqual(["Haiku"]);
  });
});

const menuButton = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="menu"] button')].find((b) => b.textContent?.includes(text));

function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const enter = (el: HTMLElement) => act(async () => { el.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); });

describe("Persona chat: a typed model ID (#1644)", () => {
  it("Other model… says why an ID can't be used as it is typed, and picks a good one for the next messages", async () => {
    await openModels();
    await act(async () => menuButton(t("assistant-assistantchat.other_model"))!.click());
    const input = document.querySelector<HTMLInputElement>('[role="menu"] input')!;
    type(input, "vendor model");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(document.querySelector('[role="menu"] [role="alert"]')!.textContent).toBe("模型 ID 里不能有空格");
    await enter(input);
    expect(pickedModel("server/s-models")).toBeUndefined(); // Enter waits for a good one
    type(input, "~vendor/model-latest[1m]");
    expect(document.querySelector('[role="menu"] [role="alert"]')).toBeNull();
    await enter(input);
    expect(pickedModel("server/s-models")).toBe("~vendor/model-latest[1m]");
  });

  it("offers Other model… only where the hub keeps a typed pick (typed_models)", async () => {
    Object.assign(getState(), { features: ["ultracode"] });
    const rows = await openModels();
    expect(rows).toHaveLength(3); // Default model and the launcher's two
    expect(menuButton(t("assistant-assistantchat.other_model"))).toBeUndefined();
  });

  it("says why, instead of Other model…, when the launcher passes its own --model", async () => {
    typedBefore.locked = "启动器固定了模型参数";
    await openModels();
    expect(menuButton(t("assistant-assistantchat.other_model"))).toBeUndefined();
    expect(document.querySelector('[role="menu"] [data-menu-note]')!.textContent).toContain("启动器固定了模型参数");
  });

  it("lists the IDs typed before under the launcher's own, and forgets one (back when the hub says no)", async () => {
    typedBefore.ids = ["vendor/model-x"];
    const rows = await openModels();
    expect(rows.map((r) => r.textContent)).toEqual(expect.arrayContaining([expect.stringContaining("vendor/model-x")]));
    // under their own heading (not a row), drawn as typed, without the line each would repeat
    const heading = [...document.querySelectorAll('[role="menu"] [role="presentation"]')].find((e) => e.textContent === t("assistant-assistantchat.typed_before"));
    expect(heading).toBeTruthy();
    const typedRow = rows.find((r) => r.textContent?.includes("vendor/model-x"))!;
    expect(typedRow.querySelector(".font-mono")).not.toBeNull();
    expect(typedRow.textContent).not.toContain("之前用过的模型 ID");
    await act(async () => menuButton(t("assistant-assistantchat.forget_typed"))!.click());
    typedBefore.fail = true;
    const menus = document.querySelectorAll('[role="menu"]');
    expect(menus.length).toBe(2); // the submenu of the typed IDs beside the models
    const row = [...menus[1].querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("vendor/model-x"))!;
    await act(async () => row.click());
    expect(forgetCustomModel).toHaveBeenCalledWith("server", "Claude", "vendor/model-x");
    await act(async () => { for (let i = 0; i < 4; i++) await Promise.resolve(); });
    // the menu closed at once; the refusal is said
    const shown = document.createElement("div");
    document.body.append(shown);
    const toasts = createRoot(shown);
    act(() => toasts.render(<Toasts />));
    expect(document.body.textContent).toContain("没能忘记「vendor/model-x」：数据库暂时不可用");
    act(() => toasts.unmount());
    shown.remove();
  });
});
