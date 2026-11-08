// @vitest-environment jsdom
// Exercise the real reader, composer and HTTP serializer; only the synthetic transport is intercepted.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { InboxItem, MailAccount } from "../../lib/inbox";
import { resetMailForTests, type MailMessage } from "../../lib/mail";
import { ItemManager, MANAGER_HEADER, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { MailBody } from "./InboxBodies";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
const message = (uid = 1): MailMessage => ({
  uid, subject: "Project review", text: `Synthetic original ${uid}`, seen: true,
  from: { name: "Sender, Example", email: "sender@example.com" },
  to: [{ email: "ME@example.com" }, { name: "Team; Example", email: "team@example.com" }, { email: "SENDER@example.com" }],
  cc: [{ email: "TEAM@example.com" }, { email: "copy@example.com" }, { email: "me@example.com" }],
  message_id: `<message-${uid}@example.com>`, references: ["<parent@example.com>"],
});
let host: HTMLDivElement;
let root: Root;
let sequence = 0;
let thread: string;
let accountReads: Map<string, ReturnType<typeof deferred<MailAccount[]>>>;
let messages: Map<number, ReturnType<typeof deferred<MailMessage>>>;
let delivery: ReturnType<typeof deferred<Response>>;
let sends: { headers: Headers; body: Record<string, unknown> }[];
const field = (name: "from" | "to" | "cc" | "subject" | "body") => document.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(`[aria-label="${t(`inbox-composedialog.${name}`)}"]`)!;
const button = (key: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === t(key));
const click = (key: string) => act(() => {
  expect(button(key), `Missing action ${key}`).toBeDefined();
  button(key)!.click();
});
const edit = (name: Parameters<typeof field>[0], value: string) => act(() => {
  const element = field(name);
  const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(element, value);
  element.dispatchEvent(new Event(element instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
});
const dismiss = () => act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
const show = (manager = "alpha", uid = 1, id = thread) => act(async () => root.render(
  <ItemManager.Provider value={manager}><MailBody item={{ id, manager, source: "mail", read: true, mail: { account_id: "shared", uids: [uid] } } as InboxItem} /></ItemManager.Provider>,
));
const closePage = () => act(() => root.render(null));
const burst = () => act(() => {
  click("inbox-composedialog.send");
  document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  for (const key of ["ctrlKey", "metaKey"]) field("body").dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", [key]: true, bubbles: true, cancelable: true }));
});

beforeEach(() => {
  thread = `reply-all-${++sequence}`;
  resetManagerChoice();
  setRosterDefault("primary");
  resetMailForTests();
  accountReads = new Map();
  messages = new Map();
  delivery = deferred<Response>();
  sends = [];
  vi.stubGlobal("fetch", vi.fn(async (input: string, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    const manager = headers.get(MANAGER_HEADER) ?? "primary";
    if (input === "/api/v1/mail/accounts") return json({ accounts: accountReads.has(manager) ? await accountReads.get(manager)!.promise : [{ id: "shared", email: "me@example.com" }] });
    if (input === "/api/v1/mail/send") {
      sends.push({ headers, body: JSON.parse(init!.body as string) });
      return delivery.promise;
    }
    const uid = /\/mail\/messages\/shared\/(\d+)\?/.exec(input)?.[1];
    if (uid) return json({ message: messages.has(+uid) ? await messages.get(+uid)!.promise : message(+uid) });
    throw new Error(`Unexpected synthetic request: ${input}`);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  resetMailForTests();
  resetManagerChoice();
});

describe("Reply all through the reader and composer", () => {
  it("sends every visible participant once with the receiving owner and original thread metadata", async () => {
    await show();
    click("inbox-inboxbodies.reply_all");
    expect(field("to").value).toBe("sender@example.com, team@example.com");
    expect(field("cc").value).toBe("copy@example.com");
    expect(field("body").value).toContain("> Synthetic original 1");
    edit("body", `A synthetic answer${field("body").value}`);
    click("inbox-composedialog.send");
    expect(sends).toHaveLength(1);
    expect(sends[0].headers.get(MANAGER_HEADER)).toBe("alpha");
    expect(sends[0].headers.get("Content-Type")).toBe("application/json");
    expect(sends[0].body).toMatchObject({
      account_id: "shared", to: [{ email: "sender@example.com" }, { email: "team@example.com" }], cc: [{ email: "copy@example.com" }],
      subject: "Re: Project review", text: expect.stringContaining("A synthetic answer"),
      in_reply_to: "<message-1@example.com>", references: "<parent@example.com> <message-1@example.com>",
    });
    await act(async () => delivery.resolve(json({})));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("keeps ordinary Reply available while receiving accounts load, with sender-only recipients", async () => {
    accountReads.set("alpha", deferred());
    await show();
    expect(button("inbox-inboxbodies.reply_all")?.disabled).toBe(true);
    click("inbox-inboxbodies.reply_all");
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    click("inbox-inboxbodies.reply");
    expect(field("to").value).toBe("sender@example.com");
    expect(field("cc").value).toBe("");
    edit("body", "Ordinary synthetic reply");
    await act(async () => accountReads.get("alpha")!.resolve([{ id: "shared", email: "me@example.com" }]));
    expect(field("to").value).toBe("sender@example.com");
    expect(field("cc").value).toBe("");
    burst();
    expect(sends).toHaveLength(1);
    expect(sends[0].body).toMatchObject({ to: [{ email: "sender@example.com" }], cc: [], text: "Ordinary synthetic reply", in_reply_to: "<message-1@example.com>", references: "<parent@example.com> <message-1@example.com>" });
    await act(async () => delivery.resolve(json({})));
  });

  it.each([
    ["missing", []], ["another account", [{ id: "other", email: "me@example.com" }]],
    ["empty", [{ id: "shared", email: " " }]], ["invalid", [{ id: "shared", email: "invalid" }]],
  ] as const)("does not guess a receiving address when it is %s", async (_name, accounts) => {
    accountReads.set("alpha", deferred());
    await show();
    await act(async () => accountReads.get("alpha")!.resolve([...accounts]));
    expect(button("inbox-inboxbodies.reply_all")?.disabled).toBe(true);
    expect(button("inbox-inboxbodies.reply_all")?.title).toBe(t("inbox-inboxbodies.reply_all_unavailable"));
    expect(sends).toHaveLength(0);
  });

  it("keeps Reply all unavailable after an account read fails", async () => {
    accountReads.set("alpha", deferred());
    await show();
    await act(async () => accountReads.get("alpha")!.reject(new Error("Synthetic account read failure")));
    expect(button("inbox-inboxbodies.reply_all")?.disabled).toBe(true);
    expect(button("inbox-inboxbodies.reply")?.disabled).toBe(false);
  });

  it("never uses the previous manager's same-id account while the next manager loads", async () => {
    await show();
    expect(button("inbox-inboxbodies.reply_all")?.disabled).toBe(false);
    accountReads.set("beta", deferred());
    const next = { ...message(), to: [...message().to!, { email: "beta@example.com" }] };
    messages.set(1, deferred());
    messages.get(1)!.resolve(next);
    await show("beta");
    expect(button("inbox-inboxbodies.reply_all")?.disabled).toBe(true);
    await act(async () => accountReads.get("beta")!.resolve([{ id: "shared", email: "beta@example.com" }]));
    click("inbox-inboxbodies.reply_all");
    expect(field("to").value).toBe("sender@example.com, ME@example.com, team@example.com");
    expect(field("cc").value).toBe("copy@example.com");
    burst();
    expect(sends).toHaveLength(1);
    expect(sends[0].headers.get(MANAGER_HEADER)).toBe("beta");
    expect(JSON.stringify(sends[0].body.to)).not.toContain("beta@example.com");
    await act(async () => delivery.resolve(json({})));
  });

  it("ignores a late account response from a manager that is no longer shown", async () => {
    accountReads.set("alpha", deferred());
    accountReads.set("beta", deferred());
    await show();
    await show("beta");
    await act(async () => accountReads.get("alpha")!.resolve([{ id: "shared", email: "me@example.com" }]));
    expect(button("inbox-inboxbodies.reply_all")?.disabled).toBe(true);
    await act(async () => accountReads.get("beta")!.resolve([{ id: "shared", email: "me@example.com" }]));
    expect(button("inbox-inboxbodies.reply_all")?.disabled).toBe(false);
  });

  it("retains separate complete Reply and Reply all edits and cancels only the selected mode", async () => {
    await show();
    click("inbox-inboxbodies.reply");
    edit("to", "ordinary@example.com");
    edit("cc", "ordinary-copy@example.com");
    edit("subject", "Ordinary edited subject");
    edit("body", "Ordinary edited body");
    dismiss();
    click("inbox-inboxbodies.reply_all");
    expect(field("to").value).toBe("sender@example.com, team@example.com");
    expect(field("cc").value).toBe("copy@example.com");
    edit("to", "all@example.com");
    edit("cc", "all-copy@example.com");
    edit("subject", "All edited subject");
    edit("body", "All edited body");
    dismiss();
    click("inbox-inboxbodies.reply");
    expect(field("to").value).toBe("ordinary@example.com");
    expect(field("cc").value).toBe("ordinary-copy@example.com");
    expect(field("subject").value).toBe("Ordinary edited subject");
    expect(field("body").value).toBe("Ordinary edited body");
    click("common.cancel");
    click("inbox-inboxbodies.reply_all");
    expect(field("to").value).toBe("all@example.com");
    expect(field("cc").value).toBe("all-copy@example.com");
    expect(field("subject").value).toBe("All edited subject");
    expect(field("body").value).toBe("All edited body");
    click("common.cancel");
    click("inbox-inboxbodies.reply_all");
    expect(field("to").value).toBe("sender@example.com, team@example.com");
  });

  it("preserves edited recipients, sender, quote and thread identity across refresh, navigation and reopen", async () => {
    accountReads.set("alpha", deferred());
    accountReads.get("alpha")!.resolve([{ id: "shared", email: "me@example.com" }, { id: "alternate", email: "alternate@example.com" }]);
    await show();
    click("inbox-inboxbodies.reply_all");
    edit("from", "alternate");
    edit("to", "edited@example.com");
    edit("cc", "edited-copy@example.com");
    edit("subject", "Edited reply all subject");
    const quoted = field("body").value;
    edit("body", `Edited reply all${quoted}`);
    const textarea = field("body");
    messages.set(2, deferred());
    await show("alpha", 2);
    expect(field("body")).toBe(textarea);
    await act(async () => messages.get(2)!.resolve(message(2)));
    expect(field("body").value).toBe(`Edited reply all${quoted}`);
    closePage();
    await show("alpha", 2);
    click("inbox-inboxbodies.reply_all");
    expect(field("from").value).toBe("alternate");
    expect(field("to").value).toBe("edited@example.com");
    expect(field("cc").value).toBe("edited-copy@example.com");
    expect(field("subject").value).toBe("Edited reply all subject");
    burst();
    expect(sends).toHaveLength(1);
    expect(sends[0].body).toMatchObject({ account_id: "alternate", to: [{ email: "edited@example.com" }], cc: [{ email: "edited-copy@example.com" }], text: `Edited reply all${quoted}`, in_reply_to: "<message-1@example.com>", references: "<parent@example.com> <message-1@example.com>" });
    await act(async () => delivery.resolve(json({})));
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value).toContain("> Synthetic original 2");
    expect(field("body").value).not.toContain("Edited reply all");
  });

  it("isolates same-id owners and other items while keeping each mode's edits", async () => {
    await show();
    click("inbox-inboxbodies.reply_all");
    edit("body", "Alpha all draft");
    await show("beta");
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value).not.toBe("Alpha all draft");
    edit("body", "Beta all draft");
    await show("alpha", 1, `${thread}-other`);
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value).not.toBe("Alpha all draft");
    edit("body", "Other item draft");
    await show();
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value).toBe("Alpha all draft");
    await show("beta");
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value).toBe("Beta all draft");
  });

  it("admits one send in a mixed burst, retains failure edits and allows one intentional retry", async () => {
    await show();
    click("inbox-inboxbodies.reply_all");
    edit("body", "Retry this synthetic reply all");
    burst();
    burst();
    expect(sends).toHaveLength(1);
    await act(async () => delivery.resolve(new Response(JSON.stringify({ error: "Synthetic delivery failure" }), { status: 503 })));
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("Synthetic delivery failure");
    expect(field("body").value).toBe("Retry this synthetic reply all");
    delivery = deferred<Response>();
    burst();
    expect(sends).toHaveLength(2);
    expect(sends[1].body).toEqual(sends[0].body);
    await act(async () => delivery.resolve(json({})));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value).not.toContain("Retry this synthetic reply all");
  });

  it.each(["success", "failure"] as const)("a late %s cannot close or replace another owner/mode's live draft", async (outcome) => {
    await show();
    click("inbox-inboxbodies.reply_all");
    edit("body", "Alpha pending reply all");
    burst();
    await show("beta");
    click("inbox-inboxbodies.reply");
    edit("body", "Beta ordinary reply");
    await act(async () => delivery.resolve(outcome === "success" ? json({}) : new Response('{"error":"Synthetic failure"}', { status: 503 })));
    expect(field("body").value).toBe("Beta ordinary reply");
    expect(sends).toHaveLength(1);
    await show();
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value === "Alpha pending reply all").toBe(outcome === "failure");
  });

  it("does not close a newly opened Reply when an offscreen Reply all succeeds for the same owner", async () => {
    await show();
    click("inbox-inboxbodies.reply_all");
    burst();
    dismiss();
    click("inbox-inboxbodies.reply");
    edit("body", "New ordinary draft");
    await act(async () => delivery.resolve(json({})));
    expect(field("body").value).toBe("New ordinary draft");
  });

  it.each(["dismiss", "cancel"] as const)("a late failure respects a newer same-mode draft's %s", async (action) => {
    await show();
    click("inbox-inboxbodies.reply_all");
    edit("body", "Older pending reply all");
    burst();
    dismiss();
    click("inbox-inboxbodies.reply_all");
    edit("body", "Newer reply all edits");
    if (action === "dismiss") dismiss();
    else click("common.cancel");
    await act(async () => delivery.resolve(new Response('{"error":"Synthetic failure"}', { status: 503 })));
    click("inbox-inboxbodies.reply_all");
    expect(field("body").value).not.toBe("Older pending reply all");
    if (action === "dismiss") expect(field("body").value).toBe("Newer reply all edits");
    else expect(field("body").value).toContain("> Synthetic original 1");
  });

  it("requires a deliberate recipient for self-only mail and sends none of the receiving identity", async () => {
    messages.set(1, deferred());
    messages.get(1)!.resolve({ ...message(), from: { email: "ME@example.com" }, to: [{ email: "me@example.com" }], cc: [] });
    await show();
    click("inbox-inboxbodies.reply_all");
    expect(field("to").value).toBe("");
    expect(button("inbox-composedialog.send")?.disabled).toBe(true);
    act(() => document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(sends).toHaveLength(0);
    expect(document.querySelector('[role="alert"]')?.textContent).toBe(t("inbox-composedialog.need_recipient"));
    edit("to", "chosen@example.com");
    burst();
    expect(sends).toHaveLength(1);
    expect(sends[0].body.to).toEqual([{ email: "chosen@example.com" }]);
    await act(async () => delivery.resolve(json({})));
  });
});
