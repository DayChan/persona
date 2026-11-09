import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { acceptHubs, addHub, hubTitle, loadHubs, loadRemoved, MAX_HUBS, MAX_REMOVED, mergeHubs, normalizeHubURL, rememberHub, removeHub, renameHub, switchURL, takeHubs, type SavedHub } from "./hubs";

describe("normalizeHubURL", () => {
  it("keeps only the origin, the way location.origin writes it", () => {
    expect(normalizeHubURL("https://hub.example.com")).toEqual({ url: "https://hub.example.com" });
    expect(normalizeHubURL(" https://hub.example.com:443/ ")).toEqual({ url: "https://hub.example.com" });
    expect(normalizeHubURL("http://192.0.2.10:18660")).toEqual({ url: "http://192.0.2.10:18660" });
    expect(normalizeHubURL("HTTPS://Hub.Example.COM:8443")).toEqual({ url: "https://hub.example.com:8443" });
    expect(normalizeHubURL("http://[2001:db8::10]:18660")).toEqual({ url: "http://[2001:db8::10]:18660" });
    // the sub-path a reverse proxy serves the hub under (docs/web/web.md "Sub-path")
    expect(normalizeHubURL("https://hub.example.com/persona-hub/")).toEqual({ url: "https://hub.example.com/persona-hub" });
    expect(normalizeHubURL("https://hub.example.com/persona")).toEqual({ url: "https://hub.example.com/persona" });
    expect(normalizeHubURL("https://hub.example.com/x/../hub")).toEqual({ url: "https://hub.example.com/hub" });
  });
  it("reads no scheme as https", () => {
    expect(normalizeHubURL("hub.example.com")).toEqual({ url: "https://hub.example.com" });
    expect(normalizeHubURL("hub.example.com:8443")).toEqual({ url: "https://hub.example.com:8443" });
  });
  it("says what is wrong with anything that is not just an address", () => {
    expect(normalizeHubURL("  ")).toEqual({ error: "填 hub 的地址" });
    expect(normalizeHubURL("ftp://hub.example.com")).toEqual({ error: "只能是 http:// 或 https:// 的地址" });
    expect(normalizeHubURL("https://me:pw@hub.example.com")).toEqual({ error: "地址里不要带用户名和密码" });
    const noPath = "只填 hub 的地址（可以带反向代理的子路径），不要带 ? 或 # 之后的部分";
    expect(normalizeHubURL("https://hub.example.com/settings")).toEqual({ error: noPath });
    expect(normalizeHubURL("https://hub.example.com/persona/s/laptop/abc")).toEqual({ error: noPath });
    expect(normalizeHubURL("https://hub.example.com/?token=x")).toEqual({ error: noPath });
    expect(normalizeHubURL("https://hub.example.com/#token=x")).toEqual({ error: noPath });
    expect(normalizeHubURL("https://hub.example.com/a%20b")).toEqual({ error: noPath });
    expect(normalizeHubURL("https://")).toEqual({ error: "不是一个地址" });
  });
});

describe("hubTitle", () => {
  it("prefers the user's name, then the hub's, then the host", () => {
    expect(hubTitle({ url: "https://hub.example.com", name: "家里", label: "我的" })).toBe("我的");
    expect(hubTitle({ url: "https://hub.example.com", name: "家里" })).toBe("家里");
    expect(hubTitle({ url: "https://hub.example.com:8443" })).toBe("hub.example.com:8443");
  });
});

describe("handing the list over", () => {
  const list: SavedHub[] = [
    { url: "https://a.example.com", name: "A" },
    { url: "http://192.0.2.10:18660", label: "B 的" },
  ];
  it("round-trips through the fragment, and the link loses it", () => {
    expect(switchURL("https://b.example.com/x", list, []).startsWith("https://b.example.com/x/#hubs=")).toBe(true);
    const href = switchURL("https://b.example.com", list, []);
    expect(href.startsWith("https://b.example.com/#hubs=")).toBe(true);
    expect(href).not.toContain("token");
    expect(takeHubs(href)).toEqual({ hubs: list, removed: [], rest: "/" });
  });
  it("carries removal markers in the same list, where a page from before them sees no hub", () => {
    const href = switchURL("https://b.example.com", list, [{ url: "https://c.example.com", at: 5 }]);
    const carried = JSON.parse(new URLSearchParams(new URL(href).hash.slice(1)).get("hubs") ?? "");
    expect(carried).toEqual([...list, { removed: "https://c.example.com", at: 5 }]);
    expect(carried.filter((e: { url?: unknown }) => typeof e.url === "string")).toEqual(list); // what an older page keeps
    expect(takeHubs(href)).toEqual({ hubs: list, removed: [{ url: "https://c.example.com", at: 5 }], rest: "/" });
  });
  it("keeps the rest of the link, a token next to it included (takeToken takes that one)", () => {
    const hash = new URLSearchParams({ hubs: JSON.stringify(list), token: "t0k" }).toString();
    expect(takeHubs(`https://b.example.com/s/m1/abc?tab=x#${hash}`)).toEqual({ hubs: list, removed: [], rest: "/s/m1/abc?tab=x#token=t0k" });
  });
  it("leaves links without one alone", () => {
    expect(takeHubs("https://b.example.com/settings?tab=general")).toBeNull();
    expect(takeHubs("https://b.example.com/#token=t0k")).toBeNull();
  });
  it("drops what does not parse or is not an address, but still cleans the link", () => {
    expect(takeHubs("https://b.example.com/#hubs=%7Bnope")).toEqual({ hubs: [], removed: [], rest: "/" });
    const odd = JSON.stringify([
      { url: "javascript:alert(1)" },
      { url: "https://c.example.com/settings" },
      { name: "no url" },
      "x",
      { url: "https://a.example.com", name: 7, added: "soon" },
      { removed: "javascript:alert(1)", at: 5 },
      { removed: "https://d.example.com" },
      { removed: "https://e.example.com", at: -1 },
    ]);
    expect(takeHubs(`https://b.example.com/#hubs=${encodeURIComponent(odd)}`)).toEqual({ hubs: [{ url: "https://a.example.com" }], removed: [], rest: "/" });
  });
  it("carries at most MAX_HUBS hubs and MAX_REMOVED markers, the newest ones", () => {
    const many = Array.from({ length: MAX_HUBS + 5 }, (_, i) => ({ url: `https://h${i}.example.com` }));
    const gone = Array.from({ length: MAX_REMOVED + 5 }, (_, i) => ({ url: `https://g${i}.example.com`, at: i + 1 }));
    const taken = takeHubs(switchURL("https://b.example.com", many, gone));
    expect(taken?.hubs).toHaveLength(MAX_HUBS);
    expect(taken?.removed).toHaveLength(MAX_REMOVED);
    expect(taken?.removed[0]).toEqual({ url: `https://g${MAX_REMOVED + 4}.example.com`, at: MAX_REMOVED + 5 });
  });
});

describe("mergeHubs", () => {
  it("keeps this browser's order and labels, fills in names, appends new hubs", () => {
    const local: SavedHub[] = [{ url: "https://a.example.com", label: "我的" }, { url: "https://b.example.com" }];
    const incoming: SavedHub[] = [{ url: "https://c.example.com", name: "C" }, { url: "https://a.example.com", name: "A", label: "别处的" }, { url: "https://b.example.com", name: "B" }];
    expect(mergeHubs(local, incoming)).toEqual([
      { url: "https://a.example.com", name: "A", label: "我的" },
      { url: "https://b.example.com", name: "B" },
      { url: "https://c.example.com", name: "C" },
    ]);
  });
});

describe("the stored list", () => {
  let store: Map<string, string>;
  const NOW = 1_700_000_000_000;
  beforeEach(() => {
    store = new Map();
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    });
    vi.useFakeTimers({ toFake: ["Date"], now: NOW });
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("remembers the hub this page is on and refreshes its name", () => {
    rememberHub("https://a.example.com", "");
    expect(loadHubs()).toEqual([{ url: "https://a.example.com", added: NOW }]);
    rememberHub("https://a.example.com:443", "家里");
    expect(loadHubs()).toEqual([{ url: "https://a.example.com", name: "家里", added: NOW }]);
    rememberHub("https://a.example.com", ""); // a page that has not heard the name yet keeps it
    expect(loadHubs()).toEqual([{ url: "https://a.example.com", name: "家里", added: NOW }]);
  });
  it("adds, renames and removes; refuses duplicates and bad addresses", () => {
    expect(addHub("b.example.com", "工作")).toEqual({ list: [{ url: "https://b.example.com", label: "工作", added: NOW }] });
    expect(addHub("https://b.example.com:443/")).toEqual({ error: "这台 hub 已经在列表里" });
    expect(addHub("https://b.example.com/settings")).toEqual({ error: "只填 hub 的地址（可以带反向代理的子路径），不要带 ? 或 # 之后的部分" });
    // the same origin under a reverse proxy's sub-path is another hub
    expect(addHub("https://b.example.com/x")).toEqual({ list: [{ url: "https://b.example.com", label: "工作", added: NOW }, { url: "https://b.example.com/x", added: NOW }] });
    removeHub("https://b.example.com/x");
    expect(loadRemoved()).toEqual([{ url: "https://b.example.com/x", at: NOW }]);
    rememberHub("https://b.example.com", "B");
    expect(renameHub("https://b.example.com", "")).toEqual([{ url: "https://b.example.com", name: "B", added: NOW }]);
    expect(renameHub("https://b.example.com", " 公司 ")).toEqual([{ url: "https://b.example.com", name: "B", label: "公司", added: NOW }]);
    expect(removeHub("https://b.example.com")).toEqual([]);
    // adding it again drops its marker
    vi.setSystemTime(NOW + 1);
    addHub("https://b.example.com/x");
    expect(loadRemoved()).toEqual([{ url: "https://b.example.com", at: NOW }]);
  });
  it("stops adding at MAX_HUBS", () => {
    for (let i = 0; i < MAX_HUBS; i++) expect("list" in addHub(`https://h${i}.example.com`)).toBe(true);
    expect(addHub("https://one-more.example.com")).toEqual({ error: `最多记 ${MAX_HUBS} 台 hub` });
  });
  it("merges a handed-over list into what it has", () => {
    rememberHub("https://b.example.com", "B");
    acceptHubs([{ url: "https://a.example.com", name: "A" }, { url: "https://b.example.com", label: "别处" }]);
    expect(loadHubs()).toEqual([{ url: "https://b.example.com", name: "B", label: "别处", added: NOW }, { url: "https://a.example.com", name: "A" }]);
  });
  it("drops what a handed-over marker removed after it went in, but not a hub added since", () => {
    store.set("persona.hubs", JSON.stringify([{ url: "https://a.example.com", added: NOW }, { url: "https://c.example.com", added: NOW }, { url: "https://old.example.com" }]));
    acceptHubs([], [{ url: "https://c.example.com", at: NOW + 5 }, { url: "https://old.example.com", at: 1 }, { url: "https://a.example.com", at: NOW - 5 }]);
    // a list from before the markers has no times: any marker is newer
    expect(loadHubs()).toEqual([{ url: "https://a.example.com", added: NOW }]);
    // the markers that removed something are kept to pass on; the one an add overtook goes
    expect(loadRemoved()).toEqual([{ url: "https://c.example.com", at: NOW + 5 }, { url: "https://old.example.com", at: 1 }]);
  });
  it("keeps the newest MAX_REMOVED markers", () => {
    for (let i = 0; i < MAX_REMOVED + 3; i++) {
      vi.setSystemTime(NOW + i);
      removeHub(`https://g${i}.example.com`);
    }
    const kept = loadRemoved();
    expect(kept).toHaveLength(MAX_REMOVED);
    expect(kept[0]).toEqual({ url: `https://g${MAX_REMOVED + 2}.example.com`, at: NOW + MAX_REMOVED + 2 });
  });
  it("reads a broken stored value as an empty list", () => {
    store.set("persona.hubs", "{not json");
    expect(loadHubs()).toEqual([]);
  });
});

// Each hub's page is its own origin with its own localStorage; a switch hands the list over in the address (#1917).
describe("switching between two hubs' pages", () => {
  const A = "https://a.example.com";
  const B = "https://b.example.com";
  const C = "https://c.example.com";
  const stores = new Map<string, Map<string, string>>([[A, new Map()], [B, new Map()]]);
  let origin = A;
  const urls = () => loadHubs().map((h) => h.url);
  // open `to`'s page from the current one, as goToHub and App.tsx do
  const switchTo = (to: string) => {
    const href = switchURL(to, loadHubs());
    origin = to;
    const carried = takeHubs(href)!;
    acceptHubs(carried.hubs, carried.removed);
    rememberHub(to);
  };
  let now = 1_700_000_000_000;
  const tick = () => vi.setSystemTime((now += 1000));
  beforeEach(() => {
    stores.forEach((s) => s.clear());
    origin = A;
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => stores.get(origin)!.get(k) ?? null,
      setItem: (k: string, v: string) => void stores.get(origin)!.set(k, v),
      removeItem: (k: string) => void stores.get(origin)!.delete(k),
    });
    vi.useFakeTimers({ toFake: ["Date"], now });
    rememberHub(A);
    tick();
    addHub(B);
    tick();
    addHub(C);
    tick();
    switchTo(B); // B's page now has A, B and C
    tick();
    switchTo(A);
    tick();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("a hub removed on one page stays removed after going to the other page and back", () => {
    expect(urls()).toEqual([A, B, C]);
    removeHub(C);
    tick();
    switchTo(B);
    expect(urls()).toEqual([A, B]);
    tick();
    switchTo(A);
    expect(urls()).toEqual([A, B]);
    tick();
    switchTo(B);
    expect(urls()).toEqual([A, B]);
  });
  it("a hub added again after its removal comes back on the other page too", () => {
    removeHub(C);
    tick();
    switchTo(B);
    tick();
    addHub(C); // added again, on B's page this time
    tick();
    switchTo(A);
    expect(urls()).toEqual([A, B, C]);
    expect(loadRemoved()).toEqual([]);
    tick();
    switchTo(B);
    expect(urls()).toEqual([A, B, C]);
  });
});
