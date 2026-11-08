// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const transport = vi.hoisted(() => vi.fn<(path: string, init?: unknown) => Promise<unknown>>());
const toast = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string, init?: unknown) => transport(path, init),
}));
vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast,
}));
import { t } from "../../i18n";
import { ItemManager, MANAGER_HEADER } from "../../lib/manager-choice";
import { sendBody, type ComposeDraft } from "../../lib/mail";
import { ComposeDialog } from "./ComposeDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const accounts = [{ id: "test-account", name: "Example account", email: "sender@example.com" }];
const draft: ComposeDraft = { accountID: accounts[0].id, to: "recipient@example.com", cc: "copy@example.com", bcc: "", subject: "Synthetic message", body: "A synthetic mail body." };
const reply: ComposeDraft = { ...draft, subject: "Re: Synthetic message", inReplyTo: "<original@example.com>", references: ["<thread@example.com>", "<original@example.com>"] };
const deferred = () => {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

let host: HTMLDivElement;
let root: Root;
let nextKey = 0;
const closed = vi.fn();
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const form = () => dialog()!.querySelector("form")!;
const body = () => dialog()!.querySelector("textarea")!;
const button = (label: string) => [...dialog()!.querySelectorAll("button")].find((b) => b.textContent === label)!;
const sendButton = () => button(t("inbox-composedialog.send"));
const input = (label: string) => [...dialog()!.querySelectorAll("input")].find((el) => el.getAttribute("aria-label") === label)!;
function edit(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  act(() => {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
type Entry = "button" | "form" | "Ctrl+Enter" | "Cmd+Enter";
const entries: Entry[] = ["button", "form", "Ctrl+Enter", "Cmd+Enter"];
function trigger(entry: Entry) {
  if (entry === "button") sendButton().click();
  else if (entry === "form") form().dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  else body().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: entry === "Ctrl+Enter", metaKey: entry === "Cmd+Enter", bubbles: true, cancelable: true }));
}
function Composer({ initial, draftKey }: { initial: ComposeDraft; draftKey: string }) {
  const [open, setOpen] = useState(true);
  return open && <ItemManager.Provider value="test-manager"><ComposeDialog draftKey={draftKey} initial={initial} accounts={accounts} onClose={() => { closed(); setOpen(false); }} /></ItemManager.Provider>;
}
function render(initial = draft, draftKey = `send-once-${++nextKey}`) {
  act(() => root.render(<Composer key={++nextKey} initial={initial} draftKey={draftKey} />));
  return draftKey;
}

beforeEach(() => {
  transport.mockReset();
  toast.mockClear();
  closed.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe.each([
  ["new mail", draft], ["manual reply", reply],
  ["Bcc-only new mail", { ...draft, to: "", cc: "", bcc: "Private <private@example.com>" }],
  ["Cc-only new mail", { ...draft, to: "", bcc: "" }],
  ["Bcc-only manual reply", { ...reply, to: "", cc: "", bcc: "Private <private@example.com>" }],
  ["Cc-only manual reply", { ...reply, to: "", bcc: "" }],
] as const)("%s send admission", (_name, initial) => {
  it.each(entries)("admits only one request when %s starts a mixed burst before rerender and while pending", async (first) => {
    const pending = deferred();
    transport.mockReturnValue(pending.promise);
    render(initial);
    act(() => {
      trigger(first);
      // All entry paths share this render; a React state flag alone cannot serialize them.
      trigger("Ctrl+Enter");
      trigger("Cmd+Enter");
      trigger("form");
      trigger("button");
    });
    expect(transport).toHaveBeenCalledTimes(1);
    expect(transport).toHaveBeenCalledWith("/mail/send", { method: "POST", json: sendBody(initial), headers: { [MANAGER_HEADER]: "test-manager" } });
    expect(sendButton().disabled).toBe(true);
    expect(closed).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
    for (const entry of entries) act(() => trigger(entry));
    expect(transport).toHaveBeenCalledTimes(1);

    await act(async () => pending.resolve());
    expect(closed).toHaveBeenCalledTimes(1);
    expect(dialog()).toBeNull();
    expect(toast).toHaveBeenCalledExactlyOnceWith(t("inbox-composedialog.sent"), "ok");
  });
});

it.each(entries)("keeps a failed draft and admits one intentional retry through %s", async (retry) => {
  const first = deferred();
  const second = deferred();
  transport.mockReturnValueOnce(first.promise).mockReturnValue(second.promise);
  render();
  edit(body(), "Edited synthetic body to retry.");
  act(() => trigger("Ctrl+Enter"));
  await act(async () => first.reject(new Error("Synthetic transport failure")));
  expect(closed).not.toHaveBeenCalled();
  expect(dialog()!.querySelector('[role="alert"]')!.textContent).toBe(t("inbox-composedialog.send_failed", { error: "Synthetic transport failure" }));
  expect(body().value).toBe("Edited synthetic body to retry.");
  expect(sendButton().disabled).toBe(false);
  act(() => {
    trigger(retry);
    trigger("form");
    trigger("Cmd+Enter");
  });
  expect(transport).toHaveBeenCalledTimes(2);
  expect(transport.mock.calls[1]).toEqual(transport.mock.calls[0]);
  expect(sendButton().disabled).toBe(true);
  await act(async () => second.resolve());
  expect(closed).toHaveBeenCalledTimes(1);
  expect(dialog()).toBeNull();
});

it("does not send again after success while its parent finishes closing", async () => {
  const pending = deferred();
  transport.mockReturnValue(pending.promise);
  act(() => root.render(<ComposeDialog draftKey={`send-once-${++nextKey}`} initial={draft} accounts={accounts} onClose={closed} />));
  act(() => trigger("form"));
  await act(async () => pending.resolve());
  expect(closed).toHaveBeenCalledTimes(1);
  for (const entry of entries) act(() => trigger(entry));
  expect(transport).toHaveBeenCalledTimes(1);
  expect(closed).toHaveBeenCalledTimes(1);
});

it.each([
  ["empty recipients", "", "", "", "inbox-composedialog.need_recipient"],
  ["separator-only recipients", ", ;", "；", "，", "inbox-composedialog.need_recipient"],
  ["invalid To", "invalid", "", "", "inbox-composedialog.bad_addresses"],
  ["invalid Cc", draft.to, "invalid", "", "inbox-composedialog.bad_addresses"],
  ["invalid Bcc with valid To", draft.to, "", "invalid", "inbox-composedialog.bad_addresses"],
  ["invalid Bcc-only", "", "", "invalid", "inbox-composedialog.bad_addresses"],
] as const)("rejects %s without blocking a corrected attempt", async (_name, to, cc, bcc, errorKey) => {
  const pending = deferred();
  transport.mockReturnValue(pending.promise);
  render({ ...draft, to, cc, bcc });
  expect(sendButton().disabled).toBe(errorKey === "inbox-composedialog.need_recipient");
  act(() => trigger("form"));
  expect(transport).not.toHaveBeenCalled();
  expect(dialog()!.querySelector('[role="alert"]')!.textContent).toBe(t(errorKey, { addresses: "invalid" }));
  edit(input(t("inbox-composedialog.to")), draft.to);
  edit(input(t("inbox-composedialog.cc")), "");
  edit(input(t("inbox-composedialog.bcc")), "");
  act(() => trigger("Ctrl+Enter"));
  expect(transport).toHaveBeenCalledTimes(1);
  await act(async () => pending.resolve());
  expect(closed).toHaveBeenCalledTimes(1);
});

it("leaves plain Enter and IME confirmation alone, then accepts a real shortcut", async () => {
  const pending = deferred();
  transport.mockReturnValue(pending.promise);
  render();
  for (const init of [{}, { ctrlKey: true, isComposing: true }, { metaKey: true, isComposing: true }, { ctrlKey: true, keyCode: 229 }, { metaKey: true, keyCode: 229 }]) {
    const event = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...init });
    act(() => { body().dispatchEvent(event); });
    expect(event.defaultPrevented).toBe(false);
  }
  expect(transport).not.toHaveBeenCalled();
  act(() => trigger("Cmd+Enter"));
  expect(transport).toHaveBeenCalledTimes(1);
  await act(async () => pending.resolve());
});

it.each(["cancel", "dismiss"] as const)("preserves explicit %s behavior and draft retention", (action) => {
  const key = render();
  edit(body(), "Unsent edited synthetic body.");
  act(() => {
    if (action === "cancel") button(t("common.cancel")).click();
    else document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  });
  expect(closed).toHaveBeenCalledTimes(1);
  expect(transport).not.toHaveBeenCalled();
  render(draft, key);
  expect(body().value).toBe(action === "cancel" ? draft.body : "Unsent edited synthetic body.");
});
