// @vitest-environment jsdom
// "New agent"'s model list with a typed model ID (#1644): the IDs typed before under the agent's own (each with Forget),
// "Other model…" whose field says why an ID can't be used as it is typed, the same for what is typed in the search, and
// "Enter model ID…" when the list is empty or couldn't be read.
import { act, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LauncherModels } from "../lib/launcher-models";
import { ModelPicker, type ModelList } from "./ModelPicker";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const list: LauncherModels = { agent: "claude", models: [
  { id: "default", name: "Default", default: true },
  { id: "opus", name: "Opus 5.5" },
  { id: "vendor/model-x", name: "vendor/model-x", description: "之前用过的模型 ID", custom: true },
] };

let host: HTMLDivElement;
let root: Root;
const picked = vi.fn();
const forgot = vi.fn();

function Picker({ models, initial = "" }: { models: ModelList; initial?: string }) {
  const [model, setModel] = useState(initial);
  const [query, setQuery] = useState("");
  const search = useRef<HTMLInputElement>(null);
  return (
    <ModelPicker models={models} model={model} open query={query} onQuery={setQuery} search={search} custom typing
      onToggle={() => {}} onPick={(id) => { picked(id); setModel(id); }} onReload={() => {}} onForget={forgot} failedNote="没读到" />
  );
}

function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(text));
const enter = (el: HTMLElement) => act(() => { el.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); });

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  picked.mockClear();
  forgot.mockClear();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("New agent: a typed model ID (#1644)", () => {
  it("lists the IDs typed before under their own heading, each with Forget", () => {
    act(() => root.render(<Picker models={{ status: "ready", list }} />));
    expect(host.textContent).toContain("输入过的");
    const forget = host.querySelector<HTMLButtonElement>('button[aria-label="忘记「vendor/model-x」"]')!;
    act(() => forget.click());
    expect(forgot).toHaveBeenCalledWith("vendor/model-x");
    expect(picked).not.toHaveBeenCalled();
  });

  it("Other model… says why as it is typed, and picks a good ID; the trigger then shows it as typed", () => {
    act(() => root.render(<Picker models={{ status: "ready", list }} />));
    act(() => button("其他模型…")!.click());
    const input = host.querySelector<HTMLInputElement>("[data-model-id-field] input")!;
    type(input, "vendor model");
    expect(host.querySelector("[data-model-id-problem]")!.textContent).toBe("模型 ID 里不能有空格");
    expect(button("使用")!.disabled).toBe(true);
    enter(input);
    expect(picked).not.toHaveBeenCalled();
    type(input, "acme/coder-9");
    expect(host.querySelector("[data-model-id-problem]")).toBeNull();
    expect(button("使用")!.disabled).toBe(false);
    enter(input);
    expect(picked).toHaveBeenCalledWith("acme/coder-9");
    expect(host.querySelector("button[aria-expanded]")!.textContent).toContain("手动输入的模型 ID");
  });

  it("what is typed in the search: a good ID is offered, a bad one says why", () => {
    act(() => root.render(<Picker models={{ status: "ready", list }} />));
    const search = host.querySelector<HTMLInputElement>('input[aria-label="搜索模型"]')!;
    type(search, "a;b");
    expect(host.querySelector('[data-testid="model-picker-problem"]')!.textContent).toContain("「;」");
    type(search, "acme/coder-9");
    expect(button("使用「acme/coder-9」")).toBeTruthy();
  });

  it("offers Enter model ID… when the list is empty or couldn't be read", () => {
    act(() => root.render(<Picker models={{ status: "ready", list: { agent: "claude", models: [] } }} />));
    expect(button("输入模型 ID…")).toBeTruthy();
    act(() => root.render(<Picker key="error" models={{ status: "error", error: "claude 没有登录" }} initial="acme/coder-9" />));
    expect(button("输入模型 ID…")).toBeTruthy();
    // the typed pick stays on the trigger while the list is out
    expect(host.querySelector("button[aria-expanded]")!.textContent).toContain("acme/coder-9");
  });
});
