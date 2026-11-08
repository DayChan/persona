// @vitest-environment jsdom
import { act, StrictMode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({ matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const request = vi.hoisted(() => vi.fn<(path: string, init?: unknown) => Promise<unknown>>());
vi.mock("../../lib/api", async (original) => ({
  ...await original<typeof import("../../lib/api")>(),
  api: request,
}));
vi.mock("../../ui/atoms", async (original) => ({
  ...await original<typeof import("../../ui/atoms")>(),
  toast: vi.fn(),
}));

import { t } from "../../i18n";
import { getInbox, itemKey, itemPath, itemRequest, resetInboxForTests, unsentReplies, type InboxItem } from "../../lib/inbox";
import { DraftDialog } from "./InboxDialogs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (patch: Partial<InboxItem> = {}): InboxItem => ({
  id: "mail:demo:thread:1", manager: "owner-a", source: "mail", kind: "mail", account: "demo", account_name: "Example mail",
  title: "Example thread", preview: "Synthetic message", count: 1,
  agent: { state: "drafted", draft: "Generated reply" }, ...patch,
});

function deferred() {
  let resolve!: (value: unknown) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<unknown>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function Page({ item }: { item: InboxItem }) {
  const [open, setOpen] = useState(true);
  return <>
    <button onClick={() => setOpen(true)}>Open draft</button>
    {open && <DraftDialog item={item} onClose={() => setOpen(false)} />}
  </>;
}

let host: HTMLDivElement;
let root: Root;
const area = () => document.querySelector<HTMLTextAreaElement>("[role=dialog] textarea")!;
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent === label || b.getAttribute("aria-label") === label)!;
const click = async (label: string) => { await act(async () => button(label).click()); };
const open = async (it = item(), strict = false) => {
  await act(async () => root.render(strict ? <StrictMode><Page item={it} /></StrictMode> : <Page item={it} />));
};
const type = (text: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area(), text);
  area().dispatchEvent(new Event("input", { bubbles: true }));
});
const dismiss = async () => {
  await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(area()).toBeNull();
};

beforeEach(() => {
  resetInboxForTests();
  request.mockReset().mockImplementation(async (path) => { throw new Error(`Unexpected request: ${path}`); });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetInboxForTests();
});

describe("AI reply drafts", () => {
  it.each(["Escape", "backdrop", "close button"])("retains exact edited text after %s and reopen", async (how) => {
    await open();
    const edited = "  My edited reply\nwith a second line.  ";
    type(edited);
    expect(area().value).toBe(edited);
    if (how === "Escape") await dismiss();
    else if (how === "close button") await click(t("ui-atoms.close"));
    else await act(async () => document.querySelector("[role=dialog]")!.parentElement!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    expect(area()).toBeNull();
    await click("Open draft");
    expect(area().value).toBe(edited);
    expect(request).not.toHaveBeenCalled();
  });

  it("retains edits when navigation unmounts the page without onClose", async () => {
    await open();
    type("My navigation draft");
    await act(async () => root.render(null));
    await open(item({ agent: { state: "drafted", draft: "New AI suggestion" } }));
    expect(area().value).toBe("My navigation draft");
    expect(request).not.toHaveBeenCalled();
  });

  it("retains an intentionally emptied reply rather than restoring or generating text", async () => {
    await open();
    type("");
    await dismiss();
    await click("Open draft");
    expect(area().value).toBe("");
    expect(button(t("inbox-inboxdialogs.send")).disabled).toBe(true);
    expect(request).not.toHaveBeenCalled();
  });

  it("Cancel explicitly discards edits while an untouched dismissal keeps the AI fallback", async () => {
    await open();
    await dismiss();
    expect(unsentReplies.has(itemKey(item()))).toBe(false);
    await click("Open draft");
    type("Discard this edit");
    await click(t("common.cancel"));
    await click("Open draft");
    expect(area().value).toBe("Generated reply");
    expect(unsentReplies.has(itemKey(item()))).toBe(false);
  });

  it.each([
    { manager: "owner-b" },
    { manager: undefined },
    { id: "mail:demo:other-thread:2" },
  ])("isolates drafts across an item/owner change: %j", async (patch) => {
    const first = item();
    const second = item({ ...patch, agent: { state: "drafted", draft: "Other generated reply" } });
    await open(first);
    type("First owner's edit");
    await open(second);
    expect(area().value).toBe("Other generated reply");
    type("Second item's edit");
    await open(first);
    expect(area().value).toBe("First owner's edit");
    await open(second);
    expect(area().value).toBe("Second item's edit");
    expect(request).not.toHaveBeenCalled();
  });

  it("generates when there is no saved or AI draft, then keeps the accepted text", async () => {
    const it = item({ agent: undefined });
    request.mockResolvedValue({ text: "Fresh generated reply" });
    await open(it);
    expect(request).toHaveBeenCalledExactlyOnceWith(`${itemPath(it.id)}/draft`, { method: "POST", json: { instruction: "" }, headers: itemRequest(it) });
    expect(area().value).toBe("Fresh generated reply");
    await dismiss();
    await click("Open draft");
    expect(area().value).toBe("Fresh generated reply");
    expect(request).toHaveBeenCalledOnce();
  });

  it("allows editing after generation fails and restores those edits", async () => {
    request.mockRejectedValue(new Error("Synthetic drafting failure"));
    await open(item({ agent: undefined }));
    expect(document.querySelector("[role=dialog]")!.textContent).toContain("Synthetic drafting failure");
    expect(area().disabled).toBe(false);
    type("Manual reply after failure");
    await dismiss();
    await click("Open draft");
    expect(area().value).toBe("Manual reply after failure");
    expect(request).toHaveBeenCalledOnce();
  });

  it("keeps edits on rewrite failure and accepts an explicit successful rewrite", async () => {
    await open();
    type("My reply before rewriting");
    request.mockRejectedValueOnce(new Error("Synthetic rewrite failure"));
    await click(t("inbox-inboxdialogs.rewrite"));
    expect(area().value).toBe("My reply before rewriting");
    await dismiss();
    await click("Open draft");
    expect(area().value).toBe("My reply before rewriting");
    request.mockResolvedValueOnce({ text: "Accepted rewrite" });
    await click(t("inbox-inboxdialogs.rewrite"));
    expect(area().value).toBe("Accepted rewrite");
    await dismiss();
    await click("Open draft");
    expect(area().value).toBe("Accepted rewrite");
  });

  it("ignores a rewrite that finishes after dismissal and reopening", async () => {
    const pending = deferred();
    await open();
    type("Retained manual reply");
    request.mockReturnValueOnce(pending.promise);
    await click(t("inbox-inboxdialogs.rewrite"));
    expect(area().disabled).toBe(true);
    await dismiss();
    await click("Open draft");
    expect(area().value).toBe("Retained manual reply");
    type("Newer manual reply");
    await act(async () => pending.resolve({ text: "Late generated reply" }));
    expect(area().value).toBe("Newer manual reply");
    await dismiss();
    await click("Open draft");
    expect(area().value).toBe("Newer manual reply");
  });

  it("ignores the first StrictMode generation after the active response and manual edits", async () => {
    const stale = deferred();
    const active = deferred();
    request.mockReturnValueOnce(stale.promise).mockReturnValueOnce(active.promise);
    await open(item({ agent: undefined }), true);
    expect(request).toHaveBeenCalledTimes(2);
    await act(async () => active.resolve({ text: "Active generation" }));
    type("Manual reply after generation");
    await act(async () => stale.resolve({ text: "Stale generation" }));
    expect(area().value).toBe("Manual reply after generation");
  });

  it.each([true, false])("clears on send and restores only failed delivery (success=%s)", async (success) => {
    const pending = deferred();
    const it = item();
    await open(it);
    type("  Edited reply to send  ");
    request.mockReturnValueOnce(pending.promise);
    await click(t("inbox-inboxdialogs.send"));
    expect(area()).toBeNull();
    expect(unsentReplies.has(itemKey(it))).toBe(false);
    expect(getInbox().sending).toContain(itemKey(it));
    expect(request).toHaveBeenCalledExactlyOnceWith(`${itemPath(it.id)}/reply`, { method: "POST", json: { text: "Edited reply to send", changes: undefined }, headers: itemRequest(it) });
    await act(async () => success ? pending.resolve(it) : pending.reject(new Error("Synthetic delivery failure")));
    expect(getInbox().sending).toEqual([]);
    expect(unsentReplies.has(itemKey(it))).toBe(!success);
    await click("Open draft");
    expect(area().value).toBe(success ? "Generated reply" : "Edited reply to send");
    expect(request).toHaveBeenCalledOnce();
  });
});
