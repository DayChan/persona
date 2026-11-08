// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

const message = (id: string): ThreadMessage => ({
  id, at: "2026-11-01T09:00:00Z", sender: { name: "Example sender" }, text: `Message ${id}`,
});
const page = (mode: "chat" | "github" = "chat"): ThreadPage => ({
  chat: { id: "demo", name: "Example thread", kind: mode === "chat" ? "group" : "issue", reply: "example_send" },
  messages: [message("1"), message("2")],
});
const item = {
  id: "example:1", source: "example", kind: "group", account: "example", account_name: "Example",
  title: "Example thread", preview: "", count: 1, connector: { thread: "primary.demo" },
} as InboxItem;

let host: HTMLDivElement;
let root: Root;
const flush = async () => { await act(async () => { await Promise.resolve(); }); };
const box = () => host.querySelector("textarea")!;
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!;
const sendButton = () => button(t("inbox-connectorthread.send"));
const cancelQuote = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("inbox-connectorthread.cancel_quote")}"]`);
const type = (text: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), text);
  box().dispatchEvent(new Event("input", { bubbles: true }));
});
const enter = (options: KeyboardEventInit = {}) => box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, ...options }));
const quote = (id: string) => act(() => host.querySelector<HTMLButtonElement>(`[data-mid="${id}"] button`)!.click());
const render = async (next = item, manager?: string) => {
  await act(async () => root.render(<ItemManager value={manager}><ConnectorBody item={next} /></ItemManager>));
};

beforeEach(() => {
  resetManagerChoice();
  transport.load.mockReset().mockResolvedValue(page());
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

describe.each(["chat", "github"] as const)("%s connector sending", (mode) => {
  beforeEach(() => transport.load.mockResolvedValue(page(mode)));

  it("preserves edits made during the send and its refresh, then sends that draft", async () => {
    const pending = deferred<{ ok: boolean }>();
    const refresh = deferred<ThreadPage>();
    transport.reply.mockReturnValueOnce(pending.promise);
    transport.load.mockResolvedValueOnce(page(mode)).mockReturnValueOnce(refresh.promise);
    await render();
    type("  First message  ");
    act(() => enter());
    expect(transport.reply).toHaveBeenCalledExactlyOnceWith("example", {
      thread: "primary.demo", action: "example_send", text: "First message", reply_to: undefined,
    }, undefined);
    type("Second message");
    expect(box().disabled).toBe(false);
    expect(sendButton().disabled).toBe(true);
    pending.resolve({ ok: true });
    await flush();
    expect(transport.load).toHaveBeenCalledTimes(2);
    type("Second message\nwith another line");
    act(() => { enter(); sendButton().click(); });
    expect(transport.reply).toHaveBeenCalledTimes(1);
    const height = box().style.height;
    refresh.resolve(page(mode));
    await flush();
    expect(box().value).toBe("Second message\nwith another line");
    expect(box().style.height).toBe(height);
    expect(sendButton().disabled).toBe(false);
    act(() => sendButton().click());
    await flush();
    expect(transport.reply.mock.calls[1][1].text).toBe("Second message\nwith another line");
    expect(box().value).toBe("");
    expect(box().style.height).toBe("auto");
  });

  it("preserves a draft edited away and back to the submitted text", async () => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    await render();
    type("Same words");
    act(() => enter());
    type("Different words");
    type("Same words");
    pending.resolve({ ok: true });
    await flush();
    expect(box().value).toBe("Same words");
  });

  it("admits only one send from mixed events in one batch and permits a failure retry", async () => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    await render();
    if (mode === "chat") quote("1");
    type("Retry this message");
    act(() => {
      enter();
      sendButton().click();
      enter();
    });
    expect(transport.reply).toHaveBeenCalledTimes(1);
    pending.reject(new Error("Synthetic send failure"));
    await flush();
    expect(box().value).toBe("Retry this message");
    expect(sendButton().disabled).toBe(false);
    expect(!!cancelQuote()).toBe(mode === "chat");
    expect(toast).toHaveBeenCalledTimes(1);
    expect(transport.load).toHaveBeenCalledTimes(1);
    act(() => sendButton().click());
    await flush();
    expect(transport.reply).toHaveBeenCalledTimes(2);
    expect(transport.reply.mock.calls[1]).toEqual(transport.reply.mock.calls[0]);
    expect(box().value).toBe("");
    expect(cancelQuote()).toBeNull();
  });

  it("ignores empty, Shift+Enter and IME submissions", async () => {
    await render();
    type("   ");
    act(() => enter());
    type("Still composing");
    act(() => {
      enter({ shiftKey: true });
      enter({ isComposing: true });
      enter({ keyCode: 229 });
    });
    expect(transport.reply).not.toHaveBeenCalled();
    expect(box().value).toBe("Still composing");
  });

  it("keeps later edits and quotes when a send fails", async () => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    await render();
    type("First message");
    act(() => enter());
    type("Revised message");
    if (mode === "chat") quote("2");
    pending.reject(new Error("Synthetic send failure"));
    await flush();
    expect(box().value).toBe("Revised message");
    expect(!!cancelQuote()).toBe(mode === "chat");
    act(() => sendButton().click());
    await flush();
    expect(transport.reply.mock.calls[1][1]).toMatchObject({
      text: "Revised message", reply_to: mode === "chat" ? "2" : undefined,
    });
    expect(box().value).toBe("");
  });

  it.each([false, true])("treats a successful send as delivered if its refresh fails (edited: %s)", async (edited) => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    transport.load.mockResolvedValueOnce(page(mode)).mockRejectedValueOnce(new Error("Synthetic refresh failure"));
    await render();
    type("Delivered message");
    act(() => enter());
    if (edited) type("Next draft");
    pending.resolve({ ok: true });
    await flush();
    expect(box().value).toBe(edited ? "Next draft" : "");
    expect(transport.reply).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalled();
  });

  it("keeps the same composer when an implicit default manager becomes explicit", async () => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    await render();
    type("First message");
    act(() => enter());
    await render({ ...item, manager: "assistant" });
    expect(box().value).toBe("First message");
    type("Next draft");
    pending.resolve({ ok: true });
    await flush();
    expect(box().value).toBe("Next draft");
  });

  it("does not disturb another manager's composer after a late send failure", async () => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    await render({ ...item, manager: "manager-a" });
    type("First owner's message");
    act(() => enter());
    await render({ ...item, manager: "manager-b" });
    type("Second owner's draft");
    if (mode === "chat") quote("2");
    pending.reject(new Error("Synthetic late failure"));
    await flush();
    expect(box().value).toBe("Second owner's draft");
    expect(!!cancelQuote()).toBe(mode === "chat");
    expect(sendButton().disabled).toBe(false);
  });

  it.each(["item", "thread", "source", "manager", "context"])("isolates the composer when its %s changes during a send", async (change) => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    await render(item, "manager-a");
    if (mode === "chat") quote("1");
    type("First owner's message");
    act(() => enter());
    const next = {
      ...item,
      ...(change === "item" ? { id: "example:2" } : {}),
      ...(change === "thread" ? { connector: { thread: "primary.other" } } : {}),
      ...(change === "source" ? { source: "other" } : {}),
      ...(change === "manager" ? { manager: "manager-b" } : {}),
    };
    await render(next, change === "context" ? "manager-b" : "manager-a");
    expect(box().value).toBe("");
    expect(cancelQuote()).toBeNull();
    type("Second owner's draft");
    if (mode === "chat") quote("2");
    expect(sendButton().disabled).toBe(false);
    pending.resolve({ ok: true });
    await flush();
    expect(box().value).toBe("Second owner's draft");
    expect(!!cancelQuote()).toBe(mode === "chat");
    act(() => sendButton().click());
    await flush();
    expect(transport.reply.mock.calls[0][2]).toBe("manager-a");
    expect(transport.reply.mock.calls[1]).toEqual([next.source, {
      thread: next.connector!.thread, action: "example_send", text: "Second owner's draft", reply_to: mode === "chat" ? "2" : undefined,
    }, change === "manager" || change === "context" ? "manager-b" : "manager-a"]);
  });
});

describe("chat quote edits during sending", () => {
  it.each(["replace", "cancel", "reselect"])("preserves the draft when the user chooses to %s its quote", async (change) => {
    const pending = deferred<{ ok: boolean }>();
    transport.reply.mockReturnValueOnce(pending.promise);
    await render();
    quote("1");
    type("Quoted message");
    act(() => enter());
    if (change === "cancel" || change === "reselect") act(() => cancelQuote()!.click());
    if (change === "replace") quote("2");
    if (change === "reselect") quote("1");
    pending.resolve({ ok: true });
    await flush();
    expect(box().value).toBe("Quoted message");
    expect(!!cancelQuote()).toBe(change !== "cancel");
    act(() => sendButton().click());
    await flush();
    expect(transport.reply.mock.calls[0][1].reply_to).toBe("1");
    expect(transport.reply.mock.calls[1][1].reply_to).toBe(change === "replace" ? "2" : change === "reselect" ? "1" : undefined);
    expect(box().value).toBe("");
    expect(cancelQuote()).toBeNull();
  });
});
