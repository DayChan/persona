import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { EXTENSIONS_CHANGED } from "./extension-prompts";
import {
  checkError, clearedSecrets, clearExtensionSecret, compact, configError, fieldTitle, isDirty, listItems, listExtensions, listSummary, numberProblems, placeholder, extensionDetail,
  machineStatus, extensionState, extensionTone, readNumber, runNote, same, secretState, setAt, setExtensionConfig, setExtensionEnabled, setExtensionSecret, sourceText,
  stateLine, valueAt, morePermissions, extensionUpgrades, setExtensionAutoUpgrade, setExtensionUpgrades, upgradeAction, upgradeDetail, upgradeNote,
  type HubExtension, type ExtensionField, type ExtensionUpgrade, nodeProgramStatus, exitText, saveFooter, channelOf, sandboxEgress, sandboxState,
} from "./extensions";

const extension = (status: string, extra: Partial<HubExtension> = {}): HubExtension => ({ id: `p-${status}`, name: status, kind: "connectors", status, ...extra });

// The hub's features as the extension routes read them (lib/extension-api.ts): a hub with the new routes, unless a test
// says otherwise.
const hub = vi.hoisted(() => ({ features: ["extensions"] as string[] }));
vi.mock("./store", async (importOriginal) => {
  const m = await importOriginal<typeof import("./store")>();
  return { ...m, getState: () => ({ ...m.getState(), features: hub.features }) };
});

// The places refresh a state-changing call asks for (lib/extension-slots.ts), counted instead of fetched.
const slots = vi.hoisted(() => ({ refresh: vi.fn() }));
vi.mock("./extension-slots", async (importOriginal) => {
  const m = await importOriginal<typeof import("./extension-slots")>();
  return { ...m, refreshExtensionPlaces: slots.refresh };
});

afterEach(() => {
  vi.unstubAllGlobals();
  slots.refresh.mockReset();
  hub.features = ["extensions"];
});

describe("扩展的文字", () => {
  it("says each state, its tone, and the list's summary", () => {
    // Running or idle until something needs it: both are just on to the user.
    expect(["running", "stopped", "disabled", "starting", "needs_setup"].map((s) => extensionState(extension(s)))).toEqual([
      "启动", "启动", "停用", "正在启动", "需要设置",
    ]);
    expect(extensionState(extension("weird"))).toBe("状态未知");
    expect(["running", "needs_setup", "error", "incompatible", "disabled", "stopped"].map((s) => extensionTone(extension(s)))).toEqual([
      "green", "orange", "red", "red", "idle", "green",
    ]);
    expect(["running", "stopped", "disabled", "error"].map((s) => runNote(extension(s)))).toEqual(["正在运行", "现在没在运行，要用时会自己启动", "", ""]);
    expect(extensionTone(null)).toBe("idle");
    expect(listSummary(null)).toBe("");
    expect(listSummary([])).toBe("没有");
    expect(listSummary([extension("running"), extension("needs_setup"), extension("error"), extension("disabled")])).toBe("4 个 · 2 个要处理");
    expect(listSummary([extension("running")])).toBe("1 个");
  });
  it("puts the state, version and source on one line", () => {
    expect(stateLine(extension("running", { version: "1.2.0", source: "installed" }))).toBe("启动 · 1.2.0 · 已安装");
    expect(stateLine(extension("running", { source: "hub.json" }))).toBe("启动 · manager.json");
    expect(sourceText("other")).toBeUndefined();
  });
  it("says whether an attached extension is online, since when, and the version its program says", () => {
    const at = new Date();
    at.setHours(10, 42, 0, 0);
    const since = at.toISOString();
    const attached = (status: string, a: HubExtension["attached"]) => extension(status, { version: "1.0.0", attached: a });
    expect(runNote(attached("running", { online: true, since }))).toBe("独立运行 · 10:42 起在线");
    // within the hub's 30 seconds it is still starting, with no error of its own: the note says why
    expect(runNote(attached("starting", { online: false, since, error: "连不上" }))).toBe("独立运行 · 正在重连，10:42 起离线：连不上");
    expect(runNote(attached("starting", { online: false, since }))).toBe("独立运行 · 正在重连，10:42 起离线");
    // past them it is an error, which says why itself
    expect(runNote(attached("error", { online: false, since, error: "连不上" }))).toBe("独立运行 · 10:42 起离线");
    expect(runNote(attached("stopped", { online: false }))).toBe("独立运行，需要时再连接");
    expect(runNote(attached("disabled", { online: false }))).toBe("");
    expect(["running", "starting", "error"].map((s) => extensionTone(attached(s, { online: s === "running", since })))).toEqual(["green", "idle", "red"]);
    expect(stateLine(attached("running", { online: true, since }))).toBe("启动 · 1.0.0");
    expect(stateLine(attached("error", { online: false, since, version: "1.1.0" }))).toBe("出错了 · 已批准 1.0.0，程序是 1.1.0");
  });
  it("marks required fields without a default, and shows the default as a placeholder", () => {
    expect(fieldTitle({ name: "url", type: "string", required: true })).toBe("url（必填）");
    expect(fieldTitle({ name: "url", type: "string", title: "地址", required: true, default: "https://x" })).toBe("地址");
    expect(placeholder({ name: "n", type: "integer", default: 30 })).toBe("默认：30");
    expect(placeholder({ name: "m", type: "string", default: "safe", enum: [{ value: "safe", title: "全部核对" }] })).toBe("默认：全部核对");
    expect(placeholder({ name: "s", type: "string" })).toBe("未设置");
    expect(secretState({ name: "k", title: "K", set: false, required: true })).toBe("未设置（必填）");
    expect(secretState({ name: "k", title: "K", set: true, required: true })).toBe("已设置");
  });
  it("shows a set secret's last four characters when the hub gives them, and nothing more without", () => {
    expect(secretState({ name: "k", title: "K", set: true, hint: "7f3a" })).toBe("已设置 · 末 4 位 7f3a");
    expect(secretState({ name: "k", title: "K", set: true, hint: "" })).toBe("已设置");
    expect(secretState({ name: "k", title: "K", set: true })).toBe("已设置");
    // a hint on one that is not set (it should never come) does not make it read as set
    expect(secretState({ name: "k", title: "K", set: false, required: true, hint: "7f3a" })).toBe("未设置（必填）");
  });
  it("reads a save the extension turned down in the hub's words, and nothing else", () => {
    const words = "网关不认这把 Key（401）：invalid api key";
    expect(checkError(new ApiError(400, words, { code: "check_failed", error: words }))).toBe(words);
    expect(checkError(new ApiError(429, "检查太频繁了", { code: "check_rate_limited" }))).toBe("检查太频繁了");
    expect(checkError(new ApiError(409, "设置刚被改过", { code: "settings_moved" }))).toBe("设置刚被改过");
    expect(checkError(new ApiError(400, "bad", { code: "invalid_config" }))).toBeNull();
    expect(checkError(new Error("check_failed"))).toBeNull();
  });
});

describe("草稿", () => {
  it("reads and sets values by path, and drops a group left empty", () => {
    const d = setAt(setAt({}, ["proxy", "host"], "h.example.com"), ["proxy", "port"], 8080);
    expect(d).toEqual({ proxy: { host: "h.example.com", port: 8080 } });
    expect(valueAt(d, ["proxy", "port"])).toBe(8080);
    expect(valueAt(d, ["proxy", "host", "x"])).toBeUndefined();
    expect(valueAt({ a: null }, ["a"])).toBeUndefined();
    const less = setAt(setAt(d, ["proxy", "host"], undefined), ["proxy", "port"], undefined);
    expect(less).toEqual({});
    expect(d).toEqual({ proxy: { host: "h.example.com", port: 8080 } }); // not mutated
  });
  it("compacts what the user cleared, and compares what would be sent", () => {
    expect(compact({ a: "", b: [], c: { d: "" }, e: false, f: 0, g: ["x"], h: null })).toEqual({ e: false, f: 0, g: ["x"] });
    const config = { folders: ["工作"], mode: "safe" };
    expect(isDirty({ mode: "safe", folders: ["工作"] }, config)).toBe(false);
    expect(isDirty({ mode: "safe", folders: ["工作"], base_url: "" }, config)).toBe(false);
    expect(isDirty({ mode: "fast", folders: ["工作"] }, config)).toBe(true);
    expect(isDirty({}, undefined)).toBe(false);
    expect(same([1, { a: 2 }], [1, { a: 2 }])).toBe(true);
    expect(same([1], { 0: 1 })).toBe(false);
  });
  it("reads numbers as typed", () => {
    expect(readNumber(" 30 ", true)).toEqual({ value: 30 });
    expect(readNumber("", true)).toEqual({});
    expect(readNumber("1.5", true)).toEqual({ error: "要是一个整数" });
    expect(readNumber("1.5", false)).toEqual({ value: 1.5 });
    expect(readNumber("abc", false)).toEqual({ error: "要是一个数" });
    expect(numberProblems({ every: { text: "x", integer: true }, port: { text: "80", integer: true } })).toEqual({ every: "要是一个数" });
  });
  it("lists a list field's items, falling back to the default", () => {
    const f: ExtensionField = { name: "tags", type: "array", default: ["a"] };
    expect(listItems({}, ["tags"], f)).toEqual(["a"]);
    expect(listItems({ tags: ["b", 3] }, ["tags"], f)).toEqual(["b"]);
  });
});

describe("扩展的接口", () => {
  it("calls the hub's reserved paths", async () => {
    const calls: { url: string; method: string; body?: string }[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push({ url, method: init.method ?? "GET", body: init.body as string | undefined });
      return new Response(JSON.stringify(url.endsWith("/extensions") ? { extensions: [extension("running")] } : { id: "a b", name: "x", kind: "k", status: "running" }));
    });
    expect((await listExtensions()).map((p) => p.status)).toEqual(["running"]);
    await extensionDetail("a b");
    await setExtensionEnabled("a b", false);
    await setExtensionConfig("a b", { x: 1 });
    await setExtensionSecret("a b", "api_key", "v");
    await clearExtensionSecret("a b", "api_key");
    expect(calls.map((c) => `${c.method} ${c.url} ${c.body ?? ""}`.trim())).toEqual([
      "GET /api/v1/extensions",
      "GET /api/v1/extensions/a%20b/-/",
      'PUT /api/v1/extensions/a%20b/-/enabled {"enabled":false}',
      'PUT /api/v1/extensions/a%20b/-/config {"config":{"x":1}}',
      'PUT /api/v1/extensions/a%20b/-/secrets/api_key {"value":"v"}',
      "DELETE /api/v1/extensions/a%20b/-/secrets/api_key",
    ]);
  });
  it("calls a hub from before #1844 on its old routes, and reads its old keys", async () => {
    hub.features = ["plugins", "plugin_manager"];
    const calls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push(`${init.method ?? "GET"} ${url}`);
      if (url.endsWith("/plugins")) return new Response(JSON.stringify({ plugins: [extension("running")], can_install: true }));
      return new Response(JSON.stringify({ id: "a", name: "x", kind: "k", status: "running", ui: [{ plugin: "a", plugin_name: "A", id: "usage", title: "用量", placement: "plugin_page" }] }));
    });
    expect((await listExtensions()).map((p) => p.status)).toEqual(["running"]);
    const d = await extensionDetail("a");
    expect(d.ui).toEqual([{ extension: "a", extension_name: "A", id: "usage", title: "用量", placement: "plugin_page" }]);
    expect(calls).toEqual(["GET /api/v1/plugins", "GET /api/v1/plugins/a/-/"]);
  });
  it("refreshes the extension's places after a secret is set or cleared, not after a refused one", async () => {
    // A secret change restarts the extension (registry SetSecret / DeleteSecret), so its sidebar entries, cards and
    // quick prompts can appear or go away.
    const events = new EventTarget();
    let fired = 0;
    events.addEventListener(EXTENSIONS_CHANGED, () => fired++);
    vi.stubGlobal("window", events);
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    let fail = false;
    vi.stubGlobal("fetch", async () =>
      fail
        ? new Response(JSON.stringify({ code: "invalid_secret", message: "bad" }), { status: 400 })
        : new Response(JSON.stringify({ id: "a", name: "x", kind: "k", status: "running" })));
    await setExtensionSecret("a", "api_key", "v");
    expect([slots.refresh.mock.calls.length, fired]).toEqual([1, 1]);
    await clearExtensionSecret("a", "api_key");
    expect([slots.refresh.mock.calls.length, fired]).toEqual([2, 2]);
    fail = true;
    await expect(setExtensionSecret("a", "api_key", "v")).rejects.toBeInstanceOf(ApiError);
    await expect(clearExtensionSecret("a", "api_key")).rejects.toBeInstanceOf(ApiError);
    expect([slots.refresh.mock.calls.length, fired]).toEqual([2, 2]);
  });
  it("reads the field the hub refused", () => {
    expect(configError(new ApiError(400, "base_url: 要是一个网址", { code: "invalid_config", field: "base_url", message: "要是一个网址" }))).toEqual({
      field: "base_url", message: "要是一个网址",
    });
    expect(configError(new ApiError(400, "bad", { code: "invalid_config" }))).toEqual({ field: "", message: "bad" });
    expect(configError(new ApiError(500, "x", {}))).toBeNull();
    expect(configError(new Error("x"))).toBeNull();
  });
  it("reads the secrets a refused configuration save cleared anyway", () => {
    expect(clearedSecrets(new ApiError(500, "x", { code: "secret_not_cleared", secrets_cleared: ["api_key", 3, ""] }))).toEqual(["api_key"]);
    expect(clearedSecrets(new ApiError(409, "x", { code: "read_only", secrets_cleared: ["token"] }))).toEqual(["token"]);
    expect(clearedSecrets(new ApiError(409, "x", { code: "read_only" }))).toEqual([]);
    expect(clearedSecrets(new ApiError(500, "x", { secrets_cleared: "api_key" }))).toEqual([]);
    expect(clearedSecrets(new Error("x"))).toEqual([]);
  });
});

describe("an extension's machine switches", () => {
  const mac = { id: "macbook", name: "MacBook", online: true, os: "darwin" };
  it("say online or not, and for a computer extension how Cua Driver is", () => {
    const note = "…";
    expect(machineStatus({ ...mac, online: false, computer: { enabled: true, installed: false, note } }, true)).toBe("离线");
    expect(machineStatus({ ...mac, computer: { enabled: false, installed: false, note } }, true)).toBe("在线 · Cua 没装");
    expect(machineStatus({ ...mac, computer: { enabled: true, installed: true, daemon: false, note } }, true)).toBe("在线 · Cua 没在运行");
    expect(machineStatus({ ...mac, computer: { enabled: true, installed: true, daemon: true, screen_recording: "missing", note } }, true)).toBe("在线 · Cua 缺权限");
    expect(machineStatus({ ...mac, computer: { enabled: true, installed: true, daemon: true, ready: true } }, true)).toBe("在线 · Cua 可以用");
    expect(machineStatus({ ...mac, computer: { enabled: false } }, true)).toBe("在线");
    expect(machineStatus({ ...mac, computer: { enabled: true, ready: true } }, false)).toBe("在线");
  });
});

describe("auto-upgrade (docs/extensions/extensions.md「自动升级」)", () => {
  const up = (state: string, extra: Partial<ExtensionUpgrade> = {}): ExtensionUpgrade => ({ id: "p", market: "m1", from: "1.0.0", to: "1.1.0", state, ...extra });

  it("says what happens to a newer version, and what the user does about it", () => {
    const ask = up("needs_permission", { added_permissions: [{ key: "network", title: "访问更多网络", detail: "status.example.com" }] });
    expect(upgradeDetail(ask)).toBe("要多一项权限：访问更多网络（status.example.com）");
    expect(upgradeAction(ask)).toBe("去确认");
    expect(upgradeDetail(up("waiting_idle"))).toBe("扩展空闲时自动升级");
    expect(upgradeAction(up("waiting_idle"))).toBeNull();
    expect([upgradeDetail(up("off")), upgradeAction(up("off"))]).toEqual(["自动升级关着", "去升级"]);
    expect(upgradeDetail(up("skipped"))).toBe("上次升级后起不来，已回到 1.0.0，不再自动升级到这个版本");
    // A version the user rolled back from by hand (#1826): not "didn't start".
    expect(upgradeDetail(up("skipped", { skipped_by: "user" }))).toBe("你从这个版本回滚到了 1.0.0，不会再自动升级到它");
    expect(upgradeAction(up("skipped", { skipped_by: "user" }))).toBe("去升级");
    expect(upgradeDetail(up("failed", { detail: "下载失败" }))).toBe("没装上：下载失败");
    expect(morePermissions([])).toBe("要的权限有变化");
    expect(morePermissions([{ key: "a", title: "甲" }, { key: "b", title: "乙", detail: "x" }])).toBe("要多两项权限：甲、乙（x）");
  });

  it("tells of the last upgrade only when it is news of its own", () => {
    const at = "2026-11-04T14:34:21.439310005Z";
    expect(upgradeNote({ kind: "upgraded", from: "0.9.0", to: "1.0.0", at })).toBe("已自动升级到 1.0.0（原来是 0.9.0）");
    expect(upgradeNote({ kind: "rolled_back", from: "1.0.0", to: "1.1.0", at })).toBe("1.1.0 起不来，已回到 1.0.0");
    expect(upgradeNote({ kind: "needs_permission", from: "1.0.0", to: "1.1.0", at })).toBeNull();
  });

  it("puts the switches to the hub; a hub without the route has no global switch", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    let has = false;
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url.endsWith("/extensions/-/upgrades")) {
        if (!has) return new Response(JSON.stringify({ error: "no such extension", code: "not_found" }), { status: 404, headers: { "Content-Type": "application/json" } });
        return new Response(JSON.stringify({ auto: init?.method === "PUT" ? JSON.parse(String(init.body)).auto : true, upgrades: [] }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ id: "p", name: "P", kind: "ui", status: "running", auto_upgrade: false }), { headers: { "Content-Type": "application/json" } });
    }));
    expect(await extensionUpgrades()).toBeNull();
    has = true;
    expect(await extensionUpgrades()).toBe(true);
    expect(await setExtensionUpgrades(false)).toBe(false);
    expect((await setExtensionAutoUpgrade("p", false)).auto_upgrade).toBe(false);
    const last = calls[calls.length - 1];
    expect(last.url).toContain("/extensions/p/-/auto-upgrade");
    expect(last.init?.method).toBe("PUT");
    expect(JSON.parse(String(last.init?.body))).toEqual({ auto: false });
  });
});

describe("saveFooter", () => {
  it("says nothing when every field is a list of machines (they say so themselves)", () => {
    const machines = { name: "computers", type: "array", items: { name: "items", type: "string", format: "machine" } } as ExtensionField;
    expect(saveFooter([machines])).toBeUndefined();
    expect(saveFooter([machines, { name: "url", type: "string" } as ExtensionField])).toContain("即时生效；其他项改完点「保存」");
    expect(saveFooter([{ name: "url", type: "string" } as ExtensionField])).toMatch(/^改完点「保存」/);
  });
});

describe("nodeProgramStatus", () => {
  it("says how a plugin's program is on a machine switched on for it, in words the user can act on", () => {
    expect(nodeProgramStatus(undefined)).toBe("等 hub 把程序发过去");
    expect(nodeProgramStatus({ machine_id: "m", state: "running", version: "1.2.0" })).toBe("程序在运行 · 1.2.0");
    expect(nodeProgramStatus({ machine_id: "m", state: "error", error: "exit status 3" })).toBe("程序出错：退出码 3");
    expect(exitText("1.0.0 没有起来：exit status 3")).toBe("1.0.0 没有起来：退出码 3");
    expect(exitText("signal: killed")).toBe("被信号结束（killed）");
    expect(nodeProgramStatus({ machine_id: "m", state: "outdated" })).toContain("personad 太旧");
    expect(nodeProgramStatus({ machine_id: "m", state: "lease_expired" })).toContain("连上后会再启动");
  });
});

describe("sandbox and channel", () => {
  it("says whether the sandbox is in effect and what went through the egress proxy", () => {
    expect(sandboxState({ enabled: true, active: true })).toBe("沙箱正在生效");
    expect(sandboxState({ enabled: true, active: false, why: "no bwrap" })).toBe("要求了沙箱，但现在没开起来");
    expect(sandboxState({ enabled: false, active: false })).toBe("未启用沙箱");
    expect(sandboxEgress({ enabled: true, active: true })).toBeNull();
    expect(sandboxEgress({ enabled: true, active: true, egress: { allowed: 12, denied: 1, last_denied: "example.com:443" } })).toBe("经出网代理：放行 12 次 · 拒绝 1 次");
  });

  it("finds the channel an extension contributes", () => {
    expect(channelOf({ contributes: { channel: ["", "wechat"] } })).toBe("wechat");
    expect(channelOf({ contributes: { connector: ["x"] } })).toBeNull();
    expect(channelOf({})).toBeNull();
  });
});
