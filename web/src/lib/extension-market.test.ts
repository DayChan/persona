import { afterEach, describe, expect, it, vi } from "vitest";
import {
  addMarket, agreeLabel, canUninstall, catalogOf, catalogQuery, getCatalog, installPreview, isGitMarket, itemLine, itemState, listMarkets, MARKET_SPEC_HINT, marketView, marketKind, marketLine, marketOf,
  extensionsRoute, previewFromMarket, previewVersion, renameConsent, refreshMarket, removeMarket, routeSub, setMarketToken, specLabel, uninstallExtension, upgradePreview, upgradingFrom, wantsGitHubToken,
  type CatalogItem, type Market, type MarketPreview, type ExtensionsRoute,
} from "./extension-market";

// The hub's features as the extension routes read them (lib/extension-api.ts): a hub with the new routes, unless a test
// says otherwise.
const hub = vi.hoisted(() => ({ features: ["extensions"] as string[] }));
vi.mock("./store", async (importOriginal) => {
  const m = await importOriginal<typeof import("./store")>();
  return { ...m, getState: () => ({ ...m.getState(), features: hub.features }) };
});

afterEach(() => {
  vi.unstubAllGlobals();
  hub.features = ["extensions"];
});

const item = (extra: Partial<CatalogItem> = {}): CatalogItem => ({ market: "m1", market_name: "Example 扩展", id: "example-cal", name: "Example 日历", version: "1.1.0", ...extra });

describe("市场的规矩", () => {
  it("says where a catalog extension stands", () => {
    expect(itemState(item())).toEqual({ kind: "install", label: "未安装", tone: "gray" });
    expect(itemState(item({ problem: "没有这台 hub 能用的包" }))).toMatchObject({ kind: "problem", label: "装不了" });
    expect(itemState(item({ installed: "1.0.0", installed_source: "installed", upgradable: true }))).toMatchObject({ kind: "upgrade", label: "可升级到 1.1.0" });
    expect(itemState(item({ installed: "1.1.0", installed_source: "installed" }))).toMatchObject({ kind: "installed", label: "已装 1.1.0" });
    expect(itemState(item({ installed: "1.0.0", installed_source: "builtin" }))).toMatchObject({ kind: "elsewhere", label: "hub 自带" });
    expect(itemState(item({ installed: "1.0.0", installed_source: "hub.json" }))).toMatchObject({ kind: "elsewhere" });
    expect(itemState(item({ installed: "1.0.0", installed_source: "installed", upgradable: true, problem: "要更新的 hub" }))).toMatchObject({ kind: "problem" });
  });
  it("uninstalls only what this market installed", () => {
    expect(canUninstall(item({ installed: "1.0.0", from_this_market: true }))).toBe(true);
    expect(canUninstall(item({ installed: "1.0.0" }))).toBe(false);
    expect(canUninstall(item({ from_this_market: true }))).toBe(false);
    expect(marketOf("market:3f2a/example-gateway")).toBe("3f2a");
    expect(marketOf("dir:/opt/x")).toBe("");
    expect(marketOf(undefined)).toBe("");
  });
  it("leaves the address to the hub, and knows git markets (no token)", () => {
    expect(["git:git@example.com:team/extensions.git#main", "git@example.com:team/extensions.git", "git:ssh://git@example.com/team/extensions.git", "git:https://example.com/team/extensions.git"].map(isGitMarket)).toEqual([true, true, true, true]);
    expect(["https://example.com/extensions/", "github:example/x", "not even an address"].map(isGitMarket)).toEqual([false, false, false]);
    expect(["github:example/x", "git@example.com:a/b.git", "https://example.com/"].map(marketKind)).toEqual(["github", "git", "web"]);
    expect(specLabel("git:ssh://git@example.com/team/extensions.git")).toBe("ssh://git@example.com/team/extensions.git");
    for (const w of ["https", "github:owner/repo", "git:"]) expect(MARKET_SPEC_HINT).toContain(w);
  });
  it("wants a token for a GitHub market without one", () => {
    const base = { id: "m1", name: "x", extensions: 0 };
    expect(wantsGitHubToken({ ...base, spec: "github:example/x", has_token: false })).toBe(true);
    expect(wantsGitHubToken({ ...base, spec: "github:example/x", has_token: true })).toBe(false);
    expect(wantsGitHubToken({ ...base, spec: "https://example.com/persona-extensions.json", has_token: false })).toBe(false);
    expect(wantsGitHubToken({ ...base, spec: "git:git@example.com:team/extensions.git", has_token: false })).toBe(false);
  });
  it("words a market and a preview", () => {
    const m: Market = { id: "m1", spec: "github:example/x", name: "x", has_token: true, extensions: 3, fetched_at: "2026-11-04T06:00:00Z" };
    expect(marketLine(m, () => "1 小时前")).toBe("3 个扩展 · 1 小时前读过 · 有 token");
    expect(marketLine({ ...m, has_token: false, fetched_at: undefined }, () => "")).toBe("3 个扩展 · 还没读过");
    expect(marketLine({ ...m, signature: { state: "verified", key_id: "32ed46cdbe349656" } }, () => "1 小时前")).toBe("3 个扩展 · 1 小时前读过 · 有 token · 签名已验证");
    expect(marketLine({ ...m, signature: { state: "key_changed" } }, () => "1 小时前")).toContain("官方签名 key 不匹配，等待验签");
    expect(specLabel("github:example/x")).toBe("example/x");
    expect(specLabel("https://extensions.example.com/persona/persona-extensions.json")).toBe("extensions.example.com/persona/persona-extensions.json");
    expect(specLabel("https://example.com/")).toBe("example.com");
    expect(itemLine(item({ publisher: "example" }))).toBe("1.1.0 · example · Example 扩展");
    const pv: MarketPreview = { preview_id: "p", manifest: { id: "x", name: "X", version: "1.1.0" }, permissions: [], permissions_digest: "d", expires_at: "" };
    expect([previewVersion(pv), agreeLabel(pv)]).toEqual(["1.1.0", "同意并安装"]);
    expect([previewVersion({ ...pv, upgrade: true, from: "1.0.0" }), agreeLabel({ ...pv, upgrade: true })]).toEqual(["1.0.0 → 1.1.0", "升级"]);
    expect(agreeLabel({ ...pv, upgrade: true, more_permissions: true })).toBe("同意新权限并升级");
  });
  it("reads and writes the tab's route", () => {
    const cases: [string | null, ExtensionsRoute][] = [
      [null, { kind: "list" }],
      ["example-notes", { kind: "extension", id: "example-notes" }],
      ["example-gateway/ui/home", { kind: "page", id: "example-gateway", ui: "home" }],
      // another manager's (several managers: the same id under each)
      ["voice@m-3fa21c", { kind: "extension", id: "voice", manager: "m-3fa21c" }],
      ["example-gateway@m-3fa21c/ui/home", { kind: "page", id: "example-gateway", ui: "home", manager: "m-3fa21c" }],
      ["-market", { kind: "catalog" }],
      ["-market/3f2a/example-cal", { kind: "item", market: "3f2a", id: "example-cal" }],
      ["-markets", { kind: "markets" }],
    ];
    for (const [sub, r] of cases) {
      expect(extensionsRoute(sub)).toEqual(r);
      expect(routeSub(r)).toBe(sub);
    }
    expect(extensionsRoute("-weird")).toEqual({ kind: "list" });
    expect(extensionsRoute("a/b")).toEqual({ kind: "list" });
  });
});

describe("市场的接口", () => {
  it("returns every uninstall warning without changing the successful result", async () => {
    const warnings = ["清理 /tmp/example-one", "清理 /tmp/example-two", "清理 /tmp/example-one"];
    vi.stubGlobal("localStorage", { getItem: () => "t" });
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ ok: true, warnings })));
    expect(await uninstallExtension("example")).toEqual({ ok: true, warnings });
  });
  it("requires identity consent and upgrades the installed predecessor", async () => {
    const pv: MarketPreview = { preview_id: "rename-pv", manifest: { id: "new-name", name: "Example", version: "2.0.0" },
      upgrade: true, rename_from: "old-name", from: "1.0.0", permissions: [], permissions_digest: "confirmed-digest", expires_at: "" };
    expect(agreeLabel(pv)).toBe("确认更名");
    expect(renameConsent(pv)).toContain("old-name");
    expect(renameConsent(pv)).toContain("new-name");
    expect(renameConsent({ ...pv, rename_from: undefined })).toBe("");
    const calls: { url: string; body: unknown; manager: string | null }[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push({ url, body: JSON.parse(String(init.body)), manager: new Headers(init.headers).get("X-Persona-Manager") });
      return new Response(JSON.stringify({ id: "new-name", name: "Example" }));
    });
    await upgradePreview("new-name", pv, "manager-two");
    expect(calls).toEqual([{ url: "/api/v1/extensions/old-name/-/upgrade", body: {
      preview_id: "rename-pv", permissions_digest: "confirmed-digest" }, manager: "manager-two" }]);
  });
  it("calls the hub's market routes", async () => {
    const calls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push(`${init.method ?? "GET"} ${url} ${(init.body as string) ?? ""}`.trim());
      if (url.endsWith("/markets") && !init.method) return new Response(JSON.stringify({ markets: [{ id: "m1" }] }));
      if (url.includes("/catalog?") || url.endsWith("/catalog")) return new Response(JSON.stringify({ extensions: [item()] }));
      return new Response(JSON.stringify({ id: "x", name: "X", version: "1" }));
    });
    expect((await listMarkets()).map((m) => m.id)).toEqual(["m1"]);
    await addMarket("github:example/x");
    await addMarket("github:example/y", "tok");
    await removeMarket("m 1");
    await refreshMarket("m1");
    await setMarketToken("m1", "");
    const c = await getCatalog(" 日历 ", "m1", true);
    expect(c.markets).toEqual([]);
    expect(c.extensions).toHaveLength(1);
    await getCatalog();
    const pv: MarketPreview = { preview_id: "pv1", manifest: { id: "x", name: "X", version: "1" }, permissions: [], permissions_digest: "d1", expires_at: "" };
    await previewFromMarket("m1", "x");
    await installPreview(pv);
    await upgradePreview("x", pv);
    await uninstallExtension("x");
    await uninstallExtension("x", true);
    expect(calls).toEqual([
      "GET /api/v1/extensions/-/markets",
      'POST /api/v1/extensions/-/markets {"spec":"github:example/x"}',
      'POST /api/v1/extensions/-/markets {"spec":"github:example/y","token":"tok"}',
      "DELETE /api/v1/extensions/-/markets/m%201",
      "POST /api/v1/extensions/-/markets/m1/refresh",
      'PUT /api/v1/extensions/-/markets/m1/token {"token":""}',
      "GET /api/v1/extensions/-/catalog?q=%E6%97%A5%E5%8E%86&market=m1&refresh=1",
      "GET /api/v1/extensions/-/catalog",
      'POST /api/v1/extensions/-/catalog/preview {"market":"m1","extension":"x","plugin":"x"}',
      'POST /api/v1/extensions/-/install {"preview_id":"pv1","permissions_digest":"d1"}',
      'POST /api/v1/extensions/x/-/upgrade {"preview_id":"pv1","permissions_digest":"d1"}',
      "DELETE /api/v1/extensions/x/-/",
      "DELETE /api/v1/extensions/x/-/?keep_data=1",
    ]);
    expect(catalogQuery()).toBe("");
  });
  it("calls a hub from before #1844 on its old routes, and reads its old keys", async () => {
    hub.features = ["plugins", "plugin_manager"];
    const calls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push(`${init.method ?? "GET"} ${url}`);
      if (url.endsWith("/markets")) return new Response(JSON.stringify({ markets: [{ id: "m1", plugins: 3 }] }));
      return new Response(JSON.stringify({ markets: [{ id: "m1", plugins: 3 }], plugins: [item()] }));
    });
    expect((await listMarkets()).map((m) => m.extensions)).toEqual([3]);
    const c = await getCatalog();
    expect(c.extensions).toHaveLength(1);
    expect(c.markets[0].extensions).toBe(3);
    expect(calls).toEqual(["GET /api/v1/plugins/-/markets", "GET /api/v1/plugins/-/catalog"]);
  });
});

describe("GITHUB_TOKEN_URL", () => {
  it("opens GitHub's fine-grained token page filled in, within GitHub's limits", async () => {
    const { GITHUB_TOKEN_URL, isGitHubMarket } = await import("./extension-market");
    const u = new URL(GITHUB_TOKEN_URL);
    expect(u.origin + u.pathname).toBe("https://github.com/settings/personal-access-tokens/new");
    expect((u.searchParams.get("name") ?? "").length).toBeGreaterThan(0);
    expect((u.searchParams.get("name") ?? "").length).toBeLessThanOrEqual(40);
    expect(Number(u.searchParams.get("expires_in"))).toBeGreaterThanOrEqual(1);
    expect(Number(u.searchParams.get("expires_in"))).toBeLessThanOrEqual(366);
    expect([...u.searchParams.keys()].some((k) => k === "contents" || k.endsWith("_permissions"))).toBe(false); // no permission asked
    expect(isGitHubMarket({ spec: "github:example/extensions" })).toBe(true);
    expect(isGitHubMarket({ spec: "https://example.com/extensions/" })).toBe(false);
  });
});

// #5017: the official market reads Official extensions whatever its index calls itself; the others keep their own names.
describe("官方市场的名字", () => {
  const official = { id: "o1", spec: "github:example/official", name: "Example 扩展", has_token: false, extensions: 1, warnings: [], official: true } as Market;
  const own = { ...official, id: "m2", spec: "https://extensions.example.com", name: "团队扩展", official: undefined } as Market;
  it("names the official market, and its catalog extensions' market, 官方扩展", () => {
    expect(marketView(official).name).toBe("官方扩展");
    expect(marketView(own).name).toBe("团队扩展");
    const c = catalogOf({ markets: [official, own], extensions: [item({ market: "o1" }), item({ market: "m2", market_name: "团队扩展" })] });
    expect(c.extensions.map((it) => it.market_name)).toEqual(["官方扩展", "团队扩展"]);
  });
});

// Removing a market stops the upgrades of what was installed from it (#1895): the confirmation counts them.
describe("upgradingFrom", () => {
  it("counts the manager's own extensions that upgrade from the market", () => {
    const list = [
      { auto_upgrade: true, market: "m1" },
      { auto_upgrade: false, market: "m1" },
      { auto_upgrade: true, market: "m2" },
      {}, // built in, or not from a market
    ];
    expect(upgradingFrom("m1", list, "assistant")).toBe(2);
    expect(upgradingFrom("m3", list, "assistant")).toBe(0);
  });

  it("reads the hub's merged list, which names every item's manager, and leaves out another manager's copies", () => {
    const merged = [
      { manager: "assistant", auto_upgrade: true, market: "m1" },
      { manager: "example-manager", auto_upgrade: true, market: "m1" }, // the same market id under another manager
      { manager: "example-manager", auto_upgrade: true }, // another manager's old hub does not change this count
    ];
    expect(upgradingFrom("m1", merged, "assistant")).toBe(1);
    expect(upgradingFrom("m1", merged, "example-manager")).toBeNull();
  });

  it("does not guess when the hub does not name the market", () => {
    expect(upgradingFrom("m1", [{ auto_upgrade: true }], "assistant")).toBeNull();
    expect(upgradingFrom("m1", [{}], "assistant")).toBe(0);
  });
});
