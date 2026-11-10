// @vitest-environment jsdom
// "Hand to AI" with several managers (docs/assistant/managers.md "Clients"): the item goes to its manager's main
// conversation by default (#1194), handed off through the item's manager so that conversation may write back to it, or
// to a new conversation with that manager, and the conversation's next requests go there too; an item stamped with the
// default (a hub with one manager stamps every item so) sends no header at all.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/router", () => ({ navigate: vi.fn() }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { chooseManager, chosenManager, managerHeaderFor, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { navigate } from "../../lib/router";
import { HandOffDialog } from "./InboxDialogs";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (manager: string): InboxItem => ({
  id: "lark:f1:1", manager, source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: "Example", preview: "text", count: 1,
  updated_at: "2026-11-01T09:00:00Z", tier: "action", lark: { feed_id: "f1", is_group: false },
});

let host: HTMLDivElement;
let root: Root;
type Call = { url: string; method: string; header: string | null; body: unknown };
const calls: Call[] = [];
// what GET /assistant answers, and where a hand-off goes, by the manager it names (none: the default's)
let mains: Record<string, string | undefined>;

beforeEach(() => {
  calls.length = 0;
  mains = { assistant: "main-a", "m-1": "main-m1" };
  resetManagerChoice();
  resetInboxForTests();
  setRosterDefault("assistant");
  vi.mocked(navigate).mockClear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const header = new Headers(init?.headers).get("X-Persona-Manager");
    calls.push({ url, method: init?.method ?? "GET", header, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const main = mains[header ?? "assistant"];
    const body = url.endsWith("/assistant/conversations") ? { session: { machine_id: "mac", session_id: "s-new" } }
      : url.endsWith("/assistant") ? { machine_id: "mac", conversations: [], ...(main ? { main } : {}) }
      : url.endsWith("/handoff") ? { item: item("assistant"), session: { machine_id: "mac", session_id: main ?? "s-new" } }
      : url.includes("/inbox?") ? { items: [item("assistant")] }
      : url.includes("/inbox/") ? item("assistant") : {};
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  resetInboxForTests();
  vi.unstubAllGlobals();
});

const pick = (label: string) => {
  const radio = [...document.body.querySelectorAll('[role="radio"]')].find((b) => b.textContent?.includes(label)) as HTMLButtonElement;
  act(() => radio.click());
};
const doneBox = () => document.body.querySelector('input[type="checkbox"]') as HTMLInputElement;
const toggleDone = () => act(() => doneBox().click());
const send = async (done: (c: Call) => boolean) => {
  const button = [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "发送")!;
  await act(async () => button.click());
  await vi.waitFor(() => expect(calls.some(done)).toBe(true));
};
const started = () => calls.find((c) => c.url.endsWith("/assistant/conversations"))!;
const sentTo = (sid: string) => (c: Call) => c.method === "POST" && c.url === `/api/v1/sessions/mac/${sid}/messages`;
const handedOff = (c: Call) => c.method === "POST" && c.url === "/api/v1/inbox/lark:f1:1/handoff";
const handOffs = () => calls.filter(handedOff);
const filed = (c: Call) => c.url === "/api/v1/inbox/lark:f1:1/done";

/** Answers the requests match picks with status and body (recorded like the others); the rest as before. */
const answerWith = (match: (url: string) => boolean, reply: () => { status: number; body: unknown }) => {
  const fetcher = vi.mocked(fetch);
  const answer = fetcher.getMockImplementation()!;
  fetcher.mockImplementation(async (url, init) => {
    if (!match(String(url))) return answer(url, init);
    calls.push({ url: String(url), method: init?.method ?? "GET", header: new Headers(init?.headers).get("X-Persona-Manager"), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const r = reply();
    return { ok: r.status < 300, status: r.status, text: async () => JSON.stringify(r.body), headers: new Headers() } as Response;
  });
};
/** A hub or manager from before hand-offs: the route is not there. */
const olderHub = () => answerWith((url) => url.endsWith("/handoff"), () => ({ status: 404, body: { error: "not found" } }));

describe("Hand to AI: Persona's main conversation", () => {
  it("is picked by default and checked, without marking the item done", () => {
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    const radios = [...document.body.querySelectorAll('[role="radio"]')];
    expect(radios[0].textContent).toContain("Persona（主对话）");
    expect(radios[0].getAttribute("aria-checked")).toBe("true");
    expect(radios[1].textContent).toContain("Persona（新对话）");
    expect(doneBox().checked).toBe(false);
  });

  it("hands the item to the default's manager, which sends it to the main conversation, and opens it", async () => {
    const onClose = vi.fn();
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={onClose} />));
    await send(handedOff);
    const handOff = handOffs()[0];
    const body = handOff.body as { text: string; client_id: string };
    expect(body.text).toContain('kind="context" title="');
    expect(body.client_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "main-a" }));
    expect(onClose).toHaveBeenCalled();
    expect(calls.every((c) => c.header === null)).toBe(true);
    expect(calls.some((c) => c.url.includes("/messages") || c.url.endsWith("/assistant/conversations"))).toBe(false);
    expect(calls.some(filed)).toBe(false);
    expect(chosenManager()).toBeNull();
  });

  it("hands another manager's item to that manager and shows its page", async () => {
    act(() => root.render(<HandOffDialog item={item("m-1")} onClose={() => {}} />));
    await send(handedOff);
    expect(handOffs()[0].header).toBe("m-1");
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "main-m1" }));
    expect(chosenManager()).toBe("m-1");
    expect(managerHeaderFor("/assistant/conversations/main-m1/feed")).toBe("m-1");
  });

  it("hands the default's item to the default's manager while the page shows another", async () => {
    chooseManager("m-1");
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    await send(handedOff);
    expect(handOffs()[0].header).toBeNull();
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "main-a" }));
    expect(chosenManager()).toBeNull();
  });

  it("marks the item done once handed off when the user asks", async () => {
    await reloadInbox();
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    toggleDone();
    await send(handedOff);
    await vi.waitFor(() => expect(calls.some(filed)).toBe(true));
    expect(calls.findIndex(filed)).toBeGreaterThan(calls.findIndex(handedOff));
  });

  it("stays open with the reason when the hand-off fails; the same text again after no answer is the same hand-off", async () => {
    let reply = { status: 502, body: { code: "assistant_in_doubt", error: "It may have gone through", delivered: "unknown" } as Record<string, unknown> };
    answerWith((url) => url.endsWith("/handoff"), () => reply);
    const onClose = vi.fn();
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={onClose} />));
    await send(handedOff);
    await vi.waitFor(() => expect(document.body.textContent).toContain("It may have gone through"));
    expect(onClose).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
    reply = { status: 409, body: { code: "refused", error: "Refused for now" } };
    await send(() => handOffs().length === 2);
    await vi.waitFor(() => expect(document.body.textContent).toContain("Refused for now"));
    const ids = handOffs().map((c) => (c.body as { client_id: string }).client_id);
    expect(ids[1]).toBe(ids[0]);
    // An answer the hub keeps under that id: trying again is a new hand-off.
    reply = { status: 200, body: { item: item("assistant"), session: { machine_id: "mac", session_id: "main-a" } } };
    await send(() => handOffs().length === 3);
    expect((handOffs()[2].body as { client_id: string }).client_id).not.toBe(ids[0]);
    await vi.waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls.some((c) => c.url.includes("/messages"))).toBe(false);
  });
});

describe("Hand to AI: mark as done", () => {
  it("follows the target until the user sets it, then keeps the user's choice", () => {
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    pick("Persona（新对话）");
    expect(doneBox().checked).toBe(true);
    pick("Persona（主对话）");
    expect(doneBox().checked).toBe(false);
    toggleDone();
    expect(doneBox().checked).toBe(true);
    pick("Persona（新对话）");
    pick("Persona（主对话）");
    expect(doneBox().checked).toBe(true);
    toggleDone();
    pick("Persona（新对话）");
    expect(doneBox().checked).toBe(false);
  });
});

describe("Hand to AI: an older hub or manager", () => {
  it("sends the item to the main conversation as before and opens it", async () => {
    olderHub();
    act(() => root.render(<HandOffDialog item={item("m-1")} onClose={() => {}} />));
    await send(sentTo("main-m1"));
    const message = calls.find(sentTo("main-m1"))!;
    expect((message.body as { text: string }).text).toContain('kind="context" title="');
    expect(calls.find((c) => c.url === "/api/v1/assistant")).toMatchObject({ header: "m-1" });
    expect(calls.some((c) => c.url.endsWith("/assistant/conversations"))).toBe(false);
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "main-m1" });
    expect(chosenManager()).toBe("m-1");
  });

  it("marks the item done only once the message got there, when asked to", async () => {
    await reloadInbox();
    olderHub();
    const fetcher = vi.mocked(fetch);
    const answer = fetcher.getMockImplementation()!;
    let deliver!: () => void;
    const delivered = new Promise<void>((r) => (deliver = r));
    fetcher.mockImplementation(async (url, init) => {
      const reply = await answer(url, init);
      if (String(url) === "/api/v1/sessions/mac/main-a/messages") await delivered;
      return reply;
    });
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    toggleDone();
    await send(sentTo("main-a"));
    await act(async () => {});
    expect(calls.some(filed)).toBe(false);
    deliver();
    await vi.waitFor(() => expect(calls.some(filed)).toBe(true));
  });

  it("leaves the item open when the message didn't get there", async () => {
    await reloadInbox();
    olderHub();
    answerWith((url) => url === "/api/v1/sessions/mac/main-a/messages", () => ({ status: 409, body: { code: "refused", error: "Refused" } }));
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    toggleDone();
    await send(sentTo("main-a"));
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "main-a" }));
    await act(async () => {});
    expect(calls.some(filed)).toBe(false);
  });

  it("starts the manager's main conversation with the item when it has none", async () => {
    olderHub();
    mains["m-1"] = undefined;
    act(() => root.render(<HandOffDialog item={item("m-1")} onClose={() => {}} />));
    await send((c) => c.url.endsWith("/assistant/conversations"));
    expect(started()).toMatchObject({ method: "POST", header: "m-1", body: { main: true } });
    expect((started().body as { prompt: string }).prompt).toContain('kind="context" title="');
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "s-new" }));
    expect(managerHeaderFor("/assistant/conversations/s-new/feed")).toBe("m-1");
  });

  it("stays open with the reason when the manager can't be read, moving nothing", async () => {
    olderHub();
    answerWith((url) => url.endsWith("/assistant"), () => ({ status: 503, body: { code: "assistant_offline", error: "Assistant temporarily unavailable" } }));
    const onClose = vi.fn();
    act(() => root.render(<HandOffDialog item={item("m-1")} onClose={onClose} />));
    await send((c) => c.url === "/api/v1/assistant");
    await vi.waitFor(() => expect(document.body.textContent).toContain(t("lib-api.manager_offline")));
    expect(onClose).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
    expect(chosenManager()).toBeNull();
    expect(calls.some((c) => c.url.includes("/messages") || c.url.endsWith("/assistant/conversations"))).toBe(false);
  });
});

describe("Hand to AI: a new conversation", () => {
  it("names nothing for an item stamped with the default and nothing picked (one manager)", async () => {
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    pick("Persona（新对话）");
    await send((c) => c.url.endsWith("/assistant/conversations"));
    expect(calls.every((c) => c.header === null)).toBe(true);
    expect(started().body).toEqual({ prompt: expect.any(String) });
    expect(chosenManager()).toBeNull();
    expect(handOffs()).toHaveLength(0);
  });

  it("starts with the item's manager, shows its page and sends the conversation's requests there", async () => {
    act(() => root.render(<HandOffDialog item={item("m-1")} onClose={() => {}} />));
    pick("Persona（新对话）");
    await send((c) => c.url.endsWith("/assistant/conversations"));
    expect(calls[0]).toMatchObject({ url: "/api/v1/inbox/lark:f1:1", header: "m-1" });
    expect(started().header).toBe("m-1");
    expect(chosenManager()).toBe("m-1");
    expect(managerHeaderFor("/assistant/conversations/s-new/feed")).toBe("m-1");
  });

  it("marks the item done by default", async () => {
    await reloadInbox();
    act(() => root.render(<HandOffDialog item={item("assistant")} onClose={() => {}} />));
    pick("Persona（新对话）");
    await send((c) => c.url.endsWith("/assistant/conversations"));
    await vi.waitFor(() => expect(calls.some(filed)).toBe(true));
  });
});
