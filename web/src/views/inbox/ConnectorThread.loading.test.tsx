// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/api";
import type { ThreadMessage, ThreadPage } from "../../lib/connectors";
import type { InboxItem } from "../../lib/inbox";
import { ItemManager, resetManagerChoice } from "../../lib/manager-choice";
import { t } from "../../i18n";

const transport = vi.hoisted(() => ({ load: vi.fn(), reply: vi.fn() }));
vi.mock("../../lib/connectors", async (original) => ({
  ...(await original<typeof import("../../lib/connectors")>()),
  loadThread: transport.load,
  replyInThread: transport.reply,
}));
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: vi.fn().mockResolvedValue({ context: "Synthetic fallback content" }),
}));
vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn(),
}));
import { toast } from "../../ui/atoms";
import { ConnectorBody } from "./ConnectorThread";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

const message = (id: string, extra: Partial<ThreadMessage> = {}): ThreadMessage => ({
  id, at: "2026-11-01T09:00:00Z", sender: { name: "Example sender" }, text: `Message ${id}`, ...extra,
});
const page = (ids: string[], before?: string): ThreadPage => ({
  chat: { id: "demo", name: "Example thread", kind: "group", reply: "example_send" },
  messages: ids.map((id) => message(id)), before,
});
const quoted = (): ThreadPage => ({
  ...page(["5", "6"], "cursor-5"),
  messages: [message("5"), message("6", { reply_to: { id: "1", text: "Original message" } })],
});
const item = {
  id: "example:1", source: "example", kind: "group", account: "example", account_name: "Example",
  title: "Example thread", preview: "", count: 1, connector: { thread: "primary.demo" },
} as InboxItem;

let host: HTMLDivElement;
let root: Root;
let reads: ReturnType<typeof deferred<ThreadPage>>[];
const flush = async () => { await act(async () => { await Promise.resolve(); }); };
const render = async (next = item, manager?: string) => {
  await act(async () => root.render(<ItemManager value={manager}><ConnectorBody item={next} /></ItemManager>));
};
const refresh = (n: number) => render({ ...item, updated_at: `2026-11-01T09:00:0${n}Z` });
const resolve = async (n: number, value: ThreadPage) => { reads[n].resolve(value); await flush(); };
const reject = async (n: number, error: Error = new ApiError(429, "Synthetic rate limit", {})) => { reads[n].reject(error); await flush(); };
const ids = () => [...host.querySelectorAll<HTMLElement>("[data-mid]")].map((row) => row.dataset.mid);
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!;
const olderButton = () => button(t("inbox-connectorthread.older"));
const startOlder = () => act(() => olderButton().click());
const startReach = () => act(() => host.querySelector<HTMLButtonElement>("button.border-l-2")!.click());
const type = (text: string) => act(() => {
  const box = host.querySelector("textarea")!;
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, text);
  box.dispatchEvent(new Event("input", { bubbles: true }));
});
const send = () => act(() => button(t("inbox-connectorthread.send")).click());

beforeEach(() => {
  resetManagerChoice();
  reads = [];
  transport.load.mockReset().mockImplementation(() => {
    const pending = deferred<ThreadPage>();
    reads.push(pending);
    return pending.promise;
  });
  transport.reply.mockReset().mockResolvedValue({ ok: true });
  vi.mocked(toast).mockClear();
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("connector newest reads", () => {
  it("keeps the newest response when initial reads resolve in reverse order", async () => {
    await refresh(1);
    await refresh(2);
    await resolve(1, page(["1", "2"]));
    expect(ids()).toEqual(["1", "2"]);
    await resolve(0, page(["1"]));
    expect(ids()).toEqual(["1", "2"]);
  });

  it("ignores an older failure while a newer read is pending", async () => {
    await refresh(1);
    await refresh(2);
    await reject(0);
    expect(host.textContent).not.toContain("Synthetic rate limit");
    expect(host.textContent).not.toContain("Synthetic fallback content");
    await resolve(1, page(["2"]));
    expect(ids()).toEqual(["2"]);
  });

  it("keeps the latest failure until a fresh read recovers", async () => {
    await refresh(1);
    await refresh(2);
    await reject(1);
    expect(host.textContent).toContain("Synthetic rate limit");
    await resolve(0, page(["1"]));
    expect(ids()).toEqual([]);
    expect(host.textContent).toContain("Synthetic rate limit");
    await refresh(3);
    await resolve(2, page(["3"]));
    expect(ids()).toEqual(["3"]);
    expect(host.textContent).not.toContain("Synthetic rate limit");
  });

  it("retains a loaded page across a refresh failure and accepts the next update", async () => {
    await refresh(1);
    await resolve(0, page(["1"]));
    type("Unsent draft");
    await refresh(2);
    await reject(1);
    expect(ids()).toEqual(["1"]);
    expect(host.querySelector("textarea")!.value).toBe("Unsent draft");
    await refresh(3);
    await resolve(2, page(["1", "2"]));
    expect(ids()).toEqual(["1", "2"]);
    expect(host.querySelector("textarea")!.value).toBe("Unsent draft");
  });

  it("fences the retired StrictMode effect even after its replacement is active", async () => {
    await act(async () => root.render(<StrictMode><ConnectorBody item={item} /></StrictMode>));
    expect(reads).toHaveLength(2);
    await resolve(1, page(["1", "2"]));
    await resolve(0, page(["1"]));
    expect(ids()).toEqual(["1", "2"]);
  });
});

describe.each(["item", "source", "thread", "manager", "context"])("connector %s replacement", (change) => {
  const replacement = (): InboxItem => ({
    ...item,
    ...(change === "item" ? { id: "example:2" } : {}),
    ...(change === "source" ? { source: "other" } : {}),
    ...(change === "thread" ? { connector: { thread: "primary.other" } } : {}),
    ...(change === "manager" ? { manager: "manager-b" } : {}),
  });
  const replace = () => render(replacement(), change === "context" ? "manager-b" : "manager-a");

  it("hides the previous page and composer before the new identity loads", async () => {
    await render(item, "manager-a");
    await resolve(0, page(["old"], "old-cursor"));
    type("Previous owner's draft");
    await replace();
    expect(ids()).toEqual([]);
    expect(host.querySelector("textarea")).toBeNull();
    expect(reads).toHaveLength(2);
    await resolve(1, page(["new"]));
    expect(ids()).toEqual(["new"]);
    type("New owner's draft");
    send();
    await flush();
    expect(transport.reply).toHaveBeenCalledWith(replacement().source, {
      thread: replacement().connector!.thread, action: "example_send", text: "New owner's draft", reply_to: undefined,
    }, change === "manager" || change === "context" ? "manager-b" : "manager-a");
  });

  it.each(["success", "failure"])("ignores the previous identity's late %s", async (result) => {
    await render(item, "manager-a");
    await replace();
    await resolve(1, page(["new"]));
    if (result === "success") await resolve(0, page(["old"]));
    else await reject(0);
    expect(ids()).toEqual(["new"]);
    expect(toast).not.toHaveBeenCalled();
  });
});

describe.each(["older", "reach"] as const)("connector %s history", (kind) => {
  const start = () => kind === "older" ? startOlder() : startReach();

  const disjoint = (): ThreadPage => ({
    ...page(["7", "8"], "cursor-7"),
    messages: [message("7"), message("8", { reply_to: { id: "1", text: "Original message" } })],
  });

  it("retries from the replacement window's cursor without skipping the discarded middle", async () => {
    await refresh(1);
    await resolve(0, quoted());
    start();
    await refresh(2);
    await resolve(2, disjoint());
    await resolve(1, page(["1", "2", "3", "4"], "cursor-1"));
    expect(ids()).toEqual(["7", "8"]);
    expect(olderButton().disabled).toBe(false);
    expect(toast).not.toHaveBeenCalled();
    start();
    expect(transport.load.mock.calls[3][2]).toBe("cursor-7");
    await resolve(3, page(["5", "6"], "cursor-5"));
    expect(ids()).toEqual(["5", "6", "7", "8"]);
    if (kind === "older") start();
    expect(transport.load.mock.calls[4][2]).toBe("cursor-5");
    await resolve(4, page(["1", "2", "3", "4"], "cursor-1"));
    expect(ids()).toEqual(["1", "2", "3", "4", "5", "6", "7", "8"]);
    expect(olderButton().disabled).toBe(false);
    if (kind === "reach") expect(host.querySelector('[data-mid="1"]')!.className).toContain("bg-accent-tint");
  });

  it.each(["success", "failure"])("retires the discarded window before a stale %s and preserves a retry's busy state", async (result) => {
    await refresh(1);
    await resolve(0, quoted());
    start();
    await refresh(2);
    await resolve(2, disjoint());
    expect(olderButton().disabled).toBe(false);
    start();
    expect(transport.load.mock.calls[3][2]).toBe("cursor-7");
    expect(olderButton().disabled).toBe(true);
    if (result === "success") await resolve(1, page(["1", "2", "3", "4"], "cursor-1"));
    else await reject(1, new Error("Synthetic discarded-window failure"));
    expect(ids()).toEqual(["7", "8"]);
    expect(olderButton().disabled).toBe(true);
    expect(reads).toHaveLength(4);
    expect(toast).not.toHaveBeenCalled();
    await resolve(3, page(["1", "2", "3", "4", "5", "6"]));
    expect(ids()).toEqual(["1", "2", "3", "4", "5", "6", "7", "8"]);
    expect(host.textContent).not.toContain(t("inbox-connectorthread.older"));
  });

  it("retires history even when replacement and history responses settle in one React batch", async () => {
    await refresh(1);
    await resolve(0, quoted());
    start();
    await refresh(2);
    await act(async () => {
      reads[2].resolve(disjoint());
      reads[1].resolve(page(["1", "2", "3", "4"], "cursor-1"));
    });
    expect(ids()).toEqual(["7", "8"]);
    expect(olderButton().disabled).toBe(false);
    expect(toast).not.toHaveBeenCalled();
    startOlder();
    expect(transport.load.mock.calls[3][2]).toBe("cursor-7");
  });

  if (kind === "reach") it("retires a second history page after a disjoint refresh during a quote search", async () => {
    await refresh(1);
    await resolve(0, quoted());
    startReach();
    await resolve(1, page(["3", "4"], "cursor-3"));
    expect(transport.load.mock.calls[2][2]).toBe("cursor-3");
    await refresh(2);
    await resolve(3, disjoint());
    expect(olderButton().disabled).toBe(false);
    await resolve(2, page(["1", "2"], "cursor-1"));
    expect(ids()).toEqual(["7", "8"]);
    expect(reads).toHaveLength(4);
    expect(toast).not.toHaveBeenCalled();
    startReach();
    expect(transport.load.mock.calls[4][2]).toBe("cursor-7");
  });

  it.each([false, true])("preserves history and a concurrent newest refresh (history first: %s)", async (historyFirst) => {
    await refresh(1);
    await resolve(0, quoted());
    start();
    await refresh(2);
    const latest = { ...quoted(), messages: [message("5", { text: "Edited latest" }), ...quoted().messages.slice(1), message("7")] };
    if (historyFirst) {
      await resolve(1, page(["1", "2"], "cursor-1"));
      await resolve(2, latest);
    } else {
      await resolve(2, latest);
      await resolve(1, page(["1", "2"], "cursor-1"));
    }
    expect(ids()).toEqual(["1", "2", "5", "6", "7"]);
    expect(host.textContent).toContain("Edited latest");
    expect(olderButton().disabled).toBe(false);
    startOlder();
    expect(transport.load.mock.calls[3]).toEqual(["example", "primary.demo", "cursor-1", undefined]);
    if (kind === "reach") expect(host.querySelector('[data-mid="1"]')!.className).toContain("bg-accent-tint");
  });

  it("admits one history reader synchronously across older and quote controls", async () => {
    await render();
    await resolve(0, quoted());
    act(() => {
      if (kind === "older") olderButton().click();
      else host.querySelector<HTMLButtonElement>("button.border-l-2")!.click();
      olderButton().click();
      host.querySelector<HTMLButtonElement>("button.border-l-2")!.click();
    });
    expect(reads).toHaveLength(2);
    await resolve(1, page(["1"]));
    expect(ids()).toEqual(["1", "5", "6"]);
    expect(toast).not.toHaveBeenCalled();
  });

  it("reports a current failure once and allows retry with the same cursor", async () => {
    await render();
    await resolve(0, quoted());
    start();
    await reject(1, new Error("Synthetic history failure"));
    expect(toast).toHaveBeenCalledExactlyOnceWith(t("inbox-connectorthread.older_failed", { error: "Synthetic history failure" }), "error");
    expect(olderButton().disabled).toBe(false);
    start();
    expect(transport.load.mock.calls[2][2]).toBe("cursor-5");
    await resolve(2, page(["1"]));
    expect(ids()).toEqual(["1", "5", "6"]);
  });

  it.each(["success", "failure"])("retires history %s without touching the new owner's history reader", async (result) => {
    await render({ ...item, manager: "manager-a" });
    await resolve(0, quoted());
    start();
    await render({ ...item, manager: "manager-b" });
    await resolve(2, quoted());
    startOlder();
    if (result === "success") await resolve(1, page(["old-owner"], "old-cursor"));
    else await reject(1);
    expect(ids()).toEqual(["5", "6"]);
    expect(olderButton().disabled).toBe(true);
    expect(reads).toHaveLength(4);
    expect(toast).not.toHaveBeenCalled();
    await resolve(3, page(["new-owner"]));
    expect(ids()).toEqual(["new-owner", "5", "6"]);
  });

  it.each(["success", "failure"])("does not continue or toast after unmount and late %s", async (result) => {
    await render();
    await resolve(0, quoted());
    start();
    act(() => root.render(null));
    if (result === "success") await resolve(1, page(["3"], "cursor-3"));
    else await reject(1);
    expect(reads).toHaveLength(2);
    expect(toast).not.toHaveBeenCalled();
  });
});

it("reads multiple quote pages, keeps newest edits, and stops at the found message", async () => {
  await refresh(1);
  await resolve(0, quoted());
  startReach();
  await resolve(1, page(["3", "4"], "cursor-3"));
  expect(transport.load.mock.calls[2][2]).toBe("cursor-3");
  await refresh(2);
  await resolve(3, { ...quoted(), messages: [...quoted().messages, message("7")] });
  await resolve(2, page(["1", "2"]));
  expect(ids()).toEqual(["1", "2", "3", "4", "5", "6", "7"]);
  expect(host.querySelector('[data-mid="1"]')!.className).toContain("bg-accent-tint");
  expect(toast).not.toHaveBeenCalled();
});

it("does not revive a retired request when returning to the same identity", async () => {
  await render(item);
  await render({ ...item, manager: "manager-b" });
  await render(item);
  await resolve(2, page(["current"]));
  await resolve(0, page(["retired"]));
  await reject(1);
  expect(ids()).toEqual(["current"]);
  expect(toast).not.toHaveBeenCalled();
});

it("does not restore a message removed by refresh from the quote jump's starting snapshot", async () => {
  await refresh(1);
  await resolve(0, quoted());
  startReach();
  await refresh(2);
  await resolve(2, page(["5", "7"], "cursor-5"));
  await resolve(1, page(["1", "2"]));
  expect(ids()).toEqual(["1", "2", "5", "7"]);
  expect(toast).not.toHaveBeenCalled();
});

it("keeps a successful-send refresh when an older read finishes afterwards", async () => {
  await refresh(1);
  await resolve(0, page(["1"]));
  await refresh(2);
  type("Delivered message");
  send();
  await flush();
  await resolve(2, page(["1", "2"]));
  expect(host.querySelector("textarea")!.value).toBe("");
  await resolve(1, page(["1"]));
  expect(ids()).toEqual(["1", "2"]);
  expect(transport.reply).toHaveBeenCalledTimes(1);
});

it.each(["success", "failure"])("does not refresh or notify for a retired owner's send %s", async (result) => {
  const pending = deferred<unknown>();
  transport.reply.mockReturnValueOnce(pending.promise);
  await render(item);
  await resolve(0, page(["1"]));
  type("First owner's message");
  send();
  await render({ ...item, manager: "manager-b" });
  await resolve(1, page(["new"]));
  type("New owner's draft");
  if (result === "success") pending.resolve({ ok: true });
  else pending.reject(new Error("Synthetic retired send failure"));
  await flush();
  expect(reads).toHaveLength(2);
  expect(ids()).toEqual(["new"]);
  expect(host.querySelector("textarea")!.value).toBe("New owner's draft");
  expect(toast).not.toHaveBeenCalled();
});
