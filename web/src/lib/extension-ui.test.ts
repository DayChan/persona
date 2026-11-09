import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import {
  actUI, applyBadge, barSpans, blocks, chartExtent, compactNumber, edgeLabels, extensionSettingsHref, fieldValues, inboxItemPlace, inboxSourceItem, isHttps, isUnsupported, labelWidth, linear, niceTicks, numberProblem, outcome,
  percent, placesOf, pointText, readView, refreshDelay, renderUI, seriesColors, submitValues, thinLabels, toneOf, uiContext, uiIcon, uiPlaces, UI_VERSION, type UIPlace, type UIView,
} from "./extension-ui";

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

const view: UIView = {
  title: "概况",
  state: "tab=1",
  sections: [
    {
      items: [
        { type: "metric", label: "余额", value: "¥ 1", tone: "positive" },
        { type: "metric", label: "已用", value: "¥ 2" },
        { type: "progress", label: "今日", ratio: 0.5 },
        { type: "button", title: "刷新", action: { id: "refresh" } },
        { type: "button", title: "停用", role: "destructive", action: { id: "off", confirm: "确定？" } },
        { type: "divider" },
        { type: "row", title: "Key" },
      ],
    },
    {
      items: [
        { type: "field", name: "note", label: "备注", kind: "text", value: "hi" },
        { type: "field", name: "token", label: "Token", kind: "secret", value: "never" },
        { type: "field", name: "auto", label: "提醒", kind: "toggle", value: "true" },
        { type: "field", name: "off", label: "关", kind: "toggle" },
        { type: "field", name: "plan", label: "套餐", kind: "choice", choices: [{ value: "solo" }, { value: "team" }] },
        { type: "field", name: "limit", label: "上限", kind: "number", value: "20" },
        { type: "text", text: "x" },
      ],
    },
  ],
};

describe("画一页", () => {
  it("maps tones, ratios and links", () => {
    expect(["positive", "caution", "danger", "neutral", "pink", undefined].map(toneOf)).toEqual(["positive", "caution", "danger", "neutral", "neutral", "neutral"]);
    expect([0.72, -1, 3, undefined, Number.NaN, 0.333].map(percent)).toEqual([72, 0, 100, 0, 0, 33.3]);
    expect(isHttps("https://example.com/a")).toBe(true);
    expect(isHttps("http://example.com")).toBe(false);
    expect(isHttps("javascript:alert(1)")).toBe(false);
    expect(isHttps(undefined)).toBe(false);
  });
  it("lays metrics side by side and buttons in a row", () => {
    expect(blocks(view.sections[0].items).map((b) => (b.kind === "item" ? b.item.type : `${b.kind}:${b.items.length}`))).toEqual([
      "metrics:2", "progress", "buttons:2", "divider", "row",
    ]);
    expect(blocks([])).toEqual([]);
  });
  it("starts the fields as the extension filled them in, never a secret", () => {
    expect(fieldValues(view)).toEqual({ note: "hi", token: "", auto: "true", off: "false", plan: "solo", limit: "20" });
    expect(fieldValues(null)).toEqual({});
  });
  it("submits every field, typed or not", () => {
    expect(submitValues(view, { note: "new", token: "s", extra: "no" })).toEqual({ note: "new", token: "s", auto: "true", off: "false", plan: "solo", limit: "20" });
    expect(numberProblem("12.5")).toBe("");
    expect(numberProblem("")).toBe("");
    expect(numberProblem("abc")).toBe("要是一个数");
  });
  it("reads what a press answered", () => {
    expect(outcome({})).toEqual({ view: undefined, reload: true, toast: undefined, url: undefined });
    expect(outcome({ toast: "已刷新" })).toMatchObject({ reload: false, toast: "已刷新" });
    expect(outcome({ view })).toMatchObject({ view, reload: false });
    expect(outcome({ open_url: "https://example.com/keys" })).toMatchObject({ reload: false, url: "https://example.com/keys" });
    expect(outcome({ open_url: "http://example.com/keys" })).toMatchObject({ reload: false, url: undefined });
  });
  it("hides a section the extension says is not its gateway (404 unsupported), and only that", () => {
    expect(isUnsupported(new ApiError(404, "not my gateway", { code: "unsupported" }))).toBe(true);
    expect(isUnsupported(new ApiError(404, "no such extension", { code: "not_found" }))).toBe(false);
    expect(isUnsupported(new ApiError(502, "x", { code: "unsupported" }))).toBe(false);
    expect(isUnsupported(new Error("unsupported"))).toBe(false);
  });
});

describe("声明式 UI 的接口", () => {
  it("lists places, draws and presses", async () => {
    const calls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push(`${init.method ?? "GET"} ${url} ${(init.body as string) ?? ""}`.trim());
      if (url.includes("unsupported")) return new Response(JSON.stringify({ error: "not my gateway", code: "unsupported" }), { status: 404 });
      return new Response(JSON.stringify(url.includes("placement") ? { ui: [{ extension: "p", id: "panel", title: "面板", placement: "launcher_section" }] } : { view, toast: "ok" }));
    });
    expect((await uiPlaces("launcher_section", "work cc", "mac 1")).map((u) => u.id)).toEqual(["panel"]);
    await uiPlaces("plugin_page");
    expect((await renderUI("a b", "home")).title).toBe("概况");
    await renderUI("p", "panel", { launcher: "work" }, "tab=1");
    expect((await actUI("p", "home", "save", { state: "tab=1", values: { note: "x" } })).toast).toBe("ok");
    await actUI("p", "home", "refresh");
    const err = await renderUI("p", "unsupported", { launcher: "other" }).catch((e) => e);
    expect(isUnsupported(err)).toBe(true);
    const ctx = (c?: object) => JSON.stringify(uiContext(c));
    expect(calls).toEqual([
      "GET /api/v1/extensions/-/ui?placement=launcher_section&launcher=work+cc&machine=mac+1",
      "GET /api/v1/extensions/-/ui?placement=plugin_page",
      `POST /api/v1/extensions/a%20b/-/ui/home {"context":${ctx()}}`,
      `POST /api/v1/extensions/p/-/ui/panel {"context":${ctx({ launcher: "work" })},"state":"tab=1"}`,
      `POST /api/v1/extensions/p/-/ui/home {"context":${ctx()},"state":"tab=1","action":"save","values":{"note":"x"}}`,
      `POST /api/v1/extensions/p/-/ui/home {"context":${ctx()},"action":"refresh"}`,
      `POST /api/v1/extensions/p/-/ui/unsupported {"context":${ctx({ launcher: "other" })}}`,
    ]);
  });
});

describe("UI 版本 2：读 View", () => {
  it("skips item types it does not know and draws the rest; a section they emptied goes, unless it has a footer", () => {
    const v = readView({
      title: "用量",
      state: "range=7d",
      refresh_after: 600,
      future: true,
      sections: [
        { items: [{ type: "hologram", text: "?" }, { type: "text", text: "hi" }] },
        { title: "只剩未知的", items: [{ type: "hologram" }] },
        { footer: "留着", items: [{ type: "hologram" }] },
        { title: "空的", items: [] },
        { items: [{ type: "chart", kind: "bar", series: [] }, { type: "chart", kind: "bar", series: [{ points: [{ label: "a", value: Number.NaN }] }] }, { type: "stat_grid" }, { type: "segmented", name: "r" }, "junk", null] },
        { items: [{ type: "chart", kind: "line", series: [{ points: [{ label: "a", value: 1 }] }] }, { type: "stat_grid", stats: [{ label: "今天", value: "1" }] }] },
      ],
    });
    expect(v.title).toBe("用量");
    expect(v.state).toBe("range=7d");
    expect(v.refresh_after).toBe(600);
    expect(v.sections.map((s) => s.items.map((i) => i.type))).toEqual([["text"], [], [], ["chart", "stat_grid"]]);
    expect(v.sections[1].footer).toBe("留着");
    expect(v.sections[2].title).toBe("空的");
  });
  it("only a view that does not read at all is an error", () => {
    expect(() => readView(null)).toThrow();
    expect(() => readView({ sections: "x" })).toThrow();
    expect(readView({ sections: [] }).sections).toEqual([]);
  });
  it("draws again after refresh_after, held to 30 s … a day", () => {
    expect([undefined, 0, -5, 10, 30, 600, 999999, Number.NaN].map((r) => refreshDelay({ sections: [], refresh_after: r }))).toEqual([0, 0, 0, 30000, 30000, 600000, 86400000, 0]);
    expect(refreshDelay(null)).toBe(0);
  });
  it("a segmented starts as chosen and goes along with a submit", () => {
    const v: UIView = { sections: [{ items: [{ type: "segmented", name: "range", value: "7d", choices: [{ value: "1d" }, { value: "7d" }], action: { id: "range" } }, { type: "field", name: "note", label: "备注", kind: "text" }] }] };
    expect(fieldValues(v)).toEqual({ note: "", range: "7d" });
    expect(submitValues(v, { range: "1d" })).toEqual({ note: "", range: "1d" });
  });
});

describe("UI 版本 2：上下文", () => {
  it("says where it is drawn, for whom: time zone, language, platform, version", () => {
    vi.stubGlobal("navigator", { language: "zh-CN" });
    const c = uiContext({ machine: "m1", size: "compact" });
    expect(c).toMatchObject({ platform: "web", ui_version: UI_VERSION, locale: "zh-CN", machine: "m1", size: "compact" });
    expect(UI_VERSION).toBe(6);
    expect(c.tz).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
    expect(uiContext()).not.toHaveProperty("launcher");
    expect(uiContext({ launcher: "work" }).launcher).toBe("work");
  });
  it("lists every slot without a placement", async () => {
    const urls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string) => (urls.push(url), new Response(JSON.stringify({ ui: "nope" }))));
    expect(await uiPlaces("")).toEqual([]);
    expect(urls).toEqual(["/api/v1/extensions/-/ui"]);
  });
});

describe("UI 插槽", () => {
  const list: UIPlace[] = [
    { extension: "g", id: "usage", title: "用量", placement: "nav_page", icon: "chart", badge: { text: "3", tone: "danger" } },
    { extension: "g", id: "today", title: "今天", placement: "overview_card", opens: "usage" },
    { extension: "g", id: "gpu", title: "显卡", placement: "machine_section" },
    { extension: "g", id: "x", title: "以后的", placement: "session_action" },
  ];
  it("sorts places by slot and ignores ones it does not know", () => {
    expect(placesOf(list, "nav_page").map((u) => u.id)).toEqual(["usage"]);
    expect(placesOf(list, "overview_card").map((u) => u.id)).toEqual(["today"]);
    expect(placesOf(list, "machine_section").map((u) => u.id)).toEqual(["gpu"]);
  });
  it("draws an icon it does not know as the extension's", () => {
    expect(["chart", "money", "doc", "rocket", undefined, ""].map(uiIcon)).toEqual(["chart", "money", "doc", "extension", "extension", "extension"]);
  });
  it("changes a badge in place from the plugin_ui event; null clears it", () => {
    const a = applyBadge(list, { extension: "g", id: "usage", badge: { text: "5", tone: "caution" } });
    expect(a[0].badge).toEqual({ text: "5", tone: "caution" });
    expect(a[1]).toBe(list[1]);
    expect(applyBadge(a, { extension: "g", id: "usage", badge: null })[0].badge).toBeNull();
    expect(applyBadge(list, { extension: "g", id: "usage", badge: { text: "3", tone: "danger" } })).toBe(list);
    expect(applyBadge(list, { extension: "other", id: "usage", badge: { text: "1" } })).toBe(list);
  });
});

describe("收件箱条目（inbox_item）", () => {
  const list: UIPlace[] = [
    { extension: "g", id: "usage", title: "用量", placement: "nav_page" },
    { extension: "chat", id: "event", title: "事件", placement: "inbox_item", size: "full", connector: "examplechat" },
    { extension: "odd", id: "nothing", title: "没写连接器", placement: "inbox_item" },
  ];
  it("finds the page of the connector that brought the item in; none: the item's own detail", () => {
    expect(placesOf(list, "inbox_item").map((u) => u.id)).toEqual(["event", "nothing"]);
    expect(inboxItemPlace(list, "examplechat")?.id).toBe("event");
    expect(inboxItemPlace(list, "mail")).toBeUndefined();
    expect(inboxItemPlace(list, undefined)).toBeUndefined();
    expect(inboxItemPlace(list, "")).toBeUndefined();
    expect(inboxItemPlace([], "examplechat")).toBeUndefined();
  });
  it("opens the item's own source page only for the item it is drawn for", () => {
    expect(inboxSourceItem("persona://inbox/lark:F1:1051/source")).toBe("lark:F1:1051");
    expect(inboxSourceItem("persona://inbox/lark%3AF1%3A1051/source")).toBe("lark:F1:1051");
    expect(inboxSourceItem("persona://inbox/x%2Fy%20z/source")).toBe("x/y z");
    for (const bad of ["persona://inbox//source", "persona://inbox/a/b/source", "persona://inbox/a/source?x=1", "persona://settings", "https://example.com/inbox/a/source", "persona://inbox/%E0/source"]) {
      expect(inboxSourceItem(bad)).toBeUndefined();
    }
    const src = { open_url: "persona://inbox/lark:F1:1051/source" };
    expect(outcome(src, "lark:F1:1051")).toEqual({ view: undefined, reload: false, toast: undefined, url: undefined, source: true });
    expect(outcome(src, "lark:F1:9999")).not.toHaveProperty("source");
    expect(outcome(src)).not.toHaveProperty("source"); // not drawn for an inbox item: nothing opens
    expect(outcome({ open_url: "https://example.com/c" }, "lark:F1:1051")).toEqual({ view: undefined, reload: false, toast: undefined, url: "https://example.com/c" });
  });
  it("opens an extension's own settings in the app (persona://settings/extensions/<id>)", () => {
    expect(extensionSettingsHref("persona://settings/extensions/example-gateway")).toBe("/settings?tab=extensions&extension=example-gateway");
    expect(extensionSettingsHref("persona://settings/extensions/a%20b")).toBe("/settings?tab=extensions&extension=a%20b");
    for (const bad of [
      "persona://settings/extensions/", "persona://settings/extensions/a/b", "persona://settings/extensions/a?x=1", "persona://settings/extensions/-market",
      "persona://settings/extensions/a%2Fb", "persona://settings/extensions/%E0", "persona://settings", "persona://inbox/a/source", "https://example.com/settings/extensions/a",
      "previous://settings/extensions/a",
    ]) {
      expect(extensionSettingsHref(bad)).toBeUndefined();
    }
    expect(outcome({ open_url: "persona://settings/extensions/example-gateway" })).toEqual({ view: undefined, reload: false, toast: undefined, url: undefined, settings: "/settings?tab=extensions&extension=example-gateway" });
    // any other persona:// link stays as it was: nothing opens, nothing is drawn again
    expect(outcome({ open_url: "persona://settings/other" })).toEqual({ view: undefined, reload: false, toast: undefined, url: undefined });
  });
  it("tells the hub which item, with every draw and press", async () => {
    const calls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push(`${init.method ?? "GET"} ${url} ${(init.body as string) ?? ""}`.trim());
      if (url.includes("placement")) return new Response(JSON.stringify({ ui: [list[1]] }));
      if (String(init.body).includes(`"item":"mail:1"`)) return new Response(JSON.stringify({ error: "这一条不是这个扩展的连接器收进来的", code: "forbidden" }), { status: 403 });
      return new Response(JSON.stringify({ view, toast: "已处理" }));
    });
    expect(await uiPlaces("inbox_item")).toEqual([list[1]]);
    expect(uiContext({ item: "examplechat:1" }).item).toBe("examplechat:1");
    expect(uiContext()).not.toHaveProperty("item");
    expect((await renderUI("chat", "event", { item: "examplechat:1" })).title).toBe("概况");
    expect((await actUI("chat", "event", "done", { context: { item: "examplechat:1" } })).toast).toBe("已处理");
    const err = await renderUI("chat", "event", { item: "mail:1" }).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect([err.status, err.code, isUnsupported(err)]).toEqual([403, "forbidden", false]);
    const ctx = JSON.stringify(uiContext({ item: "examplechat:1" }));
    expect(calls.slice(0, 3)).toEqual([
      "GET /api/v1/extensions/-/ui?placement=inbox_item",
      `POST /api/v1/extensions/chat/-/ui/event {"context":${ctx}}`,
      `POST /api/v1/extensions/chat/-/ui/event {"context":${ctx},"action":"done"}`,
    ]);
  });
});

describe("图", () => {
  it("writes numbers short", () => {
    expect([0, 950, 999, 1000, 1200, 17000, 999950, 1_200_000, 3.4e9, -2500, 0.125, 12.34, 156.7].map(compactNumber)).toEqual([
      "0", "950", "999", "1K", "1.2K", "17K", "1M", "1.2M", "3.4B", "-2.5K", "0.13", "12.3", "157",
    ]);
    expect(pointText({ label: "a", value: 1200000 }, "tokens")).toBe("1.2M tokens");
    expect(pointText({ label: "a", value: 1.2, display: "¥1.20" }, "元")).toBe("¥1.20");
    expect(pointText({ label: "a", value: 3 })).toBe("3");
  });
  it("scales and finds round ticks with zero in", () => {
    expect(linear(0, 10, 100, 0)(5)).toBe(50);
    expect(linear(3, 3, 0, 10)(3)).toBe(5);
    expect(niceTicks(0, 3.1, 4)).toEqual({ lo: 0, hi: 4, ticks: [0, 1, 2, 3, 4] });
    expect(niceTicks(20, 260, 4)).toEqual({ lo: 0, hi: 300, ticks: [0, 100, 200, 300] });
    expect(niceTicks(-3, 7, 4).ticks).toEqual([-5, -2.5, 0, 2.5, 5, 7.5]);
    expect(niceTicks(0, 0)).toMatchObject({ lo: 0, hi: 1 });
    expect(niceTicks(0, 0.3, 4).ticks).toEqual([0, 0.1, 0.2, 0.3]);
  });
  it("stacks bars: positives on positives, negatives under, the outer end rounded", () => {
    const series = [
      { name: "a", points: [{ label: "x", value: 2 }, { label: "y", value: -1 }] },
      { name: "b", points: [{ label: "x", value: 3 }, { label: "y", value: 4 }] },
      { name: "c", points: [{ label: "x", value: 0 }, { label: "y", value: -2 }] },
    ];
    const spans = barSpans(series, true);
    expect(spans[0].map((b) => [b.from, b.to, b.end])).toEqual([[0, 2, false], [2, 5, true], [5, 5, false]]);
    expect(spans[1].map((b) => [b.from, b.to, b.end])).toEqual([[0, -1, false], [0, 4, true], [-1, -3, true]]);
    expect(chartExtent(series, "bar", true)).toEqual([-3, 5]);
    expect(chartExtent(series, "bar", false)).toEqual([-2, 4]);
    expect(barSpans(series, false)[0].every((b) => b.from === 0 && b.end)).toBe(true);
  });
  it("thins labels so none overlap, and keeps the last", () => {
    const labels = Array.from({ length: 30 }, (_, i) => `11-${String(i + 1).padStart(2, "0")}`);
    for (const width of [120, 240, 400, 900]) {
      const xs = labels.map((_, i) => (width / labels.length) * (i + 0.5));
      const shown = thinLabels(labels, xs, width);
      expect(shown[0]).toBe(0);
      expect(shown[shown.length - 1]).toBe(29);
      const spans = shown.map((i) => {
        const w = labelWidth(labels[i], 10.5);
        const a = Math.min(Math.max(0, xs[i] - w / 2), width - w);
        return [a, a + w];
      });
      spans.forEach((s, j) => j && expect(spans[j - 1][1] + 8).toBeLessThanOrEqual(s[0]));
      if (width === 900) expect(shown.length).toBe(15);
    }
    expect(thinLabels(["a", "b", "c"], [10, 50, 90], 100)).toEqual([0, 1, 2]);
    expect(thinLabels([], [], 100)).toEqual([]);
    expect(labelWidth("今天", 10)).toBe(20);
  });
  it("a compact chart writes only the first and last label", () => {
    expect(edgeLabels(["10-29", "10-30", "11-04"], 200)).toEqual([0, 2]);
    expect(edgeLabels(["很长很长很长的标签", "也很长很长很长很长"], 120)).toEqual([1]);
    expect(edgeLabels(["a"], 100)).toEqual([0]);
  });
  it("colors series by tone, the rest in the series palette's order", () => {
    expect(seriesColors([{ points: [] }, { tone: "positive", points: [] }, { points: [] }, { tone: "pink" as never, points: [] }, { points: [] }, { points: [] }, { points: [] }])).toEqual([
      "var(--series-1)", "var(--green)", "var(--series-2)", "var(--ink-3)", "var(--series-3)", "var(--series-4)", "var(--series-1)",
    ]);
  });
});
