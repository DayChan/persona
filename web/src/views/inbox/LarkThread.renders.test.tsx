// @vitest-environment jsdom
// A Feishu chat on a busy hub: every `lark` push naming the open chat reads its newest page again as fresh JSON, and
// every read-states reply comes back as new objects. Messages and receipts that read the same keep their objects
// (lib/lark-reuse.ts) and the rows are memoized, so a push renders again only the messages that changed and an
// unchanged read-states reply renders none (it rendered every row of the chat before). A push on a new day still draws
// every message again, so yesterday's times get their date.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LarkMessage } from "../../lib/inbox";
import type { ReadState } from "../../lib/lark-chat";

type Listener = (msg: Record<string, unknown>) => void;
const { listeners } = vi.hoisted(() => ({ listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>() }));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    onHubEvent: (type: string, l: Listener) => {
      if (!listeners.has(type)) listeners.set(type, new Set());
      listeners.get(type)!.add(l);
      return () => void listeners.get(type)?.delete(l);
    },
  };
});
// every render of a chat row asks once whether its body shows: counted per message
const renders = vi.hoisted(() => new Map<string, number>());
vi.mock("../../lib/lark-display", async (original) => {
  const real = await original<typeof import("../../lib/lark-display")>();
  return {
    ...real,
    showsBody: (...a: Parameters<typeof real.showsBody>) => {
      renders.set(a[1].id, (renders.get(a[1].id) ?? 0) + 1);
      return real.showsBody(...a);
    },
  };
});
import { t } from "../../i18n";
import { clock } from "../../lib/format";
import { LarkThread } from "./LarkThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// 50 messages a minute apart from two people and me (every fifth), so some rows follow on under the one before
const msg = (n: number, extra: Partial<LarkMessage> = {}): LarkMessage => {
  const mine = n % 5 === 0;
  return {
    id: `m${n}`, position: String(n), sender_id: mine ? "me" : `u${n % 2}`, sender_name: mine ? "Me" : `Colleague ${n % 2}`, is_mine: mine,
    created_at: new Date(Date.UTC(2026, 10, 1, 9, n)).toISOString(), text: `Message ${n}`, ...extra,
  };
};
const chat = (n: number) => Array.from({ length: n }, (_, i) => msg(i + 1));

let host: HTMLDivElement;
let root: Root;
let newest: LarkMessage[];
let replies: LarkMessage[];
let readOf: (id: string) => ReadState;
const pageReads: string[] = [];
const stateReads: string[][] = [];
const flush = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};
const push = async () => {
  await act(async () => listeners.get("lark")?.forEach((l) => l({ type: "lark", feed_ids: ["f1"] })));
  await flush();
};
const wait = async (ms: number) => {
  await act(async () => void vi.advanceTimersByTime(ms));
  await flush();
};
const total = () => [...renders.values()].reduce((a, b) => a + b, 0);
const rendered = () => [...renders.keys()].sort();

// Both of the chat's IntersectionObservers (read reports, read receipts) see the rows they watch when `see` is called.
const observers = new Set<FakeObserver>();
class FakeObserver {
  private els = new Set<Element>();
  constructor(private cb: IntersectionObserverCallback) {
    observers.add(this);
  }
  observe(el: Element) {
    this.els.add(el);
  }
  unobserve(el: Element) {
    this.els.delete(el);
  }
  disconnect() {
    this.els.clear();
    observers.delete(this);
  }
  takeRecords() {
    return [];
  }
  fire(on: boolean) {
    const entries = [...this.els].map((target) => ({
      target, isIntersecting: on, intersectionRatio: on ? 1 : 0, rootBounds: null, intersectionRect: target.getBoundingClientRect(),
    }) as unknown as IntersectionObserverEntry);
    if (entries.length) this.cb(entries, this as unknown as IntersectionObserver);
  }
}
const see = async (on = true) => {
  await act(async () => observers.forEach((o) => o.fire(on)));
  await flush();
};

beforeEach(() => {
  pageReads.length = 0;
  stateReads.length = 0;
  renders.clear();
  observers.clear();
  newest = chat(50);
  replies = [];
  readOf = (id) => ({ message_id: id, read_count: 1, unread_count: 2 });
  vi.stubGlobal("IntersectionObserver", FakeObserver);
  Element.prototype.scrollIntoView = vi.fn();
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    let body: unknown = { error: "not found" };
    let status = 404;
    if (url.includes("/conversations/f1/messages?")) {
      pageReads.push(url);
      body = { messages: newest };
      status = 200;
    } else if (url.includes("/conversations/f1/read-states")) {
      const ids = (JSON.parse(String(init.body)) as { message_ids: string[] }).message_ids;
      stateReads.push(ids);
      body = { states: ids.map((id) => readOf(id)) };
      status = 200;
    } else if (url.includes("/messages/m3/thread?")) {
      body = { messages: replies };
      status = 200;
    } else if (url.endsWith("/conversations/f1/read")) {
      body = {};
      status = 200;
    }
    // a real reply is fresh JSON: no message or receipt keeps its object
    return { ok: status < 400, status, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
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

const open = async (topic = false) => {
  await act(async () => root.render(<LarkThread chat={{ feed: "f1", title: "Example", group: true, topic }} />));
  await flush();
  expect(host.querySelectorAll("[data-mid]")).toHaveLength(50);
  expect(rendered()).toHaveLength(50);
  renders.clear();
};
const textOf = (id: string) => host.querySelector(`[data-mid="${id}"]`)?.textContent ?? "";

describe("a Feishu chat on a push renders again only the messages that changed", () => {
  it("renders no row again for a push that changed nothing", async () => {
    await open();
    await push();
    expect(pageReads).toHaveLength(2);
    expect(total()).toBe(0);
  });

  it("renders only the new row for one new message (the one before keeps its grouping)", async () => {
    await open();
    // mine a minute after my m50: it follows on under it, and m50 itself draws as it did
    newest = [...chat(50), msg(51, { sender_id: "me", sender_name: "Me", is_mine: true })];
    await push();
    expect(host.querySelectorAll("[data-mid]")).toHaveLength(51);
    expect(rendered()).toEqual(["m51"]);
  });

  it("renders only the edited row when one message changed", async () => {
    await open();
    newest = chat(50).map((m) => (m.id === "m10" ? { ...m, text: "Edited message", edited: true } : m));
    await push();
    expect(textOf("m10")).toContain("Edited message");
    expect(rendered()).toEqual(["m10"]);
  });

  it("renders no row for an unchanged read-states reply, and only the row whose receipt moved", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await open();
    // my messages come into view: the first ask goes at once and draws their marks
    await see();
    await wait(0);
    expect(stateReads).toHaveLength(1);
    expect(stateReads[0]).toHaveLength(10);
    expect(rendered()).toEqual(stateReads[0].slice().sort());
    renders.clear();

    // they leave and come back: asked again (3 s later), the same answer renders nothing
    await see(false);
    await see();
    await wait(3000);
    expect(stateReads).toHaveLength(2);
    expect(total()).toBe(0);

    // one more person read m25: only its row draws again
    readOf = (id) => ({ message_id: id, read_count: id === "m25" ? 2 : 1, unread_count: id === "m25" ? 1 : 2 });
    await see(false);
    await see();
    await wait(3000);
    expect(stateReads).toHaveLength(3);
    expect(rendered()).toEqual(["m25"]);
  });
});

describe("a Feishu chat left open past midnight", () => {
  // 15:00 on the local day of the chat's messages, and 09:00 the next morning
  const days = () => {
    const d = new Date(msg(1).created_at!);
    return [new Date(d.getFullYear(), d.getMonth(), d.getDate(), 15, 0), new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1, 9, 0)];
  };
  const reply = (): LarkMessage => ({
    id: "r1", position: "r1", sender_id: "u1", sender_name: "Colleague 1", is_mine: false,
    created_at: new Date(Date.parse(msg(3).created_at!) + 30_000).toISOString(), text: "Reply 1",
  });
  const clockAt = (iso: string | undefined, now: Date) => {
    vi.setSystemTime(now);
    return clock(iso);
  };

  it("gives every row's time its date on the new day, without a push", async () => {
    const [today, tomorrow] = days();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
    const after = clockAt(msg(1).created_at, tomorrow);
    const before = clockAt(msg(1).created_at, today);
    expect(after).not.toBe(before);
    await open();
    expect(textOf("m1")).toContain(before);
    expect(textOf("m1")).not.toContain(after);

    // a minute on the same day draws nothing
    await wait(60_000);
    expect(total()).toBe(0);

    vi.setSystemTime(tomorrow);
    await wait(60_000);
    expect(textOf("m1")).toContain(after);
    expect(rendered()).toHaveLength(50);
  });

  it("gives a list row and a reply in an opened thread their date on the first push of the new day", async () => {
    const [today, tomorrow] = days();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
    const after = clockAt(reply().created_at, tomorrow);
    const rowAfter = clockAt(msg(1).created_at, tomorrow);
    const before = clockAt(reply().created_at, today);
    newest = chat(50).map((m) => (m.id === "m3" ? { ...m, thread_reply_count: 1 } : m));
    replies = [reply()];
    await open();
    const toggle = [...host.querySelectorAll<HTMLButtonElement>('[data-mid="m3"] button')]
      .find((b) => b.textContent?.includes(t("inbox-larkthread.replies", { count: 1 })));
    expect(toggle).toBeDefined();
    await act(async () => toggle!.click());
    await flush();
    expect(textOf("r1")).toContain(before);
    expect(textOf("r1")).not.toContain(after);
    expect(textOf("m1")).not.toContain(rowAfter);

    // the push itself changed nothing, so it is the new day alone that draws them again
    vi.setSystemTime(tomorrow);
    await push();
    expect(textOf("r1")).toContain(after);
    expect(textOf("m1")).toContain(rowAfter);
  });

  it("gives a topic group's cards their date on the first push of the new day", async () => {
    const [today, tomorrow] = days();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
    const after = clockAt(msg(1).created_at, tomorrow);
    const before = clockAt(msg(1).created_at, today);
    await open(true);
    expect(host.querySelectorAll('[data-testid="lark-topic"]')).toHaveLength(50);
    expect(textOf("m1")).toContain(before);
    expect(textOf("m1")).not.toContain(after);

    vi.setSystemTime(tomorrow);
    await push();
    expect(textOf("m1")).toContain(after);
  });
});
