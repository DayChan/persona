// @vitest-environment jsdom
// The usage sources page (#2595): the list never shows a key, adding / editing / switching / removing go to the hub's
// routes, and a changed address asks for the key again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { setCsrf, setTransport } from "../api";
import type { UsageSources } from "../types";
import { UsageSourcesPage } from "./UsageSources";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SECRET = "ag-secret-key-9f8e7d6c";
let host: HTMLDivElement;
let root: Root;
let calls: { method: string; path: string; body: Record<string, unknown> }[];
let view: UsageSources;
let testAnswer: unknown;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const text = () => host.textContent ?? "";
const dialog = () => document.body.querySelector<HTMLElement>('[role="dialog"]');
const byText = (root: ParentNode, label: string, tag = "button") => [...root.querySelectorAll<HTMLElement>(tag)].find((b) => b.textContent?.trim() === label);
const typeInto = async (el: HTMLInputElement | HTMLTextAreaElement, value: string) => {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!;
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
// the buttons of the nth source's row: its "Edit" button and the box that also holds its switch and "Delete"
const rowActions = (n: number) => {
  const edit = [...host.querySelectorAll<HTMLElement>("button")].filter((b) => b.textContent?.trim() === "编辑")[n];
  return { edit, box: edit.parentElement! };
};
const input = (label: string) => [...dialog()!.querySelectorAll("label")].find((l) => l.textContent === label)!.control as HTMLInputElement;

const fresh = (): UsageSources => ({
  enabled: true,
  poll_minutes: 5,
  limit: 8,
  sources: [
    { id: "s1", name: "Relay", type: "relay", url: "https://gateway.example.com", has_key: true, enabled: true, status: { ok_at: new Date(Date.now() - 120_000).toISOString(), seen: { accounts: 12, windows: 31, listed: 14, providers: { codex: 12 } } } },
    { id: "s2", name: "Team pool", type: "generic", url: "https://pool.example.com/usage", has_key: false, enabled: false, status: {} },
  ],
});

beforeEach(() => {
  view = fresh();
  calls = [];
  testAnswer = { ok: true, accounts: 12, windows: 31, listed: 14, providers: { codex: 8, claude: 4 } };
  setCsrf("tok");
  setTransport(async (url, init) => {
    const path = url.replace("/api/v1/admin", "");
    const body = typeof init.body === "string" && init.body ? JSON.parse(init.body) : {};
    calls.push({ method: String(init.method), path, body });
    const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status });
    if (path === "/usage-sources" && init.method === "GET") return reply(view);
    if (path === "/usage-sources" && init.method === "PUT") return reply({ ...view, ...body });
    if (path === "/usage-sources" && init.method === "POST") return reply({ source: { id: "s3", name: body.name || "Relay", type: body.type, url: body.url, has_key: Boolean(body.key), enabled: true, status: {} } });
    if (path === "/usage-sources/test") return reply(await testAnswer);
    if (path === "/usage-sources/s1" && init.method === "PUT" && body.url === "https://other.example.com" && !body.key) return reply({ error: "要填这个来源的 Key", code: "key_required" }, 400);
    if (path.startsWith("/usage-sources/")) return reply({ source: view.sources[0], ok: true });
    return reply({}, 404);
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  setTransport((url, init) => fetch(url, init));
  setCsrf("");
});

const open = async () => {
  act(() => root.render(<UsageSourcesPage />));
  await flush();
};

describe("the usage sources page", () => {
  it("lists the sources with how they are doing and never a key", async () => {
    await open();
    expect(text()).toContain("Relay");
    expect(text()).toContain("Team pool");
    expect(text()).toContain("12 个账号");
    expect(text()).toContain("31 个窗口");
    expect(text()).toContain("已停用");
    expect(document.body.innerHTML).not.toContain(SECRET);
    expect(host.querySelectorAll('[role="switch"]').length).toBe(3); // the master switch and one per source
  });

  it("switches reading off and sets the interval through the settings route", async () => {
    await open();
    await act(async () => host.querySelector<HTMLElement>('[role="switch"]')!.click());
    await flush();
    expect(calls.find((c) => c.method === "PUT" && c.path === "/usage-sources")?.body).toEqual({ enabled: false });
    await act(async () => byText(host, "15 分钟", "button")!.click());
    await flush();
    expect(calls.filter((c) => c.method === "PUT" && c.path === "/usage-sources").at(-1)?.body).toEqual({ poll_minutes: 15 });
  });

  it("switches one source", async () => {
    await open();
    await act(async () => host.querySelector<HTMLElement>('[role="switch"][aria-label="启用 Team pool"]')!.click());
    await flush();
    expect(calls.find((c) => c.path === "/usage-sources/s2")).toMatchObject({ method: "PUT", body: { enabled: true } });
  });

  it("adds a source: the key goes in the request, the test reads once and saves nothing", async () => {
    await open();
    await act(async () => byText(host, "添加来源")!.click());
    await flush();
    const save = byText(dialog()!, "保存") as HTMLButtonElement;
    expect(save.disabled).toBe(true); // no address, no key
    await typeInto(input("地址"), "https://gateway.example.com/");
    await typeInto(input("Key"), SECRET);
    expect(input("Key").type).toBe("password");
    await act(async () => byText(dialog()!, "测试连接")!.click());
    await flush();
    expect(dialog()!.textContent).toContain("连上了：12 个账号、31 个窗口 (codex 8 · claude 4)");
    expect(calls.find((c) => c.path === "/usage-sources/test")?.body).toMatchObject({ type: "relay", url: "https://gateway.example.com/", key: SECRET });
    expect(calls.some((c) => c.method === "POST" && c.path === "/usage-sources")).toBe(false);
    await act(async () => (byText(dialog()!, "保存") as HTMLButtonElement).click());
    await flush();
    expect(calls.find((c) => c.method === "POST" && c.path === "/usage-sources")?.body).toMatchObject({ type: "relay", url: "https://gateway.example.com/", key: SECRET });
    expect(dialog()).toBeNull();
  });

  it("says why a test failed", async () => {
    testAnswer = { ok: false, code: "unauthorized" };
    await open();
    await act(async () => byText(host, "添加来源")!.click());
    await flush();
    await typeInto(input("地址"), "https://gateway.example.com");
    await typeInto(input("Key"), "wrong");
    await act(async () => byText(dialog()!, "测试连接")!.click());
    await flush();
    expect(dialog()!.textContent).toContain("没连上：Key 不对或已过期");
  });

  it("does not take an address with a password in it", async () => {
    await open();
    await act(async () => byText(host, "添加来源")!.click());
    await flush();
    await typeInto(input("地址"), "https://me:pw@gateway.example.com");
    expect(dialog()!.textContent).toContain("不能带用户名和密码");
    await typeInto(input("Key"), SECRET);
    expect((byText(dialog()!, "保存") as HTMLButtonElement).disabled).toBe(true);
  });

  it("edits a source without showing its key, and asks for it again when the address moves", async () => {
    await open();
    await act(async () => rowActions(0).edit.click());
    await flush();
    expect(input("地址").value).toBe("https://gateway.example.com");
    expect(input("Key").value).toBe("");
    expect(input("Key").placeholder).toContain("已保存");
    expect((byText(dialog()!, "保存") as HTMLButtonElement).disabled).toBe(false); // the saved key stands
    await typeInto(input("地址"), "https://other.example.com");
    expect(dialog()!.textContent).toContain("要重新填 Key");
    expect((byText(dialog()!, "保存") as HTMLButtonElement).disabled).toBe(true);
    await typeInto(input("Key"), SECRET);
    expect((byText(dialog()!, "保存") as HTMLButtonElement).disabled).toBe(false);
    await act(async () => (byText(dialog()!, "保存") as HTMLButtonElement).click());
    await flush();
    expect(calls.find((c) => c.method === "PUT" && c.path === "/usage-sources/s1")?.body).toMatchObject({ url: "https://other.example.com", key: SECRET });
  });

  it("tests the same clear-key choice that it saves", async () => {
    view.sources[1].has_key = true;
    await open();
    await act(async () => rowActions(1).edit.click());
    await flush();
    await typeInto(input("Key"), SECRET);
    await act(async () => dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    expect(input("Key").disabled).toBe(true);
    await act(async () => byText(dialog()!, "测试连接")!.click());
    await flush();
    const tested = calls.find((c) => c.path === "/usage-sources/test")!.body;
    expect(tested).toEqual({ id: "s2", type: "generic", url: "https://pool.example.com/usage", key: SECRET, clear_key: true });
    expect(calls.some((c) => c.method === "PUT" || c.method === "POST" && c.path !== "/usage-sources/test")).toBe(false);
    await act(async () => byText(dialog()!, "保存")!.click());
    await flush();
    expect(calls.find((c) => c.method === "PUT" && c.path === "/usage-sources/s2")!.body).toEqual({ name: "Team pool", type: tested.type, url: tested.url, key: tested.key, clear_key: tested.clear_key });
  });

  it.each([false, true])("clears the previous verdict when clear-key changes from %s", async (checked) => {
    view.sources[1].has_key = true;
    await open();
    await act(async () => rowActions(1).edit.click());
    await flush();
    const checkbox = dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    if (checked) await act(async () => checkbox.click());
    await act(async () => byText(dialog()!, "测试连接")!.click());
    await flush();
    expect(dialog()!.querySelector('[role="status"]')).not.toBeNull();
    await act(async () => checkbox.click());
    expect(checkbox.checked).toBe(!checked);
    expect(dialog()!.querySelector('[role="status"]')).toBeNull();
    await act(async () => byText(dialog()!, "测试连接")!.click());
    await flush();
    const tested = calls.filter((c) => c.path === "/usage-sources/test").at(-1)!.body;
    expect(tested.clear_key).toBe(checked ? undefined : true);
    expect(tested.key).toBeUndefined();
    expect(dialog()!.querySelector('[role="status"]')).not.toBeNull();
  });

  it.each(["success", "failure"])("ignores a late test %s after clear-key changes", async (outcome) => {
    view.sources[1].has_key = true;
    let resolve!: (value: unknown) => void;
    let reject!: (reason: Error) => void;
    testAnswer = new Promise((done, fail) => { resolve = done; reject = fail; });
    await open();
    await act(async () => rowActions(1).edit.click());
    await flush();
    await act(async () => byText(dialog()!, "测试连接")!.click());
    await flush();
    expect(calls.some((c) => c.path === "/usage-sources/test")).toBe(true);
    await act(async () => dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    await act(async () => {
      if (outcome === "success") resolve({ ok: true, accounts: 12, windows: 31, listed: 14, providers: { codex: 12 } });
      else reject(new Error("outdated test failed"));
    });
    await flush();
    expect(dialog()!.querySelector('[role="status"]')).toBeNull();
    expect(dialog()!.querySelector('[role="alert"]')).toBeNull();
    expect((byText(dialog()!, "测试连接") as HTMLButtonElement).disabled).toBe(false);
  });

  it.each(["toggle", "reopen"])("keeps testing occupied through %s until the pending request settles", async (change) => {
    view.sources[1].has_key = true;
    let resolve!: (value: unknown) => void;
    testAnswer = new Promise((done) => { resolve = done; });
    await open();
    await act(async () => rowActions(1).edit.click());
    await flush();
    await act(async () => byText(dialog()!, "测试连接")!.click());
    await flush();
    expect((byText(dialog()!, "测试连接") as HTMLButtonElement).disabled).toBe(true);
    if (change === "toggle") {
      await act(async () => dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    } else {
      await act(async () => byText(dialog()!, "取消")!.click());
      await act(async () => rowActions(0).edit.click());
      await flush();
    }
    const testButton = byText(dialog()!, "测试连接") as HTMLButtonElement;
    expect(testButton.disabled).toBe(true);
    await act(async () => testButton.click());
    expect(calls.filter((c) => c.path === "/usage-sources/test")).toHaveLength(1);
    await act(async () => resolve({ ok: true, accounts: 12, windows: 31, listed: 14, providers: { codex: 12 } }));
    await flush();
    expect(dialog()!.querySelector('[role="status"]')).toBeNull();
    expect(dialog()!.querySelector('[role="alert"]')).toBeNull();
    expect(testButton.disabled).toBe(false);
    testAnswer = { ok: true, accounts: 12, windows: 31, listed: 14, providers: { codex: 12 } };
    await act(async () => testButton.click());
    await flush();
    const tests = calls.filter((c) => c.path === "/usage-sources/test");
    expect(tests).toHaveLength(2);
    expect(tests[1].body.id).toBe(change === "toggle" ? "s2" : "s1");
    expect(tests[1].body.clear_key).toBe(change === "toggle" ? true : undefined);
    expect(dialog()!.querySelector('[role="status"]')).not.toBeNull();
  });

  it("removes a source after asking again in place", async () => {
    await open();
    const row = rowActions(0).box;
    await act(async () => row.querySelector<HTMLElement>('button[aria-label="删除"]')!.click()); // an icon first
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await act(async () => byText(row, "删除")!.click()); // then the confirmation, in words
    await flush();
    expect(calls.find((c) => c.method === "DELETE")?.path).toBe("/usage-sources/s1");
  });

  it("invites adding one when there are none", async () => {
    view.sources = [];
    await open();
    expect(text()).toContain("还没有来源");
  });
});
