// @vitest-environment jsdom
// The assistant page's manager (docs/assistant/managers.md "Clients"): which requests name it, which live updates are its, the
// things merged lists showed as another manager's, and the roster's grouping that the machines, extensions and inbox
// pages go by.
import { afterEach, describe, expect, it } from "vitest";
import {
  chooseManager, conversationManager, defaultKnown, defaultManager, fromDefaultManager, fromShownManager, itemHeaders, managerHeaderFor, noteConversation, noteItemManager,
  onDefaultManager, ownedSub, ownerHeaders, parseOwnedSub, pinManager, resetManagerChoice, setRosterDefault, settleRosterDefault, shownManager, withManager,
} from "./manager-choice";
import { groupByManager, lastSyncedText, managerOrder, multiManager, offlineManagerOf, ownerIn, type ManagerList } from "./managers";

afterEach(() => resetManagerChoice());

const ROSTER: ManagerList = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true, builtin: true },
    { id: "m-1", name: "工作", online: true },
    { id: "m-2", name: "家里", online: false, last_seen: new Date(Date.now() - 3 * 3600_000).toISOString() },
    { id: "m-3", name: "测试", online: false, installing: new Date(Date.now() + 60_000).toISOString() },
  ],
};

describe("the manager the assistant page shows", () => {
  it("names the chosen one on the assistant's requests only, none for the default", () => {
    expect(managerHeaderFor("/assistant")).toBeUndefined();
    chooseManager("m-1");
    expect(shownManager()).toBe("m-1");
    expect(managerHeaderFor("/assistant")).toBe("m-1");
    expect(managerHeaderFor("/assistant/conversations")).toBe("m-1");
    expect(managerHeaderFor("/assistant?archived=1")).toBe("m-1");
    expect(managerHeaderFor("/assistant/tasks/t1/run")).toBe("m-1");
    // the health page has no switcher; others go by ownership
    expect(managerHeaderFor("/assistant?space=health")).toBeUndefined();
    expect(managerHeaderFor("/assistant/memory?space=health")).toBeUndefined();
    expect(managerHeaderFor("/assistants")).toBeUndefined();
    expect(managerHeaderFor("/sessions/m/s/messages")).toBeUndefined();
    expect(managerHeaderFor("/inbox")).toBeUndefined();
    chooseManager("assistant");
    expect(managerHeaderFor("/assistant")).toBeUndefined();
  });

  it("follows a default that moved: picking it sends no header", () => {
    chooseManager("m-1");
    setRosterDefault("m-1");
    expect(shownManager()).toBe("m-1");
    expect(managerHeaderFor("/assistant")).toBeUndefined();
  });

  it("takes only the shown manager's live updates; none is the hub's own assistant", () => {
    expect(fromShownManager({ type: "assistant_feed" })).toBe(true);
    expect(fromShownManager({ type: "assistant_feed", manager: "m-1" })).toBe(false);
    chooseManager("m-1");
    expect(fromShownManager({ type: "assistant_feed" })).toBe(false);
    expect(fromShownManager({ type: "assistant_feed", manager: "m-1" })).toBe(true);
  });

  it("names the manager of a thing a merged list showed as another's", () => {
    noteItemManager("/extensions/feishu", "m-1");
    noteItemManager("/inbox/lark:1", "assistant");
    expect(managerHeaderFor("/extensions/feishu")).toBe("m-1");
    expect(managerHeaderFor("/extensions/feishu/settings?x=1")).toBe("m-1");
    expect(managerHeaderFor("/extensions")).toBeUndefined();
    expect(managerHeaderFor("/extensions/feishu2")).toBeUndefined();
    expect(managerHeaderFor("/inbox/lark:1/done")).toBeUndefined();
    noteItemManager("/extensions/feishu", undefined); // the default's again
    expect(managerHeaderFor("/extensions/feishu")).toBeUndefined();
  });
});

describe("whose a request is", () => {
  it("keeps every stamp and asks the default only when the request goes out (the roster may move it meanwhile)", () => {
    noteItemManager("/inbox/x", "m-1");
    noteItemManager("/inbox/y", "assistant");
    expect(managerHeaderFor("/inbox/x/done")).toBe("m-1");
    expect(managerHeaderFor("/inbox/y/done")).toBeUndefined();
    setRosterDefault("m-1"); // m-1 became the default after the list was read
    expect(managerHeaderFor("/inbox/x/done")).toBeUndefined();
    expect(managerHeaderFor("/inbox/y/done")).toBe("assistant");
  });

  it("routes todos under /todos/:id and sub-routes to their noted manager", () => {
    resetManagerChoice();
    noteItemManager("/todos/t1", "assistant"); // default manager
    noteItemManager("/todos/t2", "m-2");       // non-default manager
    expect(managerHeaderFor("/todos/t2")).toBe("m-2");
    expect(managerHeaderFor("/todos/t2/retry")).toBe("m-2");
    expect(managerHeaderFor("/todos/t2/accept")).toBe("m-2");
    expect(managerHeaderFor("/todos/t2/decisions")).toBe("m-2");
    expect(managerHeaderFor("/todos/t2/agents/box/s1")).toBe("m-2");
    expect(managerHeaderFor("/todos/t1")).toBeUndefined();
    expect(managerHeaderFor("/todos/t1/retry")).toBeUndefined();
  });

  it("one conversation's routes go to its manager, never to the page's pick", () => {
    chooseManager("m-1");
    // unknown to this page: no header, the hub sends it to the conversation's manager
    expect(managerHeaderFor("/assistant/conversations/c9/feed")).toBeUndefined();
    noteConversation("c1", "m-1");
    noteConversation("h1", "assistant"); // the default's (its health space, or listed while it was shown)
    expect(managerHeaderFor("/assistant/conversations/c1/feed?since=1")).toBe("m-1");
    expect(managerHeaderFor("/assistant/conversations/h1/stop")).toBeUndefined();
    // a new one goes to the page's pick; the health space's to the default
    expect(managerHeaderFor("/assistant/conversations")).toBe("m-1");
    expect(managerHeaderFor("/assistant/conversations?space=health")).toBeUndefined();
  });

  it("a pin holds while its view is open, the latest one winning", () => {
    const off = pinManager("/lark/messages", "m-2");
    expect(managerHeaderFor("/lark/messages/om_1/thread?limit=50")).toBe("m-2");
    expect(managerHeaderFor("/lark/status")).toBeUndefined();
    const off2 = pinManager("/lark/messages", "m-1");
    expect(managerHeaderFor("/lark/messages/om_1/reactions")).toBe("m-1");
    off2();
    expect(managerHeaderFor("/lark/messages/om_1/reactions")).toBe("m-2");
    off();
    expect(managerHeaderFor("/lark/messages/om_1/reactions")).toBeUndefined();
    expect(pinManager("/x", undefined)).toBeTypeOf("function");
  });

  it("a pin goes with its own view: two of one manager, the first released, the second still holds", () => {
    const first = pinManager("/extensions/voice", "m-2");
    const second = pinManager("/extensions/voice", "m-2");
    first();
    expect(managerHeaderFor("/extensions/voice/ui")).toBe("m-2");
    first(); // twice: still only its own
    expect(managerHeaderFor("/extensions/voice/ui")).toBe("m-2");
    second();
    expect(managerHeaderFor("/extensions/voice/ui")).toBeUndefined();
  });

  it("an item's own requests name its manager, by header or in the URL; the default's name none", () => {
    expect(itemHeaders("m-1")).toEqual({ "X-Persona-Manager": "m-1" });
    expect(itemHeaders("assistant")).toBeUndefined();
    expect(itemHeaders(undefined)).toBeUndefined();
    expect(withManager("/api/v1/lark/media/k1?ol_account=2", "m-1")).toBe("/api/v1/lark/media/k1?ol_account=2&manager=m-1");
    expect(withManager("/lark/conversations/f1/read", "m-1")).toBe("/lark/conversations/f1/read?manager=m-1");
    expect(withManager("/api/v1/x#y", "m-1")).toBe("/api/v1/x?manager=m-1#y");
    expect(withManager("/api/v1/x", "assistant")).toBe("/api/v1/x");
    // not the hub's: as it is
    expect(withManager("https://example.com/a.png", "m-1")).toBe("https://example.com/a.png");
    expect(withManager("blob:abc", "m-1")).toBe("blob:abc");
    setRosterDefault("m-1");
    expect(itemHeaders("m-1")).toBeUndefined();
    expect(itemHeaders("assistant")).toEqual({ "X-Persona-Manager": "assistant" });
  });

  it("a conversation the default's page listed is not noted, so the placeholder from before the roster is never sent", () => {
    // before the roster loads, the page asked the default (the placeholder "assistant")
    noteConversation("c1", undefined);
    noteConversation("c2", "m-2");
    setRosterDefault("m-1");
    expect(managerHeaderFor("/assistant/conversations/c1/feed")).toBeUndefined();
    expect(managerHeaderFor("/assistant/conversations/c2/feed")).toBe("m-2");
    // listed again as the default's: the note goes
    noteConversation("c2", undefined);
    expect(managerHeaderFor("/assistant/conversations/c2/feed")).toBeUndefined();
  });

  it("the default's own messages are told from the others'", () => {
    expect(fromDefaultManager({ type: "workspace_board_settings" })).toBe(true);
    expect(fromDefaultManager({ type: "workspace_board_settings", manager: "m-1" })).toBe(false);
    setRosterDefault("m-1");
    expect(fromDefaultManager({ type: "workspace_board_settings" })).toBe(false);
    expect(fromDefaultManager({ type: "workspace_board_settings", manager: "m-1" })).toBe(true);
  });
});

describe("a thing's own manager", () => {
  it("names it, or the default once another is picked; nothing with no pick and no stamp", () => {
    expect(ownerHeaders(undefined)).toBeUndefined();
    expect(ownerHeaders("m-1")).toEqual({ "X-Persona-Manager": "m-1" });
    chooseManager("m-1");
    expect(ownerHeaders(undefined)).toEqual({ "X-Persona-Manager": "assistant" });
  });

  it("sends nothing for a thing stamped with the default when nothing is picked: one manager stamps everything with it", () => {
    setRosterDefault("assistant");
    expect(ownerHeaders("assistant")).toBeUndefined();
    // picked another: the default's thing still goes to the default, over the pick
    chooseManager("m-1");
    expect(ownerHeaders("assistant")).toEqual({ "X-Persona-Manager": "assistant" });
    chooseManager(null);
    setRosterDefault("m-1");
    expect(ownerHeaders("m-1")).toBeUndefined();
    expect(ownerHeaders("assistant")).toEqual({ "X-Persona-Manager": "assistant" });
  });

  it("writes and reads another manager's settings route; the default's is the id alone", () => {
    setRosterDefault("assistant");
    expect(ownedSub("wechat", "m-1")).toBe("wechat@m-1");
    expect(ownedSub("wechat", "assistant")).toBe("wechat");
    expect(ownedSub("wechat", undefined)).toBe("wechat");
    expect(parseOwnedSub("wechat@m-1")).toEqual({ id: "wechat", manager: "m-1" });
    expect(parseOwnedSub("wechat")).toEqual({ id: "wechat" });
  });
});

describe("the roster's default", () => {
  it("is a placeholder until the roster is read, then tells what goes by it to look again", () => {
    const told: { id: string; settled: boolean }[] = [];
    const off = onDefaultManager((was) => told.push(was));
    expect(defaultKnown()).toBe(false);
    // a one-manager hub: the placeholder was right, and still the pages are told it is known
    setRosterDefault("assistant");
    expect(defaultKnown()).toBe(true);
    expect(told).toEqual([{ id: "assistant", settled: false }]);
    setRosterDefault("assistant");
    expect(told).toHaveLength(1);
    // the default moved
    setRosterDefault("m-1");
    expect(told.at(-1)).toEqual({ id: "assistant", settled: true });
    off();
  });

  it("settles without a roster (an older hub, or a read that failed)", () => {
    const told: unknown[] = [];
    onDefaultManager((was) => told.push(was));
    settleRosterDefault();
    settleRosterDefault();
    expect(defaultKnown()).toBe(true);
    expect(defaultManager()).toBe("assistant");
    expect(told).toHaveLength(1);
  });

  it("names a conversation's manager for the pages that only know the conversation", () => {
    setRosterDefault("assistant");
    noteConversation("c1", "m-1");
    expect(conversationManager("c1")).toBe("m-1");
    expect(conversationManager("c2")).toBeUndefined();
  });
});

describe("grouping by manager", () => {
  it("only with more than one installed manager", () => {
    expect(multiManager(null)).toBe(false);
    expect(multiManager({ default: "assistant", managers: [ROSTER.managers[0], ROSTER.managers[3]] })).toBe(false);
    expect(multiManager(ROSTER)).toBe(true);
    expect(groupByManager([{ id: "x" }], { default: "assistant", managers: [ROSTER.managers[0]] }, () => undefined)).toBeNull();
  });

  it("the default first, then the roster's order, empty groups left out, unknown and none to the default", () => {
    const list = { ...ROSTER, default: "m-1" };
    expect(managerOrder(list).map((m) => m.id)).toEqual(["m-1", "assistant", "m-2"]);
    const items = [{ id: "a", manager: "assistant" }, { id: "b" }, { id: "c", manager: "m-2" }, { id: "d", manager: "m-9" }, { id: "e", manager: "m-1" }];
    const groups = groupByManager(items, list, (x) => x.manager)!;
    expect(groups.map((g) => [g.manager.id, g.items.map((x) => x.id)])).toEqual([
      ["m-1", ["b", "d", "e"]],
      ["assistant", ["a"]],
      ["m-2", ["c"]],
    ]);
    expect(ownerIn(list, undefined)).toBe("m-1");
    expect(ownerIn(list, "m-3")).toBe("m-1"); // still installing: nothing of its own is shown yet
  });

  it("an offline computer of an offline manager is that manager's; none with one manager or an online one", () => {
    expect(offlineManagerOf(ROSTER, { online: false, manager: "m-2" })?.name).toBe("家里");
    expect(offlineManagerOf(ROSTER, { online: true, manager: "m-2" })).toBeNull();
    expect(offlineManagerOf(ROSTER, { online: false, manager: "m-1" })).toBeNull();
    expect(offlineManagerOf(ROSTER, { online: false })).toBeNull(); // the default's, online
    expect(offlineManagerOf({ default: "assistant", managers: [{ ...ROSTER.managers[0], online: false }] }, { online: false })).toBeNull();
  });

  it("an offline manager says when it last synced", () => {
    expect(lastSyncedText(ROSTER.managers[1])).toBe("");
    expect(lastSyncedText(ROSTER.managers[2])).toMatch(/^最后同步于 3\s?小时前$/);
  });
});

describe("api", () => {
  it("sends X-Persona-Manager on the chosen manager's assistant requests", async () => {
    const { api } = await import("./api");
    const seen: string[] = [];
    const real = globalThis.fetch;
    globalThis.fetch = (async (url: string, init: RequestInit) => {
      seen.push(`${(init.method ?? "GET")} ${url.replace(/^.*\/api\/v1/, "")} ${new Headers(init.headers).get("X-Persona-Manager")}`);
      return new Response("{}", { status: 200 });
    }) as typeof fetch;
    try {
      await api("/assistant");
      chooseManager("m-1");
      await api("/assistant/conversations", { method: "POST", json: {} });
      await api("/machines");
      // picking it also reads its page again (lib/store.ts)
      expect(seen).toEqual(["GET /assistant null", "GET /assistant m-1", "POST /assistant/conversations m-1", "GET /machines null"]);
      // the health space stays the default manager's whatever the pick (docs/assistant/health.md): its list and a new conversation
      seen.length = 0;
      const { startHealthConversation } = await import("../views/health/reports");
      await api("/assistant?space=health");
      await startHealthConversation({ prompt: "x" }).catch(() => {});
      expect(seen).toEqual(["GET /assistant?space=health null", "POST /assistant/conversations?space=health null"]);
    } finally {
      globalThis.fetch = real;
    }
  });
});
