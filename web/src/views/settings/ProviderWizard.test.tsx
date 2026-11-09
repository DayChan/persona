// @vitest-environment jsdom
// Settings › Model providers › Add provider starts from the hub's presets (docs/assistant/onboarding.md 4.3 "Three paths" B): the built-in ones listed
// first, the gateway typed by hand last; a preset takes only the key, tests it by itself with the preset, saves it with
// assign and says what was assigned and what was kept.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { resetReadiness, type Readiness } from "../../lib/readiness";
import { getState } from "../../lib/store";
import { ProviderWizard } from "./ProviderWizard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const presets = [
  { id: "builtin/glm", plugin: "builtin", name: "Zhipu GLM", display_name: "智谱 GLM", description: "国内直接开通", kind: "launcher", base_url: "https://open.example.cn/api/anthropic", auth: "bearer", model: "glm-x", signup_url: "https://open.example.cn/keys" },
  { id: "example-gateway/team", plugin: "example-gateway", name: "示例网关", kind: "launcher", base_url: "https://gateway.example.com", model: "example-pro" },
  { id: "example-gateway/api", plugin: "example-gateway", name: "示例 Responses", kind: "api", api: "responses", base_url: "https://api.example.com", model: "example-r" },
];
let calls: { method: string; path: string; body?: Record<string, unknown> }[] = [];
let testOK = true;
// set: what the hub's key test answers instead (a region block, #4526)
let testAnswer: Record<string, unknown> | null = null;
let discovered: Record<string, unknown>[] = [];
let readiness: Readiness | null = null;
// set: readiness answers only once it resolves (a node self-check can take seconds)
let readinessGate: Promise<void> | null = null;
const readinessGets = () => calls.filter((c) => c.path.endsWith("/assistant/readiness")).length;
const step = (id: string, state: string, code?: string) => ({ id, state, ...(code ? { code } : {}) });
const withSteps = (mode: string, steps: Readiness["steps"]): Readiness => ({
  version: 1, mode, ready: mode === "full", next: null, machine: { id: "mbp", name: "小林的 MacBook" }, launcher: "claude", steps, dismissed: false, checked_at: "2026-11-06T09:00:00Z",
});

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0]!;
  const method = init?.method ?? "GET";
  const body = init?.body ? JSON.parse(String(init.body)) : undefined;
  calls.push({ method, path, body });
  if (path.endsWith("/assistant/readiness")) return (readinessGate ?? Promise.resolve()).then(() => (readiness ? json(readiness) : json({ error: "not found" }, 404)));
  if (path.endsWith("/launchers/presets")) return Promise.resolve(json({ presets }));
  if (path.endsWith("/launchers/discover")) return Promise.resolve(json({ models: discovered }));
  if (path.endsWith("/launchers/test") && testAnswer) return Promise.resolve(json(testAnswer));
  if (path.endsWith("/launchers/test")) return Promise.resolve(json(testOK ? { ok: true, ms: 321, model: "glm-x" } : { ok: false, ms: 80, model: "glm-x", error: "Key 不对（401）" }));
  if (path.endsWith("/launchers") && method === "POST")
    return Promise.resolve(json({ launcher: { name: "glm", display_name: "智谱 GLM", has_key: true }, assigned: ["assistant"], kept: [{ purpose: "agent.new", reason: "customized" }] }));
  return Promise.resolve(json({}));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.includes(label));
function type(input: HTMLInputElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const keyInput = () => document.body.querySelector<HTMLInputElement>('input[type="password"]')!;
// The key auto-test debounce (AUTO_TEST_MS in ProviderWizard.tsx); timers are fake, so this costs no real time.
const AUTO_TEST_MS = 700;
const advance = async (ms: number) => {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
};
const keyTests = () => calls.filter((c) => c.path.endsWith("/launchers/test"));
const waitAutoTest = async () => {
  await advance(AUTO_TEST_MS);
  await flush();
};

beforeEach(() => {
  // only timers are faked: promises, fetch and React's scheduling keep running for real
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  Object.assign(getState(), { features: ["builtin_presets"] });
  calls = [];
  testOK = true;
  testAnswer = null;
  discovered = [];
  readiness = null;
  readinessGate = null;
  resetReadiness();
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function open() {
  const saved = vi.fn();
  act(() => root.render(<ProviderWizard existing="new" onClose={() => {}} onSaved={saved} />));
  await flush();
  return saved;
}

describe("ProviderWizard from presets", () => {
  it("lists the hub's presets with the custom gateway last and no hard-coded Anthropic", async () => {
    await open();
    const rows = [...document.body.querySelectorAll("ul li")].map((li) => li.textContent ?? "");
    expect(rows).toHaveLength(4);
    expect(rows[0]).toContain("智谱 GLM");
    expect(rows[1]).toContain("示例网关");
    expect(rows[2]).toContain("示例 Responses");
    expect(rows[3]).toContain("自定义兼容网关");
    expect(text()).not.toContain("Anthropic 官方");
  });

  it("takes only the key, tests it with the preset by itself, and saves it with assign", async () => {
    const saved = await open();
    act(() => button("智谱 GLM")!.click());
    expect(text()).not.toContain("鉴权方式");
    const link = document.body.querySelector<HTMLAnchorElement>('a[href="https://open.example.cn/keys"]');
    expect(link?.target).toBe("_blank");
    expect(link?.rel).toContain("noopener");

    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    const tests = keyTests();
    expect(tests).toHaveLength(1);
    expect(tests[0]!.body).toEqual({ preset: "builtin/glm", api_key: "sk-test-123456", auth: "bearer" });
    expect(text()).toContain("Key 能用");

    await act(async () => button("保存")!.click());
    await flush();
    const post = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"));
    expect(post?.body).toEqual({ api_key: "sk-test-123456", preset: "builtin/glm", assign: ["assistant", "agent.new"] });
    expect(saved).toHaveBeenCalled();
    expect(text()).toContain("智谱 GLM 接好了");
    expect(text()).toContain("现在用它：主对话");
    expect(text()).toContain("新开的 Agent：没有改");
  });

  it("tests the key only once it has stopped changing for the whole debounce", async () => {
    await open();
    act(() => button("智谱 GLM")!.click());
    type(keyInput(), "sk-test-1234");
    await advance(AUTO_TEST_MS - 1);
    expect(keyTests()).toHaveLength(0);
    // another keystroke starts the wait over
    type(keyInput(), "sk-test-123456");
    await advance(AUTO_TEST_MS - 1);
    expect(keyTests()).toHaveLength(0);
    // the last millisecond of the wait sends it
    await advance(1);
    await flush();
    expect(keyTests()).toHaveLength(1);
    expect(keyTests()[0]!.body).toMatchObject({ api_key: "sk-test-123456" });
    expect(text()).toContain("Key 能用");
  });

  it("says why a key failed and leaves assign out when turned off", async () => {
    testOK = false;
    await open();
    act(() => button("智谱 GLM")!.click());
    type(keyInput(), "sk-wrong-123456");
    await waitAutoTest();
    expect(text()).toContain("Key 不对（401）");
    expect(button("重新测试")).toBeDefined();

    act(() => document.body.querySelector<HTMLButtonElement>('button[role="switch"]')!.click());
    await act(async () => button("保存")!.click());
    await flush();
    const post = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"));
    expect(post?.body).not.toHaveProperty("assign");
    expect(text()).toContain("到「模型配置」里选");
  });

  // #4526: the region, not the key, is the problem (#4482): no retesting, a way back to the list, and the provider is
  // not put on the main conversation and new agents unless asked for.
  it("offers another provider instead of retesting when the region is blocked, and does not assign by default", async () => {
    testAnswer = { ok: false, ms: 120, model: "glm-x", code: "region_blocked", error: "Anthropic 不对这个地区提供服务：换 Key 也没用，换一个在这里能直接用的供应商" };
    await open();
    act(() => button("智谱 GLM")!.click());
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    expect(text()).toContain("换 Key 也没用");
    expect(button("重新测试")).toBeUndefined();
    expect(button("换一家")).toBeDefined();
    const toggle = document.body.querySelector<HTMLButtonElement>('button[role="switch"]')!;
    expect(toggle.getAttribute("aria-checked")).toBe("false");

    await act(async () => button("保存")!.click());
    await flush();
    const post = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"));
    expect(post?.body).not.toHaveProperty("assign");
  });

  it("still assigns a region-blocked provider when the switch is turned on", async () => {
    testAnswer = { ok: false, ms: 120, code: "region_blocked", error: "Anthropic 不对这个地区提供服务" };
    await open();
    act(() => button("智谱 GLM")!.click());
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    act(() => document.body.querySelector<HTMLButtonElement>('button[role="switch"]')!.click());
    await act(async () => button("保存")!.click());
    await flush();
    const post = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"));
    expect(post?.body).toMatchObject({ assign: ["assistant", "agent.new"] });
  });

  it("goes back to the providers from a blocked region's 换一家", async () => {
    testAnswer = { ok: false, ms: 120, code: "region_blocked", error: "Anthropic 不对这个地区提供服务" };
    await open();
    act(() => button("智谱 GLM")!.click());
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    act(() => button("换一家")!.click());
    await flush();
    const rows = [...document.body.querySelectorAll("ul li")].map((li) => li.textContent ?? "");
    expect(rows).toHaveLength(4);
    expect(keyInput()).toBeNull();
  });

  it("does not carry a provider's key to the next one picked after 换一家", async () => {
    testAnswer = { ok: false, ms: 120, code: "region_blocked", error: "Anthropic 不对这个地区提供服务" };
    await open();
    act(() => button("智谱 GLM")!.click());
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    act(() => button("换一家")!.click());
    await flush();
    act(() => button("示例网关")!.click());
    expect(keyInput()!.value).toBe("");
    await waitAutoTest();
    const tests = calls.filter((c) => c.method === "POST" && c.path.endsWith("/launchers/test"));
    expect(tests.map((c) => (c.body as { preset?: string }).preset)).toEqual(["builtin/glm"]);
  });

  it("asks an older hub's preset for the name and sends no assign", async () => {
    Object.assign(getState(), { features: ["channels"] });
    await open();
    act(() => button("示例网关")!.click());
    expect(document.body.querySelector('button[role="switch"]')).toBeNull();
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    expect(button("保存")!.disabled).toBe(true);
    type(document.body.querySelector<HTMLInputElement>('input:not([type="password"])')!, "jv_team");
    expect(button("保存")!.disabled).toBe(false);
    await act(async () => button("保存")!.click());
    await flush();
    const post = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"));
    expect(post?.body).toEqual({ name: "jv_team", api_key: "sk-test-123456", preset: "example-gateway/team" });
  });

  it("tests a Responses preset using its configured protocol", async () => {
    await open();
    act(() => button("示例 Responses")!.click());
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    expect(keyTests()).toHaveLength(1);
    expect(keyTests()[0]!.body).toMatchObject({ preset: "example-gateway/api", api_key: "sk-test-123456" });
    expect(text()).toContain("Key 能用");
    expect(button("保存")!.disabled).toBe(false);
  });

  it("asks for the address and the auth only for the custom gateway", async () => {
    await open();
    act(() => button("自定义兼容网关")!.click());
    expect(text()).toContain("鉴权方式");
    expect(text()).toContain("地址");
  });

  it("lets a custom Responses provider test and discover using its selected protocol", async () => {
    await open();
    act(() => button("自定义兼容网关")!.click());
    act(() => button("直接 API")!.click());
    act(() => button("OpenAI Responses")!.click());
    const inputs = [...document.body.querySelectorAll<HTMLInputElement>("input")];
    type(inputs.find((input) => input.placeholder.includes("my_gateway"))!, "custom");
    type(inputs.find((input) => input.inputMode === "url")!, "https://api.example.com/prefix");
    type(keyInput(), "sk-test-123456");
    type(document.body.querySelector<HTMLInputElement>('input[aria-label="测试用的模型"]')!, "example-r");
    await act(async () => button("测试连接")!.click());
    await act(async () => button("获取模型列表")!.click());
    expect(keyTests()[0]!.body).toMatchObject({ api: "responses", model: "example-r", base_url: "https://api.example.com/prefix" });
    expect(calls.find((c) => c.path.endsWith("/launchers/discover"))?.body).toMatchObject({ api: "responses" });
  });

  it("tests, discovers and saves a Chat Completions provider", async () => {
    await open();
    act(() => button("自定义兼容网关")!.click());
    act(() => button("直接 API")!.click());
    act(() => button("Chat Completions")!.click());
    expect(text()).toContain("Chat Completions 兼容 API 的基础地址");
    expect(text()).not.toContain("鉴权方式");
    const inputs = [...document.body.querySelectorAll<HTMLInputElement>("input")];
    type(inputs.find((input) => input.placeholder.includes("my_gateway"))!, "custom");
    type(inputs.find((input) => input.inputMode === "url")!, "https://api.example.com/v1");
    type(keyInput(), "sk-test-123456");
    type(document.body.querySelector<HTMLInputElement>('input[aria-label="测试用的模型"]')!, "example-chat");
    await act(async () => button("测试连接")!.click());
    await act(async () => button("获取模型列表")!.click());
    expect(keyTests()[0]!.body).toMatchObject({ api: "chat", model: "example-chat", base_url: "https://api.example.com/v1" });
    expect(calls.find((c) => c.path.endsWith("/launchers/discover"))?.body).toMatchObject({ api: "chat" });
    act(() => button("下一步")!.click());
    type(document.body.querySelector<HTMLInputElement>('input[aria-label="手动添加模型 ID"]')!, "example-chat");
    act(() => button("添加")!.click());
    await act(async () => button("保存")!.click());
    expect(calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"))?.body).toMatchObject({ kind: "api", api: "chat", auth: "api_key", model: "example-chat" });
  });

  it("saves discovered Chat models with inconsistent effort metadata", async () => {
    discovered = [{ id: "example-chat", context_window: 16000, efforts: ["low"], default_effort: "high" }];
    await open();
    act(() => button("自定义兼容网关")!.click());
    act(() => button("直接 API")!.click());
    act(() => button("Chat Completions")!.click());
    const inputs = [...document.body.querySelectorAll<HTMLInputElement>("input")];
    type(inputs.find((input) => input.placeholder.includes("my_gateway"))!, "custom");
    type(inputs.find((input) => input.inputMode === "url")!, "https://api.example.com/v1");
    type(keyInput(), "sk-test-123456");
    await act(async () => button("获取模型列表")!.click());
    act(() => button("下一步")!.click());
    expect(button("保存")!.disabled).toBe(false);
    await act(async () => button("保存")!.click());
    expect(calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"))?.body).toMatchObject({
      api: "chat", models: [{ id: "example-chat", context_window: 16000 }],
    });
  });

  it("hides unsupported chat options and excludes drafts from test and save", async () => {
    await open();
    act(() => button("自定义兼容网关")!.click());
    act(() => button("直接 API")!.click());
    const inputs = [...document.body.querySelectorAll<HTMLInputElement>("input")];
    type(inputs.find((input) => input.placeholder.includes("my_gateway"))!, "custom");
    type(inputs.find((input) => input.inputMode === "url")!, "https://api.example.com/v1");
    type(keyInput(), "sk-test-123456");
    act(() => button("下一步")!.click());
    type(document.body.querySelector<HTMLInputElement>('input[aria-label="手动添加模型 ID"]')!, "example-chat");
    act(() => button("添加")!.click());
    act(() => button("高级")!.click());
    const header = document.body.querySelector<HTMLTextAreaElement>('textarea[placeholder^="X-Name"]')!;
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    act(() => { setter.call(header, "X-Example: value"); header.dispatchEvent(new Event("input", { bubbles: true })); });
    type(document.body.querySelector<HTMLInputElement>('input[placeholder="http://127.0.0.1:7890"]')!, "http://proxy.example.com");
    type(document.body.querySelector<HTMLInputElement>('input[placeholder="localhost,127.0.0.1"]')!, "example.com");
    type(document.body.querySelector<HTMLInputElement>('input[placeholder="3000000"]')!, "1234");
    act(() => button("上一步")!.click());
    act(() => button("Chat Completions")!.click());
    await act(async () => button("测试连接")!.click());
    expect(keyTests()[0]!.body).not.toHaveProperty("headers");
    act(() => button("下一步")!.click());
    expect(text()).toContain("上下文限制、默认思考档位");
    expect(text()).toContain("模型列表中仍可记录各模型的上下文窗口");
    expect(document.body.querySelector('textarea[placeholder^="X-Name"]')).toBeNull();
    expect(document.body.querySelector('input[placeholder="http://127.0.0.1:7890"]')).toBeNull();
    expect(document.body.querySelector('input[placeholder="localhost,127.0.0.1"]')).toBeNull();
    expect(document.body.querySelector('input[placeholder="3000000"]')).toBeNull();
    expect(document.body.querySelector('textarea[placeholder^="NAME=value"]')).toBeNull();
    act(() => button("上一步")!.click());
    act(() => button("Anthropic Messages")!.click());
    act(() => button("下一步")!.click());
    expect(document.body.querySelector<HTMLInputElement>('input[placeholder="3000000"]')?.value).toBe("1234");
    expect(document.body.querySelector<HTMLInputElement>('input[placeholder="http://127.0.0.1:7890"]')?.value).toBe("http://proxy.example.com");
    act(() => button("上一步")!.click());
    act(() => button("Chat Completions")!.click());
    act(() => button("下一步")!.click());
    await act(async () => button("保存")!.click());
    const body = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"))?.body;
    expect(body).toMatchObject({ api: "chat" });
    for (const key of ["headers", "proxy", "no_proxy", "env", "timeout_ms", "compact_window", "max_context", "effort"]) expect(body).not.toHaveProperty(key);
  });

  it("keeps model metadata while hiding unused chat thinking controls", async () => {
    act(() => root.render(<ProviderWizard existing={{ name: "chat", kind: "api", api: "chat", base_url: "https://api.example.com", model: "example-chat", has_key: true, models: [{ id: "example-chat", context_window: 16000, efforts: ["high"], default_effort: "high" }] }} onClose={() => {}} onSaved={() => {}} />));
    await flush();
    act(() => button("下一步")!.click());
    expect(document.body.querySelector<HTMLInputElement>('input[aria-label="example-chat 的上下文窗口"]')?.value).toBe("16000");
    expect(document.body.querySelector('[aria-label="example-chat 支持的思考档位"]')).toBeNull();
    expect(document.body.querySelector('[aria-label="example-chat 的默认思考档位"]')).toBeNull();
    expect(text()).toContain("Chat Completions 不使用模型的思考档位设置");
    await act(async () => button("保存")!.click());
    expect(calls.find((c) => c.method === "PUT" && c.path.endsWith("/launchers/chat"))?.body).not.toHaveProperty("models");
  });

  it("preserves existing Chat effort metadata when discovery updates context", async () => {
    discovered = [{ id: "example-chat", context_window: 32000, efforts: ["low"], default_effort: "low" }];
    act(() => root.render(<ProviderWizard existing={{ name: "chat", kind: "api", api: "chat", base_url: "https://api.example.com", model: "example-chat", has_key: true, models: [{ id: "example-chat", efforts: ["high"], default_effort: "high" }] }} onClose={() => {}} onSaved={() => {}} />));
    await flush();
    await act(async () => button("获取模型列表")!.click());
    act(() => button("下一步")!.click());
    await act(async () => button("保存")!.click());
    expect(calls.find((c) => c.method === "PUT" && c.path.endsWith("/launchers/chat"))?.body).toMatchObject({
      models: [{ id: "example-chat", context_window: 32000, efforts: ["high"], default_effort: "high" }],
    });
  });

  it("requires an explicit choice before clearing unsupported stored Chat settings", async () => {
    const existing = { name: "chat", kind: "api" as const, api: "chat" as const, base_url: "https://api.example.com", model: "example-chat", has_key: true, has_headers: true, has_env: true, proxy: "http://proxy.example.com", no_proxy: "example.com", timeout_ms: 1200, compact_window: 100, max_context: 200, effort: "high", opus_model: "tier-a", sonnet_model: "tier-b", haiku_model: "tier-c", fable_model: "tier-d", subagent_force: true };
    act(() => root.render(<ProviderWizard existing={existing} onClose={() => {}} onSaved={() => {}} />));
    await flush();
    expect(text()).toContain("保存时清除这些设置");
    expect(text()).toContain("请先清除已保存的设置并保存供应商，再测试连接或获取模型列表");
    expect(button("测试连接")!.disabled).toBe(true);
    expect(button("获取模型列表")!.disabled).toBe(true);
    act(() => { button("测试连接")!.click(); button("获取模型列表")!.click(); });
    expect(calls.some((c) => /\/launchers\/(test|discover)$/.test(c.path))).toBe(false);
    const checkbox = document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    expect(checkbox.checked).toBe(false);
    expect(button("保存")!.disabled).toBe(true);
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    act(() => checkbox.click());
    expect(button("保存")!.disabled).toBe(false);
    expect(button("测试连接")!.disabled).toBe(true);
    expect(button("获取模型列表")!.disabled).toBe(true);
    act(() => { button("测试连接")!.click(); button("获取模型列表")!.click(); });
    expect(calls.some((c) => /\/launchers\/(test|discover)$/.test(c.path))).toBe(false);
    act(() => checkbox.click());
    expect(button("保存")!.disabled).toBe(true);
    act(() => checkbox.click());
    await act(async () => button("保存")!.click());
    expect(calls.find((c) => c.method === "PUT" && c.path.endsWith("/launchers/chat"))?.body).toMatchObject({
      headers: "", env: "", proxy: "", no_proxy: "", timeout_ms: 0, compact_window: 0, max_context: 0,
      effort: "", opus_model: "", sonnet_model: "", haiku_model: "", fable_model: "", subagent_force: false,
    });
  });

  it("keeps the Chat Completions protocol when editing", async () => {
    act(() => root.render(<ProviderWizard existing={{ name: "chat", kind: "api", api: "chat", base_url: "https://api.example.com", model: "example-chat", has_key: true }} onClose={() => {}} onSaved={() => {}} />));
    await flush();
    expect(text()).toContain("Chat Completions 兼容 API 的基础地址");
    expect(text()).not.toContain("鉴权方式");
    await act(async () => button("保存")!.click());
    expect(calls.find((c) => c.method === "PUT" && c.path.endsWith("/launchers/chat"))?.body).toMatchObject({ api: "chat", auth: "api_key" });
  });

  it("leaves a Claude Code that already answers in place unless told otherwise", async () => {
    Object.assign(getState(), { features: ["builtin_presets", "assistant_readiness"] });
    readiness = withSteps("full", [step("computer", "ok"), step("claude", "ok"), step("model", "ok"), step("first_message", "ok")]);
    await open();
    act(() => button("智谱 GLM")!.click());
    expect(document.body.querySelector('button[role="switch"]')?.getAttribute("aria-checked")).toBe("false");
    expect(text()).toContain("小林的 MacBook 上的 Claude Code，已经能用");
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    const before = readinessGets();
    await act(async () => button("保存")!.click());
    await flush();
    const post = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"));
    expect(post?.body).not.toHaveProperty("assign");
    // saving asks for the readiness again, so the main conversation's card follows
    expect(readinessGets()).toBeGreaterThan(before);
    expect(document.body.querySelector('[data-testid="wizard-no-claude"]')).toBeNull();
  });

  it("waits for a slow readiness before deciding to take over the main conversation", async () => {
    Object.assign(getState(), { features: ["builtin_presets", "assistant_readiness"] });
    readiness = withSteps("full", [step("computer", "ok"), step("claude", "ok"), step("model", "ok"), step("first_message", "ok")]);
    let answer!: () => void;
    readinessGate = new Promise((r) => (answer = r));
    await open();
    act(() => button("智谱 GLM")!.click());
    // not known yet: no switch to read as on
    expect(document.body.querySelector('button[role="switch"]')).toBeNull();
    expect(text()).toContain("正在看主对话现在能不能用");
    type(keyInput(), "sk-test-123456");
    // the key test runs and saving is asked for while the readiness is still out: only the gate releases it
    await waitAutoTest();
    expect(keyTests()).toHaveLength(1);
    expect(document.body.querySelector('button[role="switch"]')).toBeNull();
    await act(async () => button("保存")!.click());
    await flush();
    expect(calls.some((c) => c.method === "POST" && c.path.endsWith("/launchers"))).toBe(false);
    await act(async () => answer());
    await flush();
    const post = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"));
    expect(post?.body).not.toHaveProperty("assign");
  });

  it("says Claude Code is missing without promising the main conversation when the key did not go to it", async () => {
    Object.assign(getState(), { features: ["builtin_presets", "assistant_readiness"] });
    readiness = withSteps("setup", [step("computer", "ok"), step("claude", "problem", "claude_missing"), step("model", "unknown"), step("first_message", "todo")]);
    await open();
    act(() => button("智谱 GLM")!.click());
    act(() => (document.body.querySelector('button[role="switch"]') as HTMLButtonElement).click());
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    await act(async () => button("保存")!.click());
    await flush();
    const note = document.body.querySelector('[data-testid="wizard-no-claude"]');
    expect(note?.textContent).toContain("Key 存好了；要用它，还得先在 小林的 MacBook 上装 Claude Code。");
  });

  it("says the key is saved but the computer still needs Claude Code", async () => {
    Object.assign(getState(), { features: ["builtin_presets", "assistant_readiness"] });
    readiness = withSteps("setup", [step("computer", "ok"), step("claude", "problem", "claude_missing"), step("model", "unknown"), step("first_message", "todo")]);
    await open();
    act(() => button("智谱 GLM")!.click());
    // nothing answers yet: the new key takes over the defaults
    expect(document.body.querySelector('button[role="switch"]')?.getAttribute("aria-checked")).toBe("true");
    type(keyInput(), "sk-test-123456");
    await waitAutoTest();
    // after saving the hub may no longer say it (a provider of its own reads unknown): what it said before still holds
    readiness = withSteps("setup", [step("computer", "ok"), step("claude", "unknown"), step("model", "unknown"), step("first_message", "todo")]);
    await act(async () => button("保存")!.click());
    await flush();
    const note = document.body.querySelector('[data-testid="wizard-no-claude"]');
    expect(note?.textContent).toContain("Key 存好了；还要在 小林的 MacBook 上装 Claude Code，主对话才能用它。");
    act(() => button("怎么装")!.click());
    expect(text()).toContain("claude.ai/install.sh");
  });
});
