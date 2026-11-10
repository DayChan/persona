import { describe, expect, it } from "vitest";
import { addSection, agentPath, arrangeSessions, assignSection, blockView, emptyPrefs, isDefaultView, mergeMessages, normalizePrefs, pendingOutgoing, removeSection, resetView, setView, setupHints, toggleProvider, togglePin, totalUnread, viewOf, type AgentMessage, type AgentSession, type Outgoing } from "./agents";

const s = (id: string, provider: string, rank: number, unread = 0, title = id): AgentSession => ({ id, source: "lark-bridge", sessionKey: id, provider, title, canSend: true, unreadCount: unread, rankTimeMs: rank });
const m = (id: string, pos: number, role = "assistant", text = id, t = pos * 1000): AgentMessage => ({ externalId: id, role, blocks: [{ type: "text", text }], createTimeMs: t, position: pos });

describe("agentPath", () => {
  it("encodes the whole id, '/' and ':' included", () => {
    expect(agentPath("lark-bridge:agent:grok_bot:a1:u-1/")).toBe("/agents/sessions/lark-bridge%3Aagent%3Agrok_bot%3Aa1%3Au-1%2F");
  });
});

describe("mergeMessages", () => {
  it("dedupes by externalId and sorts out-of-order positions", () => {
    const older = [m("x100", 100), m("x102", 102), m("x101", 101)];
    const cur = [m("x102", 102, "assistant", "edited"), m("x103", 103)];
    const r = mergeMessages(older, cur);
    expect(r.map((x) => x.externalId)).toEqual(["x100", "x101", "x102", "x103"]);
    expect(r[2].blocks[0].text).toBe("edited");
  });
});

describe("blockView", () => {
  it("knows text and tool calls and shows anything else raw", () => {
    expect(blockView({ type: "text", text: "hi" })).toEqual({ kind: "text", text: "hi" });
    expect(blockView({ type: "tool-call", name: "Bash" })).toEqual({ kind: "tool", name: "Bash", detail: "" });
    expect(blockView({ type: "tool-call", name: "Bash", input: { cmd: "ls" } }).kind).toBe("tool");
    expect(blockView({ type: "reaction", from: "assistant", emoji: "👍" })).toEqual({ kind: "reaction", emoji: "👍" });
    expect(blockView({ type: "reaction" }).kind).toBe("raw");
    const raw = blockView({ type: "image", url: "u" });
    expect(raw.kind === "raw" && JSON.parse(raw.json)).toEqual({ type: "image", url: "u" });
  });
});

describe("pendingOutgoing", () => {
  const o = (clientId: string, text: string, status: Outgoing["status"], at = 10_000): Outgoing => ({ clientId, text, at, status });
  it("drops a sent bubble once the persisted user message shows up, keeps failed / unmatched ones", () => {
    const out = [o("1", "hello", "sent"), o("2", "hello", "sent"), o("3", "x", "failed"), o("4", "later", "sent")];
    const msgs = [m("t1", 1, "user", "hello", 11_000), m("t2", 2, "assistant", "later", 12_000)];
    expect(pendingOutgoing(out, msgs).map((x) => x.clientId)).toEqual(["2", "3", "4"]);
  });
  it("ignores an identical message from long before", () => {
    expect(pendingOutgoing([o("1", "ok", "sent", 1_000_000)], [m("t", 1, "user", "ok", 1000)])).toHaveLength(1);
  });
});

describe("arrangeSessions", () => {
  const now = new Date(2026, 9, 31, 12).getTime();
  const H = 3_600_000;
  const list = [s("a", "muse", now - H, 1, "Alpha"), s("b", "grok_bot", now - 2 * H, 3, "Beta"), s("c", "grok_bot", now - 30 * H, 0, "Charlie"), { ...s("d", "muse", now - 90 * 24 * H, 0, "Delta"), canSend: false }];
  const ids = (g: { sessions: AgentSession[] }[]) => g.map((x) => x.sessions.map((y) => y.id));
  it("defaults to date buckets, recent first", () => {
    const g = arrangeSessions(list, emptyPrefs(), "", now);
    expect(g.map((x) => x.label)).toEqual(["今天", "昨天", "更早"]);
    expect(ids(g)).toEqual([["a", "b"], ["c"], ["d"]]);
  });
  it("puts pins first in pin order whatever the grouping", () => {
    const p = { ...emptyPrefs(), pins: ["c", "a", "gone"], view: { group: "provider" as const } };
    const g = arrangeSessions(list, p, "", now);
    expect(g[0]).toMatchObject({ key: "pinned", label: "已置顶" });
    expect(ids(g)).toEqual([["c", "a"], ["b"], ["d"]]);
  });
  it("sorts by unread or name and can skip grouping", () => {
    expect(ids(arrangeSessions(list, { ...emptyPrefs(), view: { group: "none", sort: "unread" } }, "", now))).toEqual([["b", "a", "c", "d"]]);
    expect(ids(arrangeSessions(list, { ...emptyPrefs(), view: { group: "none", sort: "name" } }, "", now))).toEqual([["a", "b", "c", "d"]]);
    expect(totalUnread(list)).toBe(4);
  });
  it("filters by show mode, providers and query", () => {
    const flat = (v: object, q = "") => ids(arrangeSessions(list, { ...emptyPrefs(), view: { group: "none", ...v } }, q, now)).flat();
    expect(flat({ show: "unread" })).toEqual(["a", "b"]);
    expect(flat({ show: "sendable" })).toEqual(["a", "b", "c"]);
    expect(flat({ providers: ["muse"] })).toEqual(["a", "d"]);
    expect(flat({}, "char")).toEqual(["c"]);
    expect(flat({}, "MUSE")).toEqual(["a", "d"]); // the provider, whatever the case
    expect(flat({}, "grok bot")).toEqual(["b", "c"]); // its label
  });
  it("custom sections come after pins; removing one frees its sessions", () => {
    let p = addSection(emptyPrefs(), "工作", "w");
    p = assignSection(assignSection(p, "c", "w"), "d", "w");
    p = togglePin(p, "d");
    const g = arrangeSessions(list, p, "", now);
    expect(g.map((x) => x.key).slice(0, 2)).toEqual(["pinned", "sec:w"]);
    expect(ids(g).slice(0, 2)).toEqual([["d"], ["c"]]);
    p = removeSection(p, "w");
    expect(p.assign).toEqual({});
  });
  it("reset keeps pins and sections, and view helpers merge", () => {
    let p = setView(togglePin(emptyPrefs(), "a"), { sort: "name" });
    expect(isDefaultView(p)).toBe(false);
    p = toggleProvider(p, "muse");
    expect(viewOf(p).providers).toEqual(["muse"]);
    p = resetView(p);
    expect(isDefaultView(p)).toBe(true);
    expect(p.pins).toEqual(["a"]);
    expect(togglePin(p, "a").pins).toEqual([]);
  });
  it("normalizes whatever the hub or storage returns", () => {
    expect(normalizePrefs(null)).toEqual(emptyPrefs());
    expect(normalizePrefs({ pins: ["x"] } as never).sections).toEqual([]);
  });
});

describe("setupHints", () => {
  it("gives a failing source's own reason, and prefers a setup hint", () => {
    expect(setupHints([{ source: "a", enabled: true, error: "Not configured yet" }])).toEqual(["Not configured yet"]);
    expect(setupHints([{ source: "b", enabled: true, setup: "no_accounts", error: "x" }])).not.toContain("x");
  });

  it("says what to do for each source that is not set up, nothing for a ready one", () => {
    const h = setupHints([
      { source: "a", enabled: false, setup: "disabled" },
      { source: "b", enabled: true, setup: "no_accounts" },
      { source: "c", enabled: true },
      { source: "d", enabled: true, setup: "later" },
    ]);
    expect(h).toHaveLength(2);
    expect(h[0]).toContain("扩展");
    expect(h[1]).toContain("账号");
  });
});
