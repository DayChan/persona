// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearFeedCache, feedCacheScope, readFeedCache, readMemoryPage, writeFeedCache, type FeedCache } from "../../lib/feed-cache";
import { getState, type listenSession, type ItemsEvent } from "../../lib/store";
import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";
import type { Item, ItemsPage } from "../../lib/types";
import { useTranscript, type Transcript } from "./useTranscript";

interface Pending {
  path: string;
  resolve: (page: ItemsPage) => void;
  reject: (reason: Error) => void;
}
const calls: Pending[] = [];
type Listener = Parameters<typeof listenSession>[2];
let listener: Listener | undefined;
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string) => new Promise((resolve, reject) => calls.push({ path, resolve, reject })),
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  listenSession: (_mid: string, _sid: string, next: Listener) => {
    listener = next;
    return () => { listener = undefined; };
  },
  send: () => {},
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let shown: Transcript;
let renders: { loading: boolean; ids: string[] }[];
let scope: string;
const item = (id: string, seq: number, text = id, role: Item["role"] = "assistant"): Item => ({ id, seq, role, kind: "text", text });
const saved = (patch: Partial<FeedCache> = {}): FeedCache => ({
  machineID: "m", sessionID: "main", items: [item("question", 10, "Question", "user"), item("answer", 11, "Cached answer")], hasMore: true, history: "v1", ...patch,
});
function Probe({ sid = "main", main = true, manager = "" }: { sid?: string; main?: boolean; manager?: string }) {
  shown = useTranscript("m", sid, { cache: main ? { scope, manager } : undefined });
  renders.push({ loading: shown.loading, ids: shown.items.map((row) => row.id) });
  return <div>{shown.items.map((row) => row.text).join("|")}</div>;
}
const render = (props: Parameters<typeof Probe>[0] = {}) => act(() => root.render(<Probe {...props} />));
const answer = (page: ItemsPage, index = 0) => act(async () => calls.splice(index, 1)[0].resolve(page));
const fail = () => act(async () => calls.shift()!.reject(new Error("offline")));
const push = (page: Partial<ItemsEvent>) => act(() => listener!.items!(page as ItemsEvent));
const ids = () => shown.items.map((row) => row.id);

beforeEach(() => {
  resetManagerChoice();
  Object.assign(getState(), { assistant: null });
  localStorage.clear();
  clearFeedCache();
  localStorage.setItem("persona.token", "test-credential");
  scope = feedCacheScope()!;
  calls.length = 0;
  renders = [];
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root.unmount());
  localStorage.clear();
  Object.assign(getState(), { assistant: null });
  resetManagerChoice();
});

describe("main conversation first-screen cache", () => {
  it("renders cached items synchronously while the latest page is pending", () => {
    writeFeedCache(scope, saved());
    render();
    expect(renders[0]).toEqual({ ids: ["question", "answer"], loading: false });
    expect(ids()).toEqual(["question", "answer"]);
    expect(shown.loading).toBe(false);
    expect(shown.hasMore).toBe(true);
    expect(calls).toHaveLength(1);
  });

  it("keeps cached content and storage after failure and while retrying", async () => {
    writeFeedCache(scope, saved());
    render();
    const before = readFeedCache(scope);
    await fail();
    expect(shown.error).toContain("offline");
    expect(shown.items[1].text).toBe("Cached answer");
    expect(readFeedCache(scope)).toEqual(before);
    act(() => shown.reload());
    expect(shown.loading).toBe(false);
    expect(shown.items[1].text).toBe("Cached answer");
    await answer({ items: [item("answer", 11, "Updated answer")], history: "v1", has_more: true });
    expect(shown.error).toBe("");
    expect(shown.items[1].text).toBe("Updated answer");
  });

  it("merges by id and keeps a newer push over an older HTTP answer", async () => {
    writeFeedCache(scope, saved());
    render();
    push({ items: [item("answer", 11, "Pushed answer"), item("live", 12)], history: "v1", to_seq: 12 });
    await answer({ items: [item("question", 10, "Updated question", "user"), item("answer", 11, "HTTP answer")], history: "v1", has_more: true, to_seq: 11 });
    expect(ids()).toEqual(["question", "answer", "live"]);
    expect(shown.items[0].text).toBe("Updated question");
    expect(shown.items[1].text).toBe("Pushed answer");
    expect(readFeedCache(scope)?.items.map((row) => row.text)).toEqual(["Updated question", "Pushed answer", "live"]);
  });

  it.each([
    { items: [item("rewritten", 1)], history: "v2", has_more: false },
    { items: [], history: "v1", has_more: false },
  ])("replaces stale cached history with an authoritative page: %j", async (page) => {
    writeFeedCache(scope, saved());
    render();
    await answer(page);
    expect(shown.items).toEqual(page.items);
    expect(readFeedCache(scope)?.items).toEqual(page.items);
  });

  it("does not persist older pages or expanded folds", async () => {
    const fold: Item = { id: "fold", seq: 11, role: "assistant", kind: "fold", fold: { through_seq: 15, items: 4, tools: 4, thoughts: 0 } };
    render();
    await answer({ items: [item("question", 10, "Question", "user"), fold, item("answer", 16)], has_more: true, history: "v1" });
    const before = readFeedCache(scope);
    act(() => { void shown.loadEarlier(); });
    await answer({ items: [item("older", 1, "Older question", "user")], has_more: false });
    act(() => { void shown.expandFold(fold); });
    await answer({ items: [{ ...item("tool", 12), kind: "tool_use", tool_name: "Read" }] });
    expect(ids()).toEqual(["older", "question", "tool", "answer"]);
    expect(readFeedCache(scope)).toEqual(before);
    push({ items: [item("later", 17)], history: "v1", to_seq: 17 });
    expect(readFeedCache(scope)?.items.map((row) => row.id)).toEqual(["question", "fold", "answer", "later"]);
  });

  it("does not hydrate or write a non-main conversation", async () => {
    writeFeedCache(scope, saved());
    const before = readFeedCache(scope);
    render({ main: false });
    expect(shown.items).toEqual([]);
    expect(shown.loading).toBe(true);
    await answer({ items: [item("other", 1)] });
    expect(readFeedCache(scope)).toEqual(before);
  });

  it("does not hydrate another session or manager", () => {
    writeFeedCache(scope, saved());
    render({ sid: "other" });
    expect(shown.items).toEqual([]);
    render({ sid: "main", manager: "other-manager" });
    expect(shown.items).toEqual([]);
  });

  it("drops a pending old-account response after the auth scope changes", async () => {
    writeFeedCache(scope, saved());
    render();
    clearFeedCache();
    localStorage.setItem("persona.token", "another-test-credential");
    scope = feedCacheScope()!;
    render();
    await answer({ items: [item("private-old", 1)] });
    expect(shown.items).toEqual([]);
    expect(readFeedCache(scope)).toBeNull();
    await answer({ items: [item("new-account", 1)] });
    expect(readFeedCache(scope)?.items.map((row) => row.id)).toEqual(["new-account"]);
  });

  it("does not recreate the old main cache after the store accepts a different main", async () => {
    writeFeedCache(scope, saved());
    render();
    Object.assign(getState(), { assistant: { main: "new-main", machine_id: "m", conversations: [] } });
    // The store discards this cache before notifying React; the old request can finish in between.
    const { discardFeedCache } = await import("../../lib/feed-cache");
    discardFeedCache(scope);
    await answer({ items: saved().items, history: "v1" });
    expect(readFeedCache(scope)).toBeNull();
  });

  it("does not write the old manager's feed after a manager switch", async () => {
    writeFeedCache(scope, saved());
    render();
    chooseManager("another-manager");
    const before = readFeedCache(scope);
    await answer({ items: [item("old-manager", 20)], history: "v1" });
    expect(readFeedCache(scope)).toEqual(before);
  });

  it("ignores a stale HTTP response that resolves during a rewrite push", async () => {
    writeFeedCache(scope, saved());
    render();
    await act(async () => {
      listener!.items!({ machine_id: "m", session_id: "main", items: [], history: "v2" });
      calls.shift()!.resolve({ items: saved().items, history: "v1" });
    });
    expect(shown.items).toEqual([]);
    expect(readFeedCache(scope)).toBeNull();
    await answer({ items: [item("new-history", 1)], history: "v2" });
    expect(ids()).toEqual(["new-history"]);
  });

  it("discards rewritten history before reloading, including when that reload fails", async () => {
    writeFeedCache(scope, saved());
    render();
    await answer({ items: saved().items, history: "v1", has_more: true });
    push({ items: [item("new-history", 1)], history: "v2" });
    expect(shown.items).toEqual([]);
    expect(readFeedCache(scope)).toBeNull();
    await fail();
    expect(shown.items).toEqual([]);
  });
});

describe("other chats' memory cache", () => {
  const page = { items: [item("question", 10, "Question", "user"), item("answer", 11, "Answer")], has_more: true, history: "v1", to_seq: 11 };
  const reopen = () => {
    act(() => root.unmount());
    root = createRoot(document.createElement("div"));
    renders = [];
    render({ sid: "agent", main: false });
  };
  const readOnce = async () => {
    render({ sid: "agent", main: false });
    await answer(page);
  };

  it("shows a chat read before at once, then refreshes it in the background", async () => {
    await readOnce();
    reopen();
    expect(renders[0]).toEqual({ ids: ["question", "answer"], loading: false });
    expect(shown.hasMore).toBe(true);
    expect(calls).toHaveLength(1);
    await answer({ ...page, items: [item("answer", 11, "Fresh answer"), item("next", 12)], to_seq: 12 });
    expect(ids()).toEqual(["question", "answer", "next"]);
    expect(shown.items[1].text).toBe("Fresh answer");
    expect(readMemoryPage(scope, "m", "agent")?.items.map((row) => row.id)).toEqual(["question", "answer", "next"]);
    expect(readFeedCache(scope)).toBeNull();
  });

  it("keeps the page through a failed read", async () => {
    await readOnce();
    reopen();
    await fail();
    expect(ids()).toEqual(["question", "answer"]);
    expect(shown.loading).toBe(false);
    expect(shown.error).toContain("offline");
  });

  it("merges live pushes over the page and remembers them", async () => {
    await readOnce();
    push({ items: [item("live", 12)], history: "v1", to_seq: 12 });
    reopen();
    expect(ids()).toEqual(["question", "answer", "live"]);
  });

  it("does not show a page across a hole in the transcript", async () => {
    await readOnce();
    reopen();
    await answer({ items: [item("q2", 40, "Later question", "user"), item("a2", 41)], has_more: true, history: "v1", to_seq: 41 });
    expect(ids()).toEqual(["q2", "a2"]);
    expect(shown.hasMore).toBe(true);
  });

  it("forgets a page whose transcript was rewritten", async () => {
    await readOnce();
    push({ items: [], history: "v2" });
    expect(readMemoryPage(scope, "m", "agent")).toBeNull();
  });
});

describe("recovering from a failed first read", () => {
  it("takes has_more from the replay, so older history can still load", async () => {
    render({ sid: "agent", main: false });
    await fail();
    expect(shown.hasMore).toBe(false);
    push({ items: [item("question", 10, "Question", "user")], history: "v1", to_seq: 10, backlog: true, has_more: true });
    expect(shown.hasMore).toBe(true);
    expect(shown.error).toBe("");
    act(() => { void shown.loadEarlier(); });
    expect(calls[0].path).toContain("before_seq=10");
  });

  it("does not join a cached page to a replay that starts after it", async () => {
    writeFeedCache(scope, saved());
    render();
    await fail();
    push({ items: [item("later", 30)], history: "v1", to_seq: 30, backlog: true, has_more: true });
    expect(ids()).toEqual(["later"]);
  });
});
