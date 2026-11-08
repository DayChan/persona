// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: vi.fn() }));
vi.mock("../../lib/translate", async (orig) => ({ ...(await orig<typeof import("../../lib/translate")>()), useTranslatePrefs: () => ({ enabled: false, target: "zh-Hans" }) }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { t } from "../../i18n";
import { api } from "../../lib/api";
import type { InboxItem } from "../../lib/inbox";
import { ItemManager, MANAGER_HEADER, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { newDraft, replyDraft, resetMailForTests, sendBody, type ComposeDraft, type MailMessage } from "../../lib/mail";
import { ComposeDialog } from "./ComposeDialog";
import { MailBody } from "./InboxBodies";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const accounts = ["a", "b", "edited"].map((id) => ({ id, email: `${id}@example.com` }));
const message = (owner: string): MailMessage => ({ uid: 1, from: { email: `${owner}@example.com` }, subject: `Topic ${owner}`, text: `Original ${owner}`, message_id: `<${owner}@example.com>`, references: [`<parent-${owner}@example.com>`], seen: true });
const initial = (owner: string) => replyDraft(owner, message(owner));
const edited: ComposeDraft = { ...initial("a"), accountID: "edited", to: "recipient@example.com", cc: "copy@example.com", bcc: "Private <private@example.com>", subject: "Edited subject", body: "Private draft for A" };
const deferred = () => {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

let host: HTMLDivElement;
let root: Root;
let key: string;
let sequence = 0;
let delivery: ReturnType<typeof deferred>;
const mockedApi = vi.mocked(api);

beforeEach(() => {
  key = `reply:mail:shared:thread-${++sequence}:1`;
  resetManagerChoice();
  setRosterDefault("default-owner");
  resetMailForTests();
  delivery = deferred();
  mockedApi.mockReset();
  mockedApi.mockImplementation((path, options) => {
    if (path === "/mail/send") return delivery.promise as ReturnType<typeof api>;
    if (path === "/mail/accounts") return Promise.resolve({ accounts }) as ReturnType<typeof api>;
    if (path.startsWith("/mail/messages/")) {
      const owner = new Headers(options?.headers).get(MANAGER_HEADER) ?? "default-owner";
      return Promise.resolve({ message: message(owner) }) as ReturnType<typeof api>;
    }
    throw new Error(`Unexpected request: ${path}`);
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetMailForTests();
  resetManagerChoice();
});

const field = (name: "from" | "to" | "cc" | "bcc" | "subject" | "body") => document.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(`[aria-label="${t(`inbox-composedialog.${name}`)}"]`)!;
const type = (name: Parameters<typeof field>[0], value: string) => act(() => {
  const element = field(name);
  const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(element, value);
  element.dispatchEvent(new Event(element instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
});
const click = async (text: string) => act(async () => {
  const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
  expect(button).toBeDefined();
  button!.click();
});
const unmount = () => act(() => root.render(null));
const open = (manager: string | undefined, draft = initial(manager ?? "default-owner"), draftKey = key) => act(() => root.render(
  <ItemManager.Provider value={manager}><ComposeDialog draftKey={draftKey} initial={draft} accounts={accounts} onClose={() => root.render(null)} /></ItemManager.Provider>,
));
const showMail = (manager?: string) => act(async () => root.render(
  <ItemManager.Provider value={manager}><MailBody item={{ id: key, manager, source: "mail", read: true, mail: { account_id: manager ?? "default-owner", uids: [1] } } as InboxItem} /></ItemManager.Provider>,
));
const edit = () => {
  type("from", edited.accountID);
  type("to", edited.to);
  type("cc", edited.cc);
  type("bcc", edited.bcc);
  type("subject", edited.subject);
  type("body", edited.body);
};
const expectDraft = (draft: ComposeDraft) => {
  expect(field("from").value).toBe(draft.accountID);
  expect(field("to").value).toBe(draft.to);
  expect(field("cc").value).toBe(draft.cc);
  expect(field("bcc").value).toBe(draft.bcc);
  expect(field("subject").value).toBe(draft.subject);
  expect(field("body").value).toBe(draft.body);
};
const dismiss = () => act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
const expectSent = (draft: ComposeDraft, manager?: string) => expect(mockedApi).toHaveBeenCalledWith("/mail/send", {
  method: "POST", json: sendBody(draft), headers: manager ? { [MANAGER_HEADER]: manager } : undefined,
});

describe("manual mail draft ownership", () => {
  it.each(["dismiss", "unmount"] as const)("retains a Bcc-only edit on %s, scoped to its owner", (action) => {
    open("a");
    type("bcc", edited.bcc);
    if (action === "dismiss") dismiss();
    else unmount();
    open("b");
    expectDraft(initial("b"));
    open("a");
    expectDraft({ ...initial("a"), bcc: edited.bcc });
  });

  it.each(["cancel", "success", "failure"] as const)("handles a retained Bcc-only edit after %s", async (action) => {
    open("a");
    type("bcc", edited.bcc);
    dismiss();
    open("a");
    if (action === "cancel") await click(t("common.cancel"));
    else {
      await click(t("inbox-composedialog.send"));
      expectSent({ ...initial("a"), bcc: edited.bcc }, "a");
      if (action === "success") await act(async () => delivery.resolve());
      else {
        await act(async () => delivery.reject(new Error("Synthetic failure")));
        expect(field("bcc").value).toBe(edited.bcc);
        unmount();
      }
    }
    open("a");
    expectDraft({ ...initial("a"), bcc: action === "failure" ? edited.bcc : "" });
  });

  it("isolates same-id replies across owners while preserving all of the original owner's edits and headers", async () => {
    open("a");
    edit();
    dismiss();
    expect(document.querySelector("textarea")).toBeNull();
    open("b");
    expectDraft(initial("b"));
    await click(t("inbox-composedialog.send"));
    expectSent(initial("b"), "b");
    await act(async () => delivery.resolve());
    open("a");
    expectDraft(edited);
    await click(t("inbox-composedialog.send"));
    expectSent(edited, "a");
  });

  it("keeps separate drafts for two items of the same owner", () => {
    open("a");
    edit();
    open("a", initial("a"), `${key}-other`);
    expectDraft(initial("a"));
    open("a");
    expectDraft(edited);
  });

  it("replaces live form state when its owner changes without a parent remount", () => {
    open("a");
    edit();
    open("b");
    expectDraft(initial("b"));
    open("a");
    expectDraft(edited);
  });

  it("treats an omitted manager and the explicit roster default as the same owner without adding a send header", async () => {
    open(undefined, initial("a"));
    edit();
    unmount();
    open("default-owner", initial("a"));
    expectDraft(edited);
    await click(t("inbox-composedialog.send"));
    expectSent(edited);
    await act(async () => delivery.resolve());
    open(undefined, initial("a"));
    expectDraft(initial("a"));
  });

  it("also isolates new-mail drafts across owners", async () => {
    open("a", newDraft("a"), "new");
    edit();
    unmount();
    open("b", newDraft("b"), "new");
    expectDraft(newDraft("b"));
    await click(t("common.cancel"));
    open("a", newDraft("a"), "new");
    expectDraft(edited);
    await click(t("common.cancel"));
  });

  it.each(["cancel", "success"])("clears only the matching owner after %s", async (action) => {
    open("a");
    edit();
    unmount();
    open("b");
    type("body", "Draft B");
    unmount();
    open("a");
    if (action === "cancel") await click(t("common.cancel"));
    else {
      await click(t("inbox-composedialog.send"));
      expectSent(edited, "a");
      await act(async () => delivery.resolve());
    }
    open("a");
    expectDraft(initial("a"));
    unmount();
    open("b");
    expectDraft({ ...initial("b"), body: "Draft B" });
  });

  it.each([false, true])("retains only the sending owner's draft after failure (unmounted: %s)", async (gone) => {
    open("a");
    edit();
    await click(t("inbox-composedialog.send"));
    expectSent(edited, "a");
    if (gone) {
      unmount();
      open("b");
      type("body", "Draft B");
    }
    await act(async () => delivery.reject(new Error("Synthetic delivery failure")));
    if (gone) expectDraft({ ...initial("b"), body: "Draft B" });
    else {
      expectDraft(edited);
      expect(document.querySelector('[role="alert"]')?.textContent).toContain("Synthetic delivery failure");
    }
    unmount();
    open("a");
    expectDraft(edited);
    delivery = deferred();
    await click(t("inbox-composedialog.send"));
    expectSent(edited, "a");
    expect(mockedApi.mock.calls.filter(([path]) => path === "/mail/send")).toHaveLength(2);
    await act(async () => delivery.resolve());
  });

  it("does not restore a draft after a successful send finishes offscreen", async () => {
    open("a");
    edit();
    await click(t("inbox-composedialog.send"));
    unmount();
    await act(async () => delivery.resolve());
    open("a");
    expectDraft(initial("a"));
  });

  it("retires MailBody's open reply when a same-id item from another manager replaces it", async () => {
    await showMail("a");
    await click(t("inbox-inboxbodies.reply"));
    edit();
    await showMail("b");
    expect(document.querySelector("textarea")).toBeNull();
    await click(t("inbox-inboxbodies.reply"));
    expectDraft(initial("b"));
    await click(t("inbox-composedialog.send"));
    expectSent(initial("b"), "b");
    await act(async () => delivery.resolve());
    await showMail("a");
    await click(t("inbox-inboxbodies.reply"));
    expectDraft(edited);
  });

  it("keeps MailBody's default-owner reply when its implicit manager stamp becomes explicit", async () => {
    await showMail();
    await click(t("inbox-inboxbodies.reply"));
    type("body", "Default owner's reply");
    await showMail("default-owner");
    expect(field("body").value).toBe("Default owner's reply");
    unmount();
    await showMail();
    await click(t("inbox-inboxbodies.reply"));
    expect(field("body").value).toBe("Default owner's reply");
  });
});
