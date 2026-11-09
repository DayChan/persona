// @vitest-environment jsdom
// Shared schema examples and native DOM checks. Not browser geometry or live backend acceptance.
import { t } from "../../i18n";
const exampleSources = import.meta.glob<string>("../../../../schema/extension/examples/View.*.json", { query: "?raw", import: "default", eager: true });
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ExtensionUIStreamSession, NATIVE_STREAM_COMPONENTS, nativeStreamSlots } from "../../lib/extension-ui-stream";
import type { Component, UIStreamRequest, UIStreamSnapshot, View } from "../../lib/extension-wire";
import { ExtensionStreamSections, type ExtensionStreamSectionsProps } from "./ExtensionStreamSections";
import { nativeRenderGap } from "./NativeStreamItems";

vi.hoisted(() => {
  (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
  (globalThis as Record<string, unknown>).matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});
const identity = { owner: "demo-owner", hubGroup: "demo-group", extension: "example", contribution: "native", navigation: "root" };
let root: Root, host: HTMLDivElement, session: ExtensionUIStreamSession;
const options: ExtensionStreamSectionsProps = { identity, fallback: <p>Legacy fallback</p> };
afterEach(async () => { vi.useRealTimers(); if (root) await act(async () => root.unmount()); session?.close(); host?.remove(); vi.unstubAllGlobals(); });
function tree(items: Component[], view: Partial<View> = {}): UIStreamSnapshot {
  return { protocol: 1, session: "demo-session", epoch: "demo-epoch", version: 1, root_key: "root", view: { sections: [{ key: "main", items }, { key: "other", items: [] }], ...view } };
}
function fixture(name: string): UIStreamSnapshot {
  const view = JSON.parse(exampleSources[`../../../../schema/extension/examples/${name}.json`]) as View;
  // Legacy examples have no section/item keys; adapt only the harness clone, never the shared file.
  view.sections.forEach((s, si) => { s.key ??= `section-${si}`; s.items.forEach((it, ii) => { it.key ??= `item-${si}-${ii}`; }); });
  return tree([], view);
}
async function mount(initial: UIStreamSnapshot, props: Partial<ExtensionStreamSectionsProps> = {}, transport?: (r: UIStreamRequest) => Promise<void>) {
  const sent: UIStreamRequest[] = [], errors: unknown[] = [];
  let id = 0;
  session = new ExtensionUIStreamSession({ identity, context: { platform: "web", ui_version: 7, stream: { protocol: 1, components: [...NATIVE_STREAM_COMPONENTS] } }, id: () => `id-${++id}`, transport: { send: async (r) => { sent.push(r); await transport?.(r); } } });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<ExtensionStreamSections {...options} availableAdapter={session} onError={(e) => errors.push(e)} {...props} />));
  await act(async () => { expect(session.receiveSnapshot(initial)).toBe(true); });
  return { sent, errors };
}
async function click(label: string, within: ParentNode = host) {
  const button = [...within.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
  expect(button, `Missing supported native control ${label}`).toBeDefined();
  await act(async () => button!.click());
}
async function type(el: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, value: string) {
  const prototype = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  await act(async () => { Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(el, value); el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true })); });
}
async function replace(key: string, item: Component) {
  const s = session.getState().snapshot!;
  await act(async () => { expect(session.receiveDelta({ protocol: 1, session: s.session, epoch: s.epoch, base_version: s.version, version: s.version + 1, operations: [{ op: "replace", key, item }] })).toBe(true); });
}
async function move(key: string, parent = "other") {
  const s = session.getState().snapshot!;
  await act(async () => { expect(session.receiveDelta({ protocol: 1, session: s.session, epoch: s.epoch, base_version: s.version, version: s.version + 1, operations: [{ op: "move", key, parent }] })).toBe(true); });
}
describe("independent native boundary regressions", () => {
  it("does not publish focus state before bubbling input commits the controlled draft", async () => {
    await mount(tree([{ type: "composer", key: "composer", name: "body", buttons: [{ title: "Send", action: { id: "send" } }] }]));
    const publish = vi.fn(), unsubscribe = session.subscribe(publish);
    let atTarget = -1;
    const box = host.querySelector<HTMLTextAreaElement>("textarea")!;
    box.addEventListener("input", () => { atTarget = publish.mock.calls.length; });
    await type(box, "Keep this draft"); unsubscribe();
    expect(atTarget).toBe(0); expect(box.value).toBe("Keep this draft"); expect(session.getDraft("composer")).toBe("Keep this draft");
  });
  it("refuses secret drafts even through the session seam", async () => {
    await mount(tree([{ type: "field", key: "secret", name: "credential", label: "Secret", kind: "secret", value: "never prefill" }]));
    expect(() => session.setDraft("secret", "must not appear")).toThrow("Secret draft");
    expect(host.querySelector<HTMLInputElement>('input[type="password"]')!.value).toBe("");
  });
  it("never borrows a replacement slider callback during an ongoing gesture", async () => {
    const row = (target: string): Component => ({ type: "row", key: "row", title: "Level", control: { kind: "slider", name: "level", value: 0, action: { id: "set", values: { target } } } });
    const { sent } = await mount(tree([row("a")]));
    const range = node("row").querySelector<HTMLInputElement>("input")!;
    await act(async () => range.dispatchEvent(new Event("pointerdown", { bubbles: true })));
    await type(range, "25"); await replace("row", row("b"));
    await act(async () => range.dispatchEvent(new Event("pointerup", { bubbles: true })));
    expect(events(sent)).toHaveLength(0);
  });
  it("reconciles changed canonical field and composer values without clearing unrelated drafts", async () => {
    await mount(tree([{ type: "field", key: "field", kind: "text", name: "name", label: "Name", value: "before" }, { type: "composer", key: "composer", name: "body", value: "before", buttons: [{ title: "Send", action: { id: "send" } }] }]));
    await type(host.querySelector("input")!, "local name"); await type(host.querySelector("textarea")!, "local body");
    await replace("field", { type: "field", key: "field", kind: "text", name: "name", label: "Name", value: "after" });
    expect(host.querySelector<HTMLInputElement>("input")!.value).toBe("after");
    expect(host.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("local body");
    await replace("composer", { type: "composer", key: "composer", name: "body", value: "", buttons: [{ title: "Send", action: { id: "send" } }] });
    expect(host.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("");
  });
  it("can pick and cancel inline comments after a same-key diff revision replacement", async () => {
    const diff = (revision: string): Component => ({ type: "diff", key: "diff", path: "a.txt", revision, comment: { id: "comment" }, hunks: [{ old_start: 1, new_start: 1, lines: ["+line"] }] });
    await mount(tree([diff("a")])); await replace("diff", diff("b"));
    const line = node("diff").querySelector<HTMLElement>('[data-ln="new"]')!;
    await act(async () => { line.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 })); line.dispatchEvent(new MouseEvent("mouseup", { bubbles: true })); });
    expect(node("diff").querySelector("textarea")).not.toBeNull();
    await click(t("extensions-codeviews.cancel"), node("diff"));
    expect(node("diff").querySelector("textarea")).toBeNull();
  });
  it("expands an unknown trailing gap using JSON-safe persisted state", async () => {
    await mount(tree([{ type: "diff", key: "diff", path: "a.txt", revision: "a", context: { id: "context" }, hunks: [{ old_start: 1, new_start: 1, lines: [" line"] }] }]), { readResult: async () => ({ lines: { path: "a.txt", start: 2, lines: ["tail", "last"], total: 3 } }) });
    await click(t("extensions-review.expand_all"), node("diff"));
    expect(node("diff").textContent).toContain("tail"); expect(node("diff").textContent).toContain("last");
    expect(JSON.stringify(session.getLocal("diff"))).not.toContain("null");
  });
  it("does not roll back successful viewed events with no context-lines result", async () => {
    await mount(tree([{ type: "diff", key: "diff", path: "a.txt", viewed: false, mark_viewed: { id: "viewed" }, hunks: [{ old_start: 1, new_start: 1, lines: [" line"] }] }]), { readResult: async () => null });
    await act(async () => node("diff").querySelector<HTMLButtonElement>('[role="checkbox"]')!.click());
    expect(node("diff").querySelector('[role="checkbox"]')!.getAttribute("aria-checked")).toBe("true");
  });
  it("does not consume More or diff observations while another event is pending", async () => {
    const observers: Array<() => void> = [];
    vi.stubGlobal("IntersectionObserver", class { constructor(callback: (entries: Array<{ isIntersecting: boolean }>) => void) { observers.push(() => callback([{ isIntersecting: true }])); } observe() {} disconnect() {} });
    let finish!: () => void;
    const initial = tree([{ type: "button", key: "button", title: "Hold", action: { id: "hold" } }, { type: "diff", key: "diff", path: "a.txt", hunks: [], load: { id: "load" } }]);
    initial.view.sections[0].more = { title: "More", action: { id: "more" } };
    const { sent } = await mount(initial, {}, (r) => r.request === "event" && r.interaction.action === "hold" ? new Promise<void>((resolve) => { finish = resolve; }) : Promise.resolve());
    await click("Hold"); await act(async () => observers.forEach((observe) => observe()));
    expect(events(sent).map((e) => e.action)).toEqual(["hold"]);
    await act(async () => finish());
    await act(async () => observers.forEach((observe) => observe()));
    await act(async () => observers.forEach((observe) => observe()));
    expect(events(sent).map((e) => e.action).sort()).toEqual(["hold", "load", "more"]);
  });
  it("resolves hub avatars and thumbnails, or negotiates their missing provenance", async () => {
    const items: Component[] = [{ type: "row", key: "avatar", title: "Avatar", avatar: { ref: "avatar", path: "/api/v1/extensions/example/-/image/avatar" } }, { type: "row", key: "thumbnail", title: "Thumbnail", thumbnail: { ref: "thumb", path: "/api/v1/extensions/example/-/image/thumb" } }];
    expect(nativeRenderGap(tree(items).view)).toContain("provenance");
    const media = vi.fn((source: { ref?: string }) => `https://example.com/${source.ref}.png`);
    await mount(tree(items), { media });
    expect(node("avatar").querySelector<HTMLImageElement>("img")!.src).toBe("https://example.com/avatar.png");
    expect(node("thumbnail").querySelector<HTMLImageElement>("img")!.src).toBe("https://example.com/thumb.png");
  });
  it("shows a design-system placeholder for a failed native thumbnail and retries a changed source", async () => {
    await mount(tree([{ type: "row", key: "thumbnail", title: "Synthetic thumbnail", thumbnail: { url: "https://example.com/missing.png" } }]));
    await act(async () => node("thumbnail").querySelector("img")!.dispatchEvent(new Event("error")));
    expect(node("thumbnail").querySelector("img")).toBeNull();
    expect(node("thumbnail").querySelector('[role="img"]')?.getAttribute("aria-label")).toBe("Synthetic thumbnail");
    await replace("thumbnail", { type: "row", key: "thumbnail", title: "Synthetic thumbnail", thumbnail: { url: "https://example.com/ready.png" } });
    expect(node("thumbnail").querySelector<HTMLImageElement>("img")!.src).toBe("https://example.com/ready.png");
  });
  it("constrains long streamed stat values without losing their full text", async () => {
    const stats = (value: string): Component => ({ type: "stat_grid", key: "stats", stats: [
      { label: "Open", value: "12.34" }, { label: "Previous", value: "12.00" },
      { label: "Volume", value }, { label: "Range", value: "2.18%" },
      { label: "Turnover", value: "0.31%" }, { label: "Ratio", value: "38.13" },
    ] });
    const value = "123456789.00 units";
    await mount(tree([stats(value)]));
    const displayed = [...node("stats").querySelectorAll<HTMLDivElement>("div")].find((el) => !el.children.length && el.textContent === value)!;
    expect(displayed.parentElement!.classList.contains("min-w-0")).toBe(true);
    expect(displayed.classList.contains("truncate")).toBe(true);
    expect(displayed.title).toBe(value);
    expect(node("stats").textContent).toContain("2.18%");
    await replace("stats", stats(value + " updated"));
    const updated = [...node("stats").querySelectorAll<HTMLDivElement>("div[title]")].find((el) => el.title === value + " updated")!;
    expect(updated.textContent).toBe(value + " updated");
    expect(updated.title).toBe(value + " updated");
    expect(updated.classList.contains("truncate")).toBe(true);
  });
  it("preserves native metric and stat tones", async () => {
    await mount(tree([{ type: "metric", key: "metric", label: "Warning", value: "0", tone: "danger" }, { type: "stat_grid", key: "stats", stats: [{ label: "Good", value: "0", tone: "positive" }, { label: "Watch", value: "0", tone: "caution" }] }]));
    expect(node("metric").querySelector(".text-red")).not.toBeNull();
    expect(node("stats").querySelector(".text-green")).not.toBeNull(); expect(node("stats").querySelector(".text-orange")).not.toBeNull();
  });
});

const events = (sent: UIStreamRequest[]) => sent.filter((r) => r.request === "event").map((r) => r.interaction);
const node = (key: string) => [...host.querySelectorAll<HTMLElement>("[data-native-key]")].find((n) => n.dataset.nativeKey === key)!;

 describe("complete native renderer action slots", () => {
  it.each([
    { item: { type: "button", key: "target", title: "Run", action: { id: "run" } }, label: "Run", event: "press", value: {} },
    { item: { type: "row", key: "target", title: "Run", action: { id: "run" } }, label: "Run", event: "press", value: {} },
    { item: { type: "empty", key: "target", title: "Empty", button: { title: "Run", action: { id: "run" } } }, label: "Run", event: "press", value: {} },
    { item: { type: "breadcrumb", key: "target", crumbs: [{ title: "Run", action: { id: "run" } }, { title: "Current" }] }, label: "Run", event: "press", value: {} },
    { item: { type: "code", key: "target", text: "", path: "example.txt", truncated: true, load: { id: "run" } }, label: "Load the rest", event: "press", value: { path: "example.txt", from: "2" } },
  ])("draws and dispatches $item.type at its displayed node", async ({ item, label, event, value }) => {
    const { sent } = await mount(tree([item as Component]));
    if (item.type === "code") await click(t("extensions-codeviews.load_rest"));
    else await click(label);
    expect(events(sent)).toMatchObject([{ key: "target", action: "run", version: 1, event, value }]);
  });
  it("dispatches file open and unloaded-directory expand at the file tree, not the root", async () => {
    const { sent } = await mount(tree([{ type: "file_tree", key: "files", entries: [{ path: "a.txt" }, { path: "unloaded", kind: "dir" }], action: { id: "open" }, expand: { id: "expand" } }]));
    await click("a.txt"); await click("unloaded");
    expect(events(sent)).toMatchObject([{ key: "files", action: "open", value: { path: "a.txt" } }, { key: "files", action: "expand", value: { path: "unloaded" } }]);
  });
  it("binds every tile action/control/menu to the displayed tile even with reused action IDs", async () => {
    const { sent } = await mount(tree([{ type: "grid", key: "grid", tiles: [
      { key: "tile-a", title: "Tile A", action: { id: "same", values: { entity: "a" } } },
      { key: "tile-b", title: "Tile B", control: { kind: "switch", name: "power", on: false, action: { id: "same", values: { entity: "b" } } }, actions: [{ title: "Extra", action: { id: "same", values: { entity: "extra" } } }] },
    ] }, { type: "carousel", key: "carousel", title: "Scenes", tiles: [{ key: "tile-c", title: "Tile C", action: { id: "same", values: { entity: "c" } } }] }]));
    await click("Tile A"); await act(async () => node("tile-b").querySelector<HTMLButtonElement>('[role="switch"]')!.click());
    const menu = node("tile-b").querySelector<HTMLButtonElement>(`button[title="${t("extensions-native.actions")}"]`)!;
    await act(async () => menu.click()); await click("Extra", node("tile-b")); await click("Tile C");
    expect(events(sent)).toMatchObject([
      { key: "tile-a", value: { entity: "a" } }, { key: "tile-b", event: "change", value: { power: "true", entity: "b" } },
      { key: "tile-b", value: { entity: "extra" } }, { key: "tile-c", value: { entity: "c" } },
    ]);
  });
  it("segmented changes and delayed stepper taps send their native values", async () => {
    vi.useFakeTimers();
    const { sent } = await mount(tree([{ type: "segmented", key: "seg", name: "mode", value: "a", choices: [{ value: "a", title: "A" }, { value: "b", title: "B" }], action: { id: "same" } }, { type: "row", key: "step", title: "Level", control: { kind: "stepper", name: "level", value: 0, min: 0, max: 2, step: 0.5, action: { id: "same" } } }]));
    await click("B", node("seg"));
    await act(async () => node("step").querySelector<HTMLButtonElement>(`button[title="${t("extensions-native.increase")}"]`)!.click());
    await act(async () => node("step").querySelector<HTMLButtonElement>(`button[title="${t("extensions-native.increase")}"]`)!.click());
    await act(async () => vi.advanceTimersByTimeAsync(600));
    expect(events(sent)).toMatchObject([{ key: "seg", event: "change", value: { mode: "b" } }, { key: "step", event: "change", value: { level: "1" } }]);
  });
  it("local navigation carries the displayed key/action/version without an invented transport envelope", async () => {
    const navigate = vi.fn();
    const { sent } = await mount(tree([{ type: "row", key: "row", title: "Detail", action: { id: "open", push: { state: "detail", title: "Detail" }, values: { target: "original" } } }]), { navigate });
    await click("Detail");
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ id: "open", values: { target: "original" }, push: { state: "detail", title: "Detail" } }), { key: "row", action: "open", version: 1, session: "demo-session", epoch: "demo-epoch" });
    expect(events(sent)).toHaveLength(0);
  });
  it("cannot dispatch a disabled row control, including through the session API", async () => {
    const { sent } = await mount(tree([{ type: "row", key: "row", title: "Offline", control: { kind: "switch", name: "power", disabled: true, action: { id: "toggle" } } }]));
    const control = node("row").querySelector<HTMLButtonElement>('[role="switch"]')!;
    expect(control.disabled).toBe(true); await act(async () => control.click());
    await expect(session.event("row", "toggle", "change", { power: "true" })).rejects.toThrow("callback");
    expect(events(sent)).toHaveLength(0);
  });
  it("slider sends only on release with zero defaults, and restores on uncertain delivery", async () => {
    const { sent } = await mount(tree([{ type: "row", key: "row", title: "Brightness", control: { kind: "slider", name: "brightness", min: 0, max: 100, action: { id: "set" } } }]), {}, async (r) => { if (r.request === "event") throw new Error("Disconnected"); });
    const range = node("row").querySelector<HTMLInputElement>('input[type="range"]')!;
    expect(range.value).toBe("0"); await type(range, "25"); expect(events(sent)).toHaveLength(0);
    await act(async () => range.dispatchEvent(new Event("pointerup", { bubbles: true })));
    expect(events(sent)).toMatchObject([{ key: "row", event: "change", value: { brightness: "25" } }]);
    expect(range.value).toBe("0"); expect(session.getState().phase).toBe("recovering");
  });
  it("stepper coalesces taps without borrowing a handler after an unrelated version change", async () => {
    vi.useFakeTimers();
    const { sent } = await mount(tree([{ type: "row", key: "row", title: "Temperature", control: { kind: "stepper", name: "temperature", value: 0, min: 0, max: 2, step: 0.5, action: { id: "set" } } }, { type: "text", key: "text", text: "Before" }]));
    await act(async () => node("row").querySelector<HTMLButtonElement>(`button[title="${t("extensions-native.increase")}"]`)!.click());
    await act(async () => node("row").querySelector<HTMLButtonElement>(`button[title="${t("extensions-native.increase")}"]`)!.click());
    expect(events(sent)).toHaveLength(0);
    await replace("text", { type: "text", key: "text", text: "After" });
    await act(async () => vi.advanceTimersByTimeAsync(600)); expect(events(sent)).toHaveLength(0);
  });
  it("late control failure after keyed removal cannot write into another node or throw on the retired node", async () => {
    let reject!: (error: Error) => void;
    const { sent } = await mount(tree([{ type: "row", key: "row", title: "Power", control: { kind: "switch", name: "power", action: { id: "toggle" } } }]), {}, (request) => request.request === "event" ? new Promise<void>((_resolve, fail) => { reject = fail; }) : Promise.resolve());
    await act(async () => node("row").querySelector<HTMLButtonElement>('[role="switch"]')!.click());
    const s = session.getState().snapshot!;
    await act(async () => { expect(session.receiveDelta({ protocol: 1, session: s.session, epoch: s.epoch, base_version: 1, version: 2, operations: [{ op: "remove", key: "row" }] })).toBe(true); });
    await act(async () => reject(new Error("Uncertain delivery")));
    expect(session.getLocal("row")).toBeUndefined(); expect(events(sent)).toHaveLength(1);
  });
  it("retains row menus and spark fields through unrelated deltas and remounts", async () => {
    await mount(tree([{ type: "row", key: "row", title: "Trend", value: "0", spark: { values: [0, 2, 1], fill: false }, actions: [{ title: "Edit", action: { id: "edit" } }] }, { type: "text", key: "text", text: "Before" }]));
    await act(async () => node("row").querySelector<HTMLButtonElement>(`button[title="${t("extensions-native.actions")}"]`)!.click());
    expect(node("row").querySelector('svg[role="img"]')!.getAttribute("aria-label")).toBe("Trend: 0, 2, 1");
    await replace("text", { type: "text", key: "text", text: "After" }); await move("row");
    expect(node("row").textContent).toContain("Edit"); expect(node("row").textContent).toContain("0");
  });
});

describe("root chrome, drafts and stable confirmations", () => {
  it("root search/filter/tabs and More carry current narrowed values while retaining local search", async () => {
    vi.useFakeTimers();
    const initial = tree([], { title: "Native page", search: { value: "", action: { id: "search" } }, filter: { name: "kind", value: "all", choices: [{ value: "all" }, { value: "new" }], action: { id: "filter" } }, tabs: { name: "tab", value: "a", items: [{ value: "a", title: "A" }, { value: "b", title: "B", badge: "0" }], action: { id: "tabs" } } });
    initial.view.sections[0].more = { title: "More", action: { id: "more" } };
    const { sent } = await mount(initial);
    await type(host.querySelector<HTMLInputElement>('input[type="search"]')!, "local query");
    await act(async () => vi.advanceTimersByTimeAsync(300));
    await type(host.querySelector("select")!, "new"); await click("B0"); await click("More");
    expect(events(sent)).toMatchObject([
      { key: "root", event: "change", action: "search", value: { q: "local query", kind: "all", tab: "a" } },
      { key: "root", event: "change", action: "filter", value: { q: "local query", kind: "new", tab: "a" } },
      { key: "root", event: "change", action: "tabs", value: { q: "local query", kind: "new", tab: "b" } },
      { key: "main", action: "more", value: { q: "local query", kind: "new", tab: "b" } },
    ]);
  });
  it("freezes submitted drafts and carried values before confirmation, without clearing a canceled draft", async () => {
    let answer!: (yes: boolean) => void;
    const confirm = vi.fn(() => new Promise<boolean>((resolve) => { answer = resolve; }));
    const { sent } = await mount(tree([{ type: "field", key: "field", kind: "text", name: "body", label: "Body", value: "Initial" }, { type: "button", key: "button", title: "Confirm", action: { id: "confirm", submit: true, confirm: "Confirm operation", values: { target: "original" } } }]), { confirm });
    await type(host.querySelector("input")!, "Displayed draft"); await click("Confirm");
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ id: "confirm", confirm: "Confirm operation", values: { target: "original" } }), expect.objectContaining({ key: "button", action: "confirm", version: 1 }), { body: "Displayed draft", target: "original" });
    await act(async () => { session.setDraft("field", "Edited during confirmation"); answer(true); });
    expect(events(sent)).toMatchObject([{ key: "button", version: 1, event: "press", value: { body: "Displayed draft", target: "original" } }]);
    await click("Confirm"); await act(async () => answer(false));
    expect(events(sent)).toHaveLength(1); expect(host.querySelector<HTMLInputElement>("input")!.value).toBe("Edited during confirmation");
  });
  it("retains review summaries, including count zero, across deltas and native remounts", async () => {
    const { sent } = await mount(tree([{ type: "text", key: "text", text: "Before" }], { review: { count: 0, name: "summary", buttons: [{ title: "Submit review", action: { id: "review" } }], discard: { id: "discard" } } }));
    await type(host.querySelector("textarea")!, "Review draft"); await replace("text", { type: "text", key: "text", text: "After" });
    await click("Submit review");
    expect(events(sent)).toMatchObject([{ key: "root", action: "review", event: "submit", value: { summary: "Review draft" } }]);
    await click(t("extensions-review.discard")); expect(events(sent).at(-1)).toMatchObject({ key: "root", action: "discard", event: "press" });
    expect(host.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("Review draft");
  });
  it("retains focused selection and keyed scroll after a composer moves sections", async () => {
    await mount(tree([{ type: "composer", key: "composer", name: "body", buttons: [{ title: "Send", action: { id: "send" } }] }]));
    const box = host.querySelector("textarea")!; await type(box, "Keep this draft");
    await act(async () => { box.focus(); box.setSelectionRange(2, 5); box.dispatchEvent(new Event("select", { bubbles: true })); document.dispatchEvent(new Event("selectionchange", { bubbles: true })); node("composer").scrollTop = 80; node("composer").dispatchEvent(new Event("scroll", { bubbles: true })); });
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(session.getLocal<Record<string, unknown>>("root")?.focus).toMatchObject({ start: 2, end: 5 });
    await move("composer");
    const restored = node("composer").querySelector<HTMLTextAreaElement>("textarea")!;
    expect(restored.value).toBe("Keep this draft"); expect(document.activeElement).toBe(restored); expect(restored.selectionStart).toBe(2); expect(restored.selectionEnd).toBe(5); expect(node("composer").scrollTop).toBe(80);
  });
  it("hidden and reconnecting pages keep drafts but cannot execute controls until a fresh snapshot", async () => {
    const initial = tree([{ type: "composer", key: "composer", name: "body", buttons: [{ title: "Send", action: { id: "send" } }] }]);
    const { sent } = await mount(initial); await type(host.querySelector("textarea")!, "Unsent");
    await act(async () => { session.setConnected(false); }); await click("Send"); expect(events(sent)).toHaveLength(0);
    expect(host.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("Unsent");
    await act(async () => { session.setConnected(true); session.receiveSnapshot(initial); }); await click("Send");
    expect(events(sent)).toMatchObject([{ key: "composer", event: "submit", value: { body: "Unsent" } }]);
  });
});

describe("native diff, review and discussion locality", () => {
  const diff = (extra: Partial<Extract<Component, { type: "diff" }>> = {}): Extract<Component, { type: "diff" }> => ({ type: "diff", key: "diff", path: "sample.txt", revision: "rev-a", status: "modified", additions: 0, deletions: 0, viewed: false, hunks: [{ old_start: 5, new_start: 5, lines: [" unchanged", "+added"] }], ...extra });
  it("context and viewed callbacks use the displayed diff key and preserve loaded context after a move", async () => {
    const readResult = vi.fn(async () => ({ lines: { path: "sample.txt", start: 1, lines: ["first", "second", "third", "fourth"], total: 6 } }));
    const { sent } = await mount(tree([diff({ context: { id: "context" }, mark_viewed: { id: "viewed" } })]), { readResult });
    await click(t("extensions-review.expand_all"), node("diff"));
    expect(events(sent)[0]).toMatchObject({ key: "diff", action: "context", event: "press", value: { path: "sample.txt", side: "new", from: "1", to: "4" } });
    expect(readResult).toHaveBeenCalledWith(expect.objectContaining({ key: "diff", version: 1, event_id: expect.any(String) }));
    expect(node("diff").textContent).toContain("fourth"); await move("diff"); expect(node("diff").textContent).toContain("fourth");
    await act(async () => node("diff").querySelector<HTMLButtonElement>('[role="checkbox"]')!.click());
    expect(events(sent).at(-1)).toMatchObject({ key: "diff", action: "viewed", event: "press", version: 2, value: { path: "sample.txt", viewed: "true" } });
    expect(node("diff").querySelector('[role="checkbox"]')!.getAttribute("aria-checked")).toBe("true");
  });
  it("manual diff load and old expand remain distinct native slots", async () => {
    const { sent } = await mount(tree([diff({ hunks: [], load: { id: "load" }, collapsed: true })]));
    await click(t("extensions-codeviews.load_diff"), node("diff"));
    expect(events(sent)[0]).toMatchObject({ key: "diff", action: "load", value: { path: "sample.txt" } });
    await replace("diff", diff({ expand: { id: "expand" } }));
    await click(t("extensions-codeviews.expand_up"), node("diff"));
    expect(events(sent).at(-1)).toMatchObject({ key: "diff", action: "expand", value: { path: "sample.txt", hunk: "0", direction: "up" } });
  });
  it("automatic diff loading is observed only once across remounts and unrelated deltas", async () => {
    const observers: Array<() => void> = [];
    vi.stubGlobal("IntersectionObserver", class { constructor(callback: (entries: Array<{ isIntersecting: boolean }>) => void) { observers.push(() => callback([{ isIntersecting: true }])); } observe() {} disconnect() {} });
    const { sent } = await mount(tree([diff({ hunks: [], load: { id: "load" } }), { type: "text", key: "text", text: "Before" }]));
    await act(async () => { observers.forEach((observe) => observe()); });
    await replace("text", { type: "text", key: "text", text: "After" }); await move("diff");
    await act(async () => { observers.forEach((observe) => observe()); });
    expect(events(sent)).toHaveLength(1);
  });
  it("retains inline comment drafts and binds draft/comment submit to the displayed diff", async () => {
    const { sent } = await mount(tree([diff({ comment: { id: "comment" }, draft: { id: "draft", values: { carried: "draft-target" } } })]));
    const line = node("diff").querySelector<HTMLElement>('[data-ln="new"][data-n="6"]')!;
    expect(line).toBeTruthy(); await act(async () => { line.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 })); line.dispatchEvent(new MouseEvent("mouseup", { bubbles: true })); });
    await type(node("diff").querySelector<HTMLTextAreaElement>("textarea")!, "Inline draft"); await move("diff");
    expect(node("diff").querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("Inline draft");
    await click(t("extensions-review.start_review"), node("diff"));
    await click(t("extensions-review.comment_now"), node("diff"));
    expect(events(sent)).toMatchObject([
      { key: "diff", action: "draft", event: "press", version: 2, value: { path: "sample.txt", side: "new", line: "6", body: "Inline draft", carried: "draft-target" } },
      { key: "diff", action: "comment", event: "press", version: 2, value: { path: "sample.txt", side: "new", line: "6", body: "Inline draft" } },
    ]);
  });
  it("thread reply and resolve cannot borrow the first thread's carried values on the same diff", async () => {
    const { sent } = await mount(tree([diff({ threads: [
      { id: "thread-a", side: "new", line: 5, comments: [{ author: "Demo A", body: "First" }], reply: { id: "reply", values: { target: "a" } } },
      { id: "thread-b", side: "new", line: 6, comments: [{ author: "Demo B", body: "Second" }], reply: { id: "reply", values: { target: "b" } }, resolve: { id: "resolve", values: { target: "b" } } },
    ] })]));
    let thread = node("diff").querySelector<HTMLElement>('[data-thread="thread-b"]')!;
    await click(t("extensions-codeviews.reply"), thread); await type(thread.querySelector<HTMLTextAreaElement>("textarea")!, "Thread B draft"); await move("diff");
    thread = node("diff").querySelector<HTMLElement>('[data-thread="thread-b"]')!;
    expect(thread.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("Thread B draft"); await click(t("extensions-codeviews.comment"), thread); await click(t("extensions-review.resolve"), thread);
    expect(events(sent)).toMatchObject([{ key: "diff", action: "reply", event: "press", value: { thread: "thread-b", body: "Thread B draft", target: "b" } }, { key: "diff", action: "resolve", event: "press", value: { thread: "thread-b", resolved: "true", target: "b" } }]);
  });
  it("standalone reply drafts survive cancel/reopen, moves and uncertain delivery", async () => {
    const { sent } = await mount(tree([{ type: "comment_thread", key: "thread", comments: [], reply: { id: "reply" } }]), {}, async (r) => { if (r.request === "event") throw new Error("Uncertain delivery"); });
    await click(t("extensions-codeviews.reply"), node("thread")); await type(node("thread").querySelector<HTMLTextAreaElement>("textarea")!, "Unsent reply");
    await click(t("extensions-codeviews.cancel"), node("thread")); await click(t("extensions-codeviews.reply"), node("thread")); await move("thread");
    expect(node("thread").querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("Unsent reply"); await click(t("extensions-codeviews.comment"), node("thread"));
    expect(events(sent)).toMatchObject([{ key: "thread", action: "reply", event: "press", value: { body: "Unsent reply" } }]);
    expect(node("thread").querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("Unsent reply");
  });
});

describe("shared native vocabulary and explicit negotiation", () => {
  it.each(["View.every-v1-item", "View.components-v3", "View.code-overview", "View.code-browse", "View.code-review", "View.v6-inputs"])("draws the shared %s fixture without silently omitting rows", async (name) => {
    const initial = fixture(name); await mount(initial, { navigate: vi.fn() });
    expect(host.textContent).not.toContain("Legacy fallback");
    for (const item of initial.view.sections.flatMap((s) => s.items).filter((it) => it.type === "row")) expect(host.textContent).toContain((item as { title: string }).title);
    expect(session.getState().snapshot?.view).toEqual(initial.view);
  });
  it("preserves the shared v6 input defaults exactly before native display and submit", async () => {
    const initial = fixture("View.v6-inputs"); const { sent } = await mount(initial);
    const items = initial.view.sections[0].items;
    const day = items.find((it) => it.type === "field" && it.kind === "date")!;
    expect(node(day.key!).querySelector<HTMLInputElement>("input")!.min).toBe("2026-11-01");
    expect(host.querySelector<HTMLInputElement>('input[type="time"]')!.value).toBe("07:30");
    expect(host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]:checked')).toHaveLength(2);
    await act(async () => node(items.at(-1)!.key!)?.querySelector<HTMLButtonElement>("button")?.click());
    // Button groups have no item wrapper; locate its schema title in the native DOM.
    if (!events(sent).length) await click((items.at(-1) as { title: string }).title);
    expect(events(sent)).toMatchObject([{ value: { day: "2026-11-06", at: "07:30", until: "2026-11-06T22:00", rooms: '["kitchen","bedroom"]', note: "第一行\n第二行" } }]);
  });
  it("explicitly falls back for unimplemented chart forms rather than advertising tag parity", async () => {
    const onGap = vi.fn(); await mount(fixture("View.v6-charts"), { onGap });
    expect(host.textContent).toBe("Legacy fallback"); expect(onGap).toHaveBeenCalledWith(expect.stringMatching(/^chart\./)); expect(session.getState().phase).toBe("suspended");
  });
  it("keeps the shared controls fixture canonical but negotiates missing hue/icon/navigation support", async () => {
    const initial = fixture("View.v6-controls"), onGap = vi.fn(); await mount(initial, { onGap, navigate: vi.fn() });
    expect(host.textContent).toBe("Legacy fallback"); expect(onGap).toHaveBeenCalledWith(expect.stringMatching(/color\.amber|icon\.light/)); expect(session.getState().snapshot?.view).toEqual(initial.view);
  });
  it("requires an owner-bound media adapter for hub ref/path sources; HTTPS sources remain credential-free", async () => {
    const view: View = { sections: [{ key: "main", items: [{ type: "image", key: "image", src: { ref: "camera", path: "/api/v1/extensions/example/-/image/camera" }, alt: "Camera" }] }] };
    expect(nativeRenderGap(view)).toContain("provenance"); const media = vi.fn(() => "https://example.com/camera.png");
    await mount(tree([], view), { media }); expect(media).toHaveBeenCalledWith(expect.objectContaining({ ref: "camera", path: "/api/v1/extensions/example/-/image/camera" }));
    expect(host.querySelector<HTMLImageElement>("img")!.getAttribute("referrerpolicy")).toBe("no-referrer"); expect(host.querySelector<HTMLImageElement>("img")!.src).toBe("https://example.com/camera.png");
  });
  it("scalar carried values never become component-field negotiation rules", () => {
    expect(nativeRenderGap({ sections: [{ items: [{ type: "button", title: "Run", action: { id: "run", values: { icon: "custom data", color: "custom data", reply: "custom data", page: "custom data" } } }] }] })).toBeUndefined();
  });
  it("does not invent a context-lines endpoint or an assistant nested-pages renderer", () => {
    expect(nativeRenderGap({ sections: [{ key: "main", items: [{ type: "diff", key: "diff", path: "a.txt", context: { id: "lines" } }] }] })).toContain("result adapter");
  });
  it("enumerates every native callback slot with local key and semantic event", () => {
    const initial = fixture("View.code-review");
    const slots = nativeStreamSlots(initial);
    expect(slots).toEqual(expect.arrayContaining([
      expect.objectContaining({ key: "root", event: "change", action: { id: "tab" } }),
      expect.objectContaining({ key: "files", event: "press", action: { id: "open_file" } }),
      expect.objectContaining({ key: "diff:src/upload/retry.go", event: "press", action: expect.objectContaining({ id: "reply" }) }),
      expect.objectContaining({ key: "diff:src/upload/client.go", event: "press", action: { id: "expand" } }),
      expect.objectContaining({ key: "diff:docs/upload.md", event: "press", action: { id: "load_diff" } }),
    ]));
  });
});
