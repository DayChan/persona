// @vitest-environment jsdom
// The mail frame sizes itself with a ResizeObserver on the frame's body and catches link clicks on its document. The
// observer lives in this window: left connected, it keeps every old frame document (and the frame's state) alive, one
// more on each load of the frame and on each frame that goes (a new mail of the thread, Show translation, another item).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { InboxItem } from "../../lib/inbox";
import type { MailMessage } from "../../lib/mail";

const answers = new Map<string, (v: unknown) => void>();
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn((path: string) => {
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "acc", email: "me@example.com" }] });
    if (path.includes("/flags")) return Promise.resolve({});
    return new Promise((resolve) => answers.set(path.replace(/\?.*$/, ""), resolve));
  }),
}));
vi.mock("../../lib/translate", async (orig) => ({ ...(await orig<typeof import("../../lib/translate")>()), useTranslatePrefs: () => ({ enabled: false, target: "zh-Hans" }) }));

import { MailBody } from "./InboxBodies";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** Every ResizeObserver made, and whether it is still connected (observing something and not disconnected). */
class FakeResizeObserver {
  static made: FakeResizeObserver[] = [];
  targets: Element[] = [];
  disconnected = false;
  constructor(readonly callback: ResizeObserverCallback) {
    FakeResizeObserver.made.push(this);
  }
  observe(target: Element) {
    this.targets.push(target);
  }
  unobserve(target: Element) {
    this.targets = this.targets.filter((t) => t !== target);
  }
  disconnect() {
    this.disconnected = true;
    this.targets = [];
  }
}
// counted, not printed: an observer holds the body of a frame document whose window may be gone
const live = () => FakeResizeObserver.made.filter((o) => !o.disconnected && o.targets.length > 0);

let thread = 0;
const item = (uids: number[]): InboxItem =>
  ({ id: `mail:acc:f${thread}:1`, source: "mail", kind: "mail", account: "acc", title: "Weekly", count: uids.length, tier: "action", version: `v${uids.length}`, read: true,
    mail: { account_id: "acc", mailbox: "INBOX", uids, from: { email: "a@example.com" } } }) as InboxItem;
const mail = (uid: number, html: string): MailMessage => ({ uid, subject: "Weekly", from: { email: "a@example.com" }, html, message_id: `<m${uid}@example.com>` }) as MailMessage;

let host: HTMLDivElement;
let root: Root;
const realResizeObserver = globalThis.ResizeObserver;
beforeEach(() => {
  thread++;
  answers.clear();
  FakeResizeObserver.made = [];
  globalThis.ResizeObserver = FakeResizeObserver as unknown as typeof ResizeObserver;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  globalThis.ResizeObserver = realResizeObserver;
  vi.restoreAllMocks();
});

const answer = (uid: number, html: string) => act(async () => answers.get(`/mail/messages/acc/${uid}`)!({ message: mail(uid, html) }));
const frame = () => document.querySelector<HTMLIFrameElement>("iframe")!;
/** The click listeners on the frame's document as it is now, the one its load hooks onto. */
const clicksOn = (doc: Document) => {
  const clicks = new Set<EventListenerOrEventListenerObject>();
  const add = doc.addEventListener.bind(doc);
  const remove = doc.removeEventListener.bind(doc);
  vi.spyOn(doc, "addEventListener").mockImplementation((type: string, fn: EventListenerOrEventListenerObject, opts?: boolean | AddEventListenerOptions) => {
    if (type === "click") clicks.add(fn);
    add(type, fn, opts);
  });
  vi.spyOn(doc, "removeEventListener").mockImplementation((type: string, fn: EventListenerOrEventListenerObject, opts?: boolean | EventListenerOptions) => {
    if (type === "click") clicks.delete(fn);
    remove(type, fn, opts);
  });
  return clicks;
};
/** The load event the browser fires on the frame. */
const load = () => act(() => frame().dispatchEvent(new Event("load")));

describe("the mail frame", () => {
  it("keeps one size observer and one click listener however often its document loads", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1, "<p>one</p>"); // jsdom loads the inserted frame once by itself
    const clicks = clicksOn(frame().contentDocument!);
    load();
    expect(live().length).toBe(1);
    expect(clicks.size).toBe(1);
    load();
    expect(live().length).toBe(1);
    expect(clicks.size).toBe(1);
  });

  it("lets go of them when a new mail of the thread replaces it, and when it goes", async () => {
    act(() => root.render(<MailBody item={item([1])} />));
    await answer(1, "<p>one</p>");
    const first = clicksOn(frame().contentDocument!);
    load();

    act(() => root.render(<MailBody item={item([1, 2])} />)); // the body reads the new mail, a new frame shows it
    await answer(2, "<p>two</p>");
    const second = clicksOn(frame().contentDocument!);
    load();
    expect(live().length).toBe(1);
    expect(first.size + second.size).toBe(1);

    act(() => root.render(<div />)); // another item opened, or the page left
    expect(live().length).toBe(0);
    expect(first.size + second.size).toBe(0);
  });
});
