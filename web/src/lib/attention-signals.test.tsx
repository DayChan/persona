// @vitest-environment jsdom
// The system notifications of useAttentionSignals (#2971): "Needs you" at once, "Done" after the settle time and only for a
// long turn (Settings › Notifications "Long tasks only"), and only while the page is not in front.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentState, Interaction, Session } from "./types";

let sessions: Session[] = [];
let mutes: { spaces: string[]; sessions: string[]; allowed: string[] } = { spaces: [], sessions: [], allowed: [] };
let interactions: Interaction[] = [];
vi.mock("./store", () => ({
  useStore: (select: (s: { loaded: boolean; interactions: Interaction[] }) => unknown) => select({ loaded: true, interactions }),
  getState: () => ({ mutes, interactions }),
}));
vi.mock("./sessions", () => ({ useCounts: () => ({ badge: 0 }), useAgentSessions: () => sessions }));
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  api: async () => ({ input: true, done: "long", long_turn_seconds: 60 }),
  enc: encodeURIComponent,
}));

const { useAttentionSignals } = await import("./attention-signals");
const { BATCH_MAX_MS, BATCH_QUIET_MS, SETTLE_MS } = await import("./attention");
const { _setTabId, markFront } = await import("./notify");

const T0 = Date.parse("2026-01-01T10:00:00Z");
const session = (id: string, state: AgentState, since?: string): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/p", status: state, state, state_since: since, last_text: `${id} 的最后一句`,
});

const posted: { title: string; body?: string; tag?: string }[] = [];
const closed: string[] = [];
class FakeNotification {
  tag?: string;
  static permission = "granted";
  static requestPermission = async () => "granted";
  onclick: (() => void) | null = null;
  title: string;
  constructor(title: string, opts?: { body?: string; tag?: string }) {
    this.title = title;
    this.tag = opts?.tag;
    posted.push({ title, body: opts?.body, tag: opts?.tag });
  }
  close() {
    closed.push(this.title);
  }
}

function Probe() {
  useAttentionSignals("Persona", undefined);
  return null;
}

let root: Root;
let focused = false;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  vi.setSystemTime(T0);
  vi.stubGlobal("Notification", FakeNotification);
  vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
  localStorage.setItem("persona.systemNotify", "1");
  posted.length = 0;
  closed.length = 0;
  interactions = [];
  focused = false;
  mutes = { spaces: [], sessions: [], allowed: [] };
  _setTabId("tab-me");
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root.unmount());
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

async function show(next: Session[], cards: Interaction[] = interactions) {
  sessions = next;
  interactions = cards;
  await act(async () => root.render(<Probe key="p" />));
}

describe("系统通知", () => {
  it("需要你马上发；做完的长任务等一会儿再发，短的不发", async () => {
    await show([session("long", "working", "2026-01-01T09:58:00Z"), session("short", "working", "2026-01-01T09:59:50Z"), session("ask", "working")]);
    await act(async () => {}); // the prefs load
    await show([session("long", "unread"), session("short", "unread"), session("ask", "needs_you")]);
    expect(posted).toEqual([{ title: "ask 需要你", body: "ask 的最后一句", tag: "m/ask#needs" }]);
    await act(async () => void vi.advanceTimersByTime(SETTLE_MS + BATCH_QUIET_MS + 10));
    expect(posted.map((p) => p.title)).toEqual(["ask 需要你", "long 做完了"]);
  });

  it("页面在前台时不发", async () => {
    focused = true;
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([]);
  });

  it("开关没开时不发", async () => {
    localStorage.removeItem("persona.systemNotify");
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([]);
  });

  it("屏蔽了的 Agent 需要你时不发（和手机推送、Mac 一样）", async () => {
    mutes = { spaces: [], sessions: ["m/quiet"], allowed: [] };
    await show([session("quiet", "working"), session("loud", "working")]);
    await show([session("quiet", "needs_you"), session("loud", "needs_you")]);
    expect(posted.map((p) => p.title)).toEqual(["loud 需要你"]);
  });

  it("另一个标签页在前台时不发", async () => {
    _setTabId("tab-other");
    markFront(true);
    _setTabId("tab-me");
    await show([session("a", "working")]);
    await show([session("a", "needs_you")]);
    expect(posted).toEqual([]);
  });

  it("同一件事别的标签页刚发过就不再发", async () => {
    localStorage.setItem("persona.notified", JSON.stringify({ "m/a#needs": Date.now() }));
    await show([session("a", "working"), session("b", "working")]);
    await show([session("a", "needs_you"), session("b", "needs_you")]);
    expect(posted.map((p) => p.title)).toEqual(["b 需要你"]);
  });
});

describe("一批做完的合成一条（#4945）", () => {
  const long = "2026-01-01T09:58:00Z";
  const working = (...ids: string[]) => ids.map((id) => session(id, "working", long));
  const settle = (ms = SETTLE_MS + BATCH_QUIET_MS + 10) => act(async () => void vi.advanceTimersByTime(ms));
  beforeEach(() => localStorage.setItem("persona.notifyPrefs", JSON.stringify({ input: true, done: "always" })));

  it("几个先后做完：只弹一条，写几个、都是谁，点开去总览", async () => {
    await show(working("a", "b", "c"));
    await show([session("a", "unread"), ...working("b", "c")]);
    vi.advanceTimersByTime(500);
    await show([session("a", "unread"), session("b", "unread"), ...working("c")]);
    vi.advanceTimersByTime(500);
    await show([session("a", "unread"), session("b", "unread"), session("c", "unread")]);
    await settle();
    expect(posted).toEqual([{ title: "3 个任务做完了 · a、b、c", body: "点开看总览", tag: "finished#batch" }]);
  });

  it("超过三个只列三个；一直有新的做完也最多等一小段", async () => {
    const ids = ["a", "b", "c", "d", "e", "f"];
    await show(working(...ids));
    for (let i = 1; i <= ids.length; i++) {
      await show([...ids.slice(0, i).map((id) => session(id, "unread")), ...working(...ids.slice(i))]);
      await act(async () => void vi.advanceTimersByTime(1200));
    }
    // the first five went out BATCH_MAX_MS after the first joined; the sixth joins them in the next one
    await settle();
    expect(BATCH_MAX_MS).toBeLessThan(5 * 1200);
    expect(posted.map((p) => p.title)).toEqual(["5 个任务做完了 · a、b、c 等", "6 个任务做完了 · a、b、c 等"]);
  });

  it("等的时候有几个已经看过了：剩一个就照原来的单条", async () => {
    await show(working("a", "b"));
    await show([session("a", "unread"), session("b", "unread")]);
    vi.advanceTimersByTime(SETTLE_MS + 100);
    await show([session("a", "idle"), session("b", "unread")]);
    await settle();
    expect(posted).toEqual([{ title: "b 做完了", body: "b 的最后一句", tag: "m/b#finished" }]);
  });

  it("需要你不合并，照样各弹一条", async () => {
    await show(working("a", "b", "c", "d"));
    await show([session("a", "needs_you"), session("b", "needs_you"), session("c", "unread"), session("d", "unread")]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["a 需要你", "b 需要你", "2 个任务做完了 · c、d"]);
  });

  it("下一批替换这一条，还没看的留在里面；都看过了就收回", async () => {
    // ids no other test posted for: the notify module keeps its notifications between tests
    const at = (...st: AgentState[]) => ["p", "q", "r", "s"].map((id, i) => session(id, st[i], long));
    await show(working("p", "q", "r", "s"));
    await show(at("unread", "unread", "working", "working"));
    await settle();
    await show(at("idle", "unread", "unread", "unread"));
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["2 个任务做完了 · p、q", "3 个任务做完了 · q、r、s"]);
    await show(at("idle", "idle", "idle", "unread"));
    expect(closed).toEqual([]);
    await show(at("idle", "idle", "idle", "idle"));
    expect(closed).toEqual(["3 个任务做完了 · q、r、s"]);
  });

  it("收回后同一批 Agent 很快又做完：照样弹", async () => {
    const at = (st: AgentState) => ["u", "v"].map((id) => session(id, st, long));
    await show(working("u", "v"));
    await show(at("unread"));
    await settle();
    await show(at("idle"));
    expect(closed).toEqual(["2 个任务做完了 · u、v"]);
    await show(working("u", "v"));
    await show(at("unread"));
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["2 个任务做完了 · u、v", "2 个任务做完了 · u、v"]);
  });

  it("别的标签页已经发了这一批：这页不再发，自己的单条收回", async () => {
    await show(working("g", "h"));
    await show([session("g", "unread", long), ...working("h")]);
    await settle();
    localStorage.setItem("persona.notified", JSON.stringify({ "finished#batch@m/g,m/h": Date.now() }));
    await show([session("g", "unread", long), session("h", "unread", long)]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["g 做完了"]);
    expect(closed).toEqual(["g 做完了"]);
  });

  it("先单独弹过的，并进下一批时收回单条", async () => {
    await show(working("x", "y", "z"));
    await show([session("x", "unread", long), ...working("y", "z")]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["x 做完了"]);
    await show([session("x", "unread", long), session("y", "unread", long), session("z", "unread", long)]);
    await settle();
    expect(posted.map((p) => p.title)).toEqual(["x 做完了", "3 个任务做完了 · x、y、z"]);
    expect(closed).toEqual(["x 做完了"]);
  });
});

describe("已在需要你时又来一张卡（#4058）", () => {
  const ask: Interaction = { id: "q1", machine_id: "m", session_id: "a", kind: "question", created_at: "2026-01-01T10:00:00Z", questions: [{ question: "用哪个框架？" } as never] };
  const perm: Interaction = { id: "p1", machine_id: "m", session_id: "a", kind: "permission", created_at: "2026-01-01T10:00:05Z", tool_name: "Bash", tool_input: { command: "go test ./..." } };

  it("第二张卡也弹，写的是这张卡；同一张卡不重复弹", async () => {
    await show([session("a", "working")]);
    await show([session("a", "needs_you")], [ask]);
    expect(posted).toEqual([{ title: "a 在问你", body: "用哪个框架？", tag: "m/a#needs" }]);
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted.at(-1)).toEqual({ title: "a 要你批准", body: "go test ./...", tag: "m/a#needs" });
    await show([session("a", "needs_you")], [perm, ask]); // a snapshot again, the same cards
    await show([{ ...session("a", "needs_you"), last_text: "新的一句" }]);
    expect(posted).toHaveLength(2);
  });

  it("通知说的那张卡答掉了就收回，会话还在等另一张也收回", async () => {
    await show([session("a", "needs_you")], [ask]);
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted.map((p) => p.title)).toEqual(["a 要你批准"]);
    await show([session("a", "needs_you")], [ask]);
    expect(closed).toEqual(["a 要你批准"]);
    await show([session("a", "needs_you")], [ask]);
    expect(closed).toHaveLength(1);
  });

  it("需要你先到、卡后到（Codex 授权）：通知改写成这张卡，不算第二次", async () => {
    await show([session("a", "working")]);
    await show([session("a", "needs_you")], []);
    expect(posted.map((p) => p.title)).toEqual(["a 需要你"]);
    vi.setSystemTime(T0 + 10_000);
    await show([session("a", "needs_you")], [perm]);
    expect(posted.at(-1)).toEqual({ title: "a 要你批准", body: "go test ./...", tag: "m/a#needs" });
    await show([session("a", "needs_you")], [perm, { ...ask, id: "q2" }]);
    expect(posted.at(-1)?.title).toBe("a 在问你");
  });

  it("刚打开页面时已有的卡不弹", async () => {
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted).toEqual([]);
  });

  it("屏蔽了的 Agent 新卡不弹", async () => {
    mutes = { spaces: [], sessions: ["m/a"], allowed: [] };
    await show([session("a", "needs_you")], [ask]);
    await show([session("a", "needs_you")], [ask, perm]);
    expect(posted).toEqual([]);
  });

  describe("授权卡的通知带「允许 / 拒绝」（#4464）", () => {
    let shown: { title: string; ix?: string }[];
    beforeEach(() => {
      shown = [];
      (FakeNotification as unknown as { maxActions: number }).maxActions = 2;
      vi.stubGlobal("matchMedia", () => ({ matches: true }));
      const reg = { showNotification: async (title: string, o: { data?: { ix?: string } }) => void shown.push({ title, ix: o.data?.ix }), getNotifications: async () => [] };
      vi.stubGlobal("navigator", { ...navigator, serviceWorker: { controller: {}, ready: Promise.resolve(reg), getRegistration: async () => reg, addEventListener() {}, removeEventListener() {} } });
    });
    afterEach(() => delete (FakeNotification as unknown as { maxActions?: number }).maxActions);

    it("能批的会话：经 service worker 弹，带卡片 id", async () => {
      await show([session("a", "working")]);
      await show([session("a", "needs_you")], [perm]);
      await act(async () => {});
      expect(shown).toEqual([{ title: "a 要你批准", ix: "p1" }]);
      expect(posted).toEqual([]);
    });

    it("要先填内容的授权卡（fields）：不带按钮", async () => {
      await show([session("a", "working")]);
      await show([session("a", "needs_you")], [{ ...perm, fields: [{ key: "pw", label: "密码", secret: true }] }]);
      await act(async () => {});
      expect(shown).toEqual([]);
      expect(posted.map((p) => p.title)).toEqual(["a 要你批准"]);
    });

    it("不能批的会话（approve=false）：不带按钮，照旧弹普通通知", async () => {
      const locked = { ...session("a", "needs_you"), source_kind: "codex_history", capabilities: { approve: false } } as Session;
      await show([{ ...locked, state: "working", status: "working" }]);
      await show([locked], [perm]);
      await act(async () => {});
      expect(shown).toEqual([]);
      expect(posted.map((p) => p.title)).toEqual(["a 要你批准"]);
    });
  });
});
