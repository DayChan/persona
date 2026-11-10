// @vitest-environment jsdom
// Settings › Sent as you (#5278): the ledger's rows (where, when, the first lines, how it was let through, who asked),
// paging with "Load earlier", the connector chips (and a response for a filter left behind is dropped), the details
// dialog, refreshing when the page comes back, and the five states: empty, empty under a filter, no database, no full
// access, an older hub.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Call = { path: string; resolve: (v: unknown) => void; reject: (e: unknown) => void };
const calls: Call[] = [];
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => path === "/connectors/sent/chat-rules" ? Promise.resolve({ rules: [], most: 500 }) : new Promise((resolve, reject) => calls.push({ path, resolve, reject })),
}));
const copied: string[] = [];
vi.mock("../../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../../ui/atoms")>()),
  copyText: (text: string) => (copied.push(text), Promise.resolve(true)),
}));

const { ApiError } = await import("../../lib/api");
const { initI18n } = await import("../../i18n");
const { reset, upsertSession } = await import("../../lib/store");
const { SentSettings } = await import("./SentSettings");

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();
const row = (id: number, over: Record<string, unknown> = {}) => ({
  id,
  at: minutesAgo(id),
  connector: "mail",
  action: "mail_send",
  target: { to: ["ann@example.com"], subject: `Subject ${id}` },
  text: `Body of ${id}`,
  summary: `mail_send to ann@example.com`,
  origin: { machine_id: "mac", session_id: "s1" },
  let_through: { kind: "card", interaction_id: `ix-${id}` },
  ...over,
});

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  calls.length = 0;
  copied.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  reset();
  setVisibility("visible");
  await initI18n("zh-Hans");
});

const render = () => act(async () => root.render(<SentSettings />));
const reads = () => calls.filter((c) => c.path.startsWith("/connectors/sent"));
const answer = (c: Call | undefined, v: unknown) => act(async () => c!.resolve(v));
const refuse = (c: Call | undefined, e: unknown) => act(async () => c!.reject(e));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (el: Element | undefined) => act(async () => (el as HTMLElement).click());
/** A row's open button, found by words in it. */
const rowOf = (words: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(words) && b.className.includes("after:"));
/** The rows' targets, top to bottom (a row's button holds the time too, which would run into a number). */
const subjects = () => [...host.querySelectorAll("button.block")].map((b) => /Subject \d+/.exec(b.querySelector("span.flex-1")?.textContent ?? "")?.[0]);
const chip = (label: string) => [...host.querySelectorAll("button[aria-pressed]")].find((b) => b.textContent === label);
const setVisibility = (v: "visible" | "hidden") => Object.defineProperty(document, "visibilityState", { value: v, configurable: true });
const visibility = (v: "visible" | "hidden") => act(async () => (setVisibility(v), document.dispatchEvent(new Event("visibilitychange"))));

/** The page, loaded: the first page of the ledger. */
async function loaded(items: unknown[], next: number | null = null) {
  await render();
  await answer(reads()[0], { items, next });
}

describe("设置 › 以你的名义发出", () => {
  it("asks for the first page with no query, without a connector inventory (rules load independently)", async () => {
    await render();
    expect(calls.map((c) => c.path)).toEqual(["/connectors/sent"]);
    expect(text()).toContain("加载中");
  });

  it("lists each send: connector, where, how long ago, the first two lines, and how it was let through", async () => {
    await loaded([
      row(5, { text: "Line one\n\nLine two\nLine three", let_through: { kind: "rule", rule: { connector: "mail", action: "mail_send", decision: "allow" } } }),
      row(6, { connector: "github", action: "github_comment", target: { ref: "example-org/app#12" }, text: "", summary: "Comment on example-org/app#12", let_through: { kind: "conversation_always" } }),
      row(7, { text: "Sent by hand", let_through: { kind: "card", interaction_id: "ix-7", answered_by: "assistant" } }),
      row(8, { connector: "chatext", action: "sent.record", target: { chat: "general" }, text: "Hello there", origin: { extension: "chatext" }, let_through: { kind: "auto", setting: "auto_reply" } }),
      row(9, { text: "Odd", let_through: { kind: "voice_confirm" } }),
    ]);
    const mail = rowOf("Subject 5")!.parentElement!;
    expect(mail.textContent).toContain("邮件"); // the hub's name for it
    expect(mail.textContent).toContain("ann@example.com · Subject 5");
    expect(mail.textContent).toMatch(/5\s?分钟前/);
    expect(mail.textContent).toContain("Line one");
    expect(mail.textContent).toContain("Line two");
    expect(mail.textContent).not.toContain("Line three");
    expect(mail.textContent).toContain("按规则");

    const github = rowOf("example-org/app#12")!.parentElement!;
    expect(github.textContent).toContain("GitHub");
    expect(github.textContent).toContain("Comment on example-org/app#12"); // no text: the summary
    expect(github.textContent).toContain("本对话始终允许");

    const orange = (words: string) => [...host.querySelectorAll("span")].find((s) => s.textContent === words)?.className.includes("text-orange");
    expect(orange("助手代答")).toBe(true);
    expect(orange("扩展自行发送 · auto_reply")).toBe(true);
    expect(orange("按规则")).toBe(false);
    expect(host.textContent).toContain("voice_confirm"); // a kind it doesn't know, as it is
    // newest first, as the hub gave them
    const order = [...host.querySelectorAll("button")].map((b) => /Subject (\d)|example-org|general|Odd/.exec(b.textContent ?? "")?.[0]).filter(Boolean);
    expect(order).toEqual(["Subject 5", "example-org", "Subject 7", "general", "Subject 9"]);
  });

  it("says who asked: a link to the chat, the assistant's chat in Persona, an extension's own word, or no chat", async () => {
    upsertSession({ machine_id: "srv", session_id: "c1", assistant: true } as never);
    await loaded([
      row(1, { origin: { machine_id: "mac", session_id: "s1" } }),
      row(2, { origin: { machine_id: "srv", session_id: "c1" } }),
      row(3, { origin: { extension: "chatext" } }),
      row(4, { origin: { extension: "chatext", machine_id: "mac", session_id: "s9" } }),
      row(5, { origin: {} }),
    ]);
    const links = [...host.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(links).toEqual(["/s/mac/s1", "/persona/c1", "/s/mac/s9"]);
    const rows = (id: number) => rowOf(`Subject ${id}`)!.parentElement!.textContent ?? "";
    expect(rows(3)).toContain("扩展自述");
    expect(rows(3)).toContain("chatext");
    expect(rows(3)).not.toContain("打开对话");
    expect(rows(4)).toContain("扩展自述");
    expect(rows(4)).toContain("打开对话");
    expect(rows(5)).toContain("无对话");
    expect(rows(1)).not.toContain("无对话");
  });

  it("names each row's chat link by the row, so a screen reader can tell them apart", async () => {
    await loaded([
      row(1, { origin: { machine_id: "mac", session_id: "s1" } }),
      row(2, { connector: "lark", target: { conversation: "oc_demo01" }, origin: { machine_id: "srv", session_id: "c1" } }),
      row(3, { connector: "chatext", target: { arg_keys: ["body"] }, origin: { machine_id: "mac", session_id: "s3" } }),
    ]);
    const links = [...host.querySelectorAll("a")];
    expect(links.map((a) => a.getAttribute("aria-label"))).toEqual([
      "打开对话：邮件 ann@example.com · Subject 1",
      "打开对话：飞书 oc_demo01", // Lark is not in the hub's list here: its own name
      "打开对话：chatext", // a row that says nothing of where it went: the connector alone
    ]);
    expect(new Set(links.map((a) => a.getAttribute("aria-label"))).size).toBe(3);
    // the words on screen start the name (a voice user says what they see), and the link is found by it
    for (const a of links) expect(a.getAttribute("aria-label")!.startsWith(a.textContent!.trim())).toBe(true);
    // the details are one row: the link there needs no more than its words
    await click(rowOf("Subject 1"));
    expect(document.querySelector('[role="dialog"] a')?.getAttribute("aria-label")).toBeNull();
  });

  it("loads older rows by the cursor, merges them by id, and hides the button on the last page", async () => {
    await loaded([row(9), row(8), row(7)], 7);
    expect(button("加载更早")).toBeDefined();
    await click(button("加载更早"));
    expect(reads()[1].path).toBe("/connectors/sent?before=7");
    expect(button("加载更早")?.disabled).toBe(true); // busy
    await answer(reads()[1], { items: [row(7), row(6), row(5)], next: null });
    expect(subjects()).toEqual(["Subject 9", "Subject 8", "Subject 7", "Subject 6", "Subject 5"]);
    expect(button("加载更早")).toBeUndefined();
  });

  it("keeps the list and says why when loading older rows fails, and lets you try again", async () => {
    await loaded([row(9), row(8)], 8);
    await click(button("加载更早"));
    await refuse(reads()[1], new ApiError(500, "boom", { error: "boom", code: "internal" }));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("服务暂时出错");
    expect(rowOf("Subject 9")).toBeDefined();
    await click(button("加载更早"));
    expect(reads()[2].path).toBe("/connectors/sent?before=8");
  });

  it("filters with chips: Lark and the connectors rows show; the list clears and a late answer for the old filter is dropped", async () => {
    await loaded([
      row(9),
      row(8, { connector: "github", target: { ref: "example-org/app#8" } }),
      row(7, { connector: "chatext", target: { chat: "general" } }),
    ]);
    const chips = [...host.querySelectorAll("button[aria-pressed]")].map((b) => b.textContent);
    expect(chips).toEqual(["全部", "飞书", "邮件", "GitHub", "chatext"]);
    expect(chip("全部")?.getAttribute("aria-pressed")).toBe("true");

    await click(chip("邮件"));
    expect(reads()[1].path).toBe("/connectors/sent?connector=mail");
    expect(rowOf("Subject 9")).toBeUndefined(); // cleared at once
    expect(text()).toContain("加载中");
    expect(chip("邮件")?.getAttribute("aria-pressed")).toBe("true");

    await click(chip("GitHub")); // before mail has answered
    expect(reads()[2].path).toBe("/connectors/sent?connector=github");
    await answer(reads()[1], { items: [row(30)], next: null }); // late: for mail
    expect(rowOf("Subject 30")).toBeUndefined();
    await answer(reads()[2], { items: [row(20, { connector: "github", target: { ref: "example-org/app#3" } })], next: null });
    expect(rowOf("example-org/app#3")).toBeDefined();
    expect(rowOf("Subject 30")).toBeUndefined();
    expect(chip("GitHub")?.getAttribute("aria-pressed")).toBe("true");

    // the chosen chip again: back to all
    await click(chip("GitHub"));
    expect(reads()[3].path).toBe("/connectors/sent");
  });

  it("drops a late answer to 'Load earlier' after the filter changed", async () => {
    await loaded([row(9), row(8)], 8);
    await click(button("加载更早"));
    await click(chip("邮件"));
    await answer(reads()[1], { items: [row(7)], next: null });
    expect(rowOf("Subject 7")).toBeUndefined();
    await answer(reads()[2], { items: [row(6)], next: null });
    expect(rowOf("Subject 6")).toBeDefined();
    expect(rowOf("Subject 7")).toBeUndefined();
  });

  it("offers a connector only once a row has shown it: Lark's chip is always there, and nothing reads the hub's connectors", async () => {
    await loaded([row(9, { connector: "lark", target: { conversation: "oc_1" }, text: "Hi" })]);
    expect([...host.querySelectorAll("button[aria-pressed]")].map((b) => b.textContent)).toEqual(["全部", "飞书"]);
    expect(host.textContent).toContain("oc_1");
    expect(calls.some((c) => c.path.startsWith("/connectors?"))).toBe(false);
  });

  it("says nothing is left under a filter, with Show all", async () => {
    await loaded([row(9)]);
    await click(chip("飞书"));
    await answer(reads()[1], { items: [], next: null });
    expect(text()).toContain("飞书 还没有记录");
    expect(chip("飞书")).toBeDefined(); // the chips stay
    await click(button("显示全部"));
    expect(reads()[2].path).toBe("/connectors/sent");
    await answer(reads()[2], { items: [row(9)], next: null });
    expect(rowOf("Subject 9")).toBeDefined();
  });

  it("explains an empty ledger: what is recorded, what is not (lark-cli), and for how long", async () => {
    await loaded([]);
    expect(text()).toContain("还没有记录到以你名义发出的内容");
    expect(text()).toContain("会记：");
    expect(text()).toContain("lark-cli");
    expect(text()).toContain("90 天");
    expect(host.querySelector("[aria-pressed]")).toBeNull(); // nothing to filter yet
  });

  it("no database: says so (not that the hub is down)", async () => {
    await render();
    await refuse(reads()[0], new ApiError(503, "no database: there is no ledger", { error: "no database: there is no ledger", code: "no_database" }));
    expect(text()).toContain("这台 hub 没有数据库");
    expect(text()).not.toContain("暂时无法响应");
    expect(button("重试")).toBeUndefined();
  });

  it("no full access: says which devices can read it", async () => {
    await render();
    await refuse(reads()[0], new ApiError(403, "only a device with full rights", { error: "only a device with full rights", code: "forbidden" }));
    expect(text()).toContain("只有拥有完整权限的设备才能看这个列表");
  });

  it("an older hub (404, no code): says to update the hub", async () => {
    await render();
    await refuse(reads()[0], new ApiError(404, "404 page not found", { error: "404 page not found" }, false));
    expect(text()).toContain("升级 hub");
  });

  it("another failure shows the error with Retry, which reads again", async () => {
    await render();
    await refuse(reads()[0], new ApiError(500, "boom", { error: "boom", code: "internal" }));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("服务暂时出错");
    await click(button("重试"));
    expect(reads()).toHaveLength(2);
    await answer(reads()[1], { items: [row(3)], next: null });
    expect(rowOf("Subject 3")).toBeDefined();
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("a row opens the details: when, action, every target field, how it was let through, the chat, the full text, copyable", async () => {
    await loaded([
      row(4, {
        text: "First\nSecond\nThird line with words",
        action: "github_review",
        connector: "github",
        target: { ref: "example-org/app#12", event: "APPROVE" },
        let_through: { kind: "rule", rule: { connector: "github", action: "github_review", decision: "allow" } },
        message_ref: "msg-123",
      }),
    ]);
    await click(rowOf("example-org/app#12"));
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog.textContent).toContain("GitHub");
    expect(dialog.textContent).toContain("github_review");
    expect(dialog.textContent).toContain("ref");
    expect(dialog.textContent).toContain("example-org/app#12");
    expect(dialog.textContent).toContain("event");
    expect(dialog.textContent).toContain("APPROVE");
    expect(dialog.textContent).toContain("github · github_review → allow");
    expect(dialog.textContent).toContain("msg-123");
    expect(dialog.textContent).toContain("Third line with words"); // past the row's two lines
    expect(dialog.querySelector("a")?.getAttribute("href")).toBe("/s/mac/s1");
    expect(dialog.textContent).not.toContain("已截断");
    await click(button("复制正文"));
    expect(copied).toEqual(["First\nSecond\nThird line with words"]);
    await click(button("完成"));
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("notes that a text of exactly 4000 characters was cut, and one of 3999 was not", async () => {
    await loaded([row(2, { text: "y".repeat(4000) }), row(3, { text: "z".repeat(3999) })]);
    await click(rowOf("Subject 2"));
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain("在 4,000 个字处被截断");
    await click(button("完成"));
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await click(rowOf("Subject 3"));
    expect(document.querySelector('[role="dialog"]')?.textContent).not.toContain("被截断");
  });

  it("a send with no text says so in the details and offers no copy", async () => {
    await loaded([row(2, { text: "", summary: "mail_send to ann@example.com", origin: {}, target: { arg_keys: ["body"] } })]);
    await click(rowOf("mail_send to ann@example.com"));
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog.textContent).toContain("没有记下正文");
    expect(dialog.textContent).toContain("arg_keys");
    expect(dialog.textContent).toContain("无对话");
    expect(button("复制正文")).toBeUndefined();
  });

  it("reads the first page again when the page becomes visible: new rows on top, older pages and their cursor kept", async () => {
    await loaded([row(9), row(8)], 8);
    await click(button("加载更早"));
    await answer(reads()[1], { items: [row(7), row(6)], next: 6 });
    await visibility("hidden");
    expect(reads()).toHaveLength(2); // hidden: nothing read
    await visibility("visible");
    expect(reads()[2].path).toBe("/connectors/sent");
    await answer(reads()[2], { items: [row(10), row(9)], next: 9 });
    expect(subjects()).toEqual(["Subject 10", "Subject 9", "Subject 8", "Subject 7", "Subject 6"]);
    await click(button("加载更早"));
    expect(reads()[3].path).toBe("/connectors/sent?before=6");
  });

  it("keeps the list when a refresh fails, and takes only the latest of two refreshes", async () => {
    await loaded([row(9)]);
    await visibility("hidden");
    await visibility("visible");
    await refuse(reads()[1], new TypeError("Failed to fetch"));
    expect(rowOf("Subject 9")).toBeDefined();
    expect(host.querySelector('[role="alert"]')).toBeNull();
    await visibility("hidden");
    await visibility("visible");
    await visibility("hidden");
    await visibility("visible");
    await answer(reads()[3], { items: [row(11), row(9)], next: null });
    await answer(reads()[2], { items: [row(10)], next: null }); // the older read, late
    expect(subjects()).toEqual(["Subject 11", "Subject 9"]);
  });

  it("drops a read of older rows that was asked before a refresh started the list over (it would leave a hole)", async () => {
    await loaded([row(9), row(8), row(7)], 7);
    await click(button("加载更早")); // asks for the rows before 7
    await visibility("hidden");
    await visibility("visible"); // a refresh while that is on its way
    await answer(reads()[2], { items: [row(10), row(9), row(8)], next: 8 }); // a row came in: the page's end moved to 8
    await answer(reads()[1], { items: [row(6), row(5)], next: null }); // the older read, late: it would join 8 with 6 and lose 7
    expect(subjects()).toEqual(["Subject 10", "Subject 9", "Subject 8"]);
    expect(button("加载更早")?.disabled).toBe(false);
    await click(button("加载更早"));
    expect(reads()[3].path).toBe("/connectors/sent?before=8");
    await answer(reads()[3], { items: [row(7), row(6)], next: null });
    expect(subjects()).toEqual(["Subject 10", "Subject 9", "Subject 8", "Subject 7", "Subject 6"]);
  });

  it("keeps the chips when reading under a filter fails, so another one (or All) can be chosen", async () => {
    await loaded([row(9)]);
    await click(chip("飞书"));
    await refuse(reads()[1], new ApiError(500, "boom", { error: "boom", code: "internal" }));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("服务暂时出错");
    expect(chip("全部")).toBeDefined();
    await click(chip("全部"));
    expect(reads()[2].path).toBe("/connectors/sent");
    await answer(reads()[2], { items: [row(9)], next: null });
    expect(rowOf("Subject 9")).toBeDefined();
  });

  it("shows markup in a send as text: no element is made from it, in the list or in the details", async () => {
    const markup = "<img src=x onerror=alert(1)>";
    await loaded([
      row(3, { text: markup, target: { to: ["ann@example.com"], subject: "<img src=x onerror=alert(2)>" }, message_ref: "<img src=x onerror=alert(3)>" }),
      row(2, { text: "", summary: "<img src=x onerror=alert(4)>", target: { to: ["bob@example.com"], subject: "Plain" } }),
    ]);
    expect(host.querySelector("img")).toBeNull();
    expect(host.textContent).toContain(markup); // as the words they are
    expect(host.textContent).toContain("<img src=x onerror=alert(2)>");
    expect(host.textContent).toContain("<img src=x onerror=alert(4)>");
    await click(rowOf("Plain")); // the one with no text: its summary
    expect(document.querySelector('[role="dialog"] img')).toBeNull();
    await click(button("完成"));
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await click(rowOf("alert(2)"));
    const dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog.querySelector("img")).toBeNull();
    expect(dialog.querySelector("pre")?.textContent).toBe(markup);
    expect(dialog.textContent).toContain("<img src=x onerror=alert(3)>");
    expect(document.querySelector("img")).toBeNull();
  });
});

describe("recall", () => {
  const recalls = () => calls.filter((c) => c.path.endsWith("/recall"));

  it("is offered only when the hub says so, asks first, then shows the recalled row and reads the list again", async () => {
    await loaded([row(5, { connector: "github", target: { ref: "o/r#1" }, can_recall: true }), row(4, { target: { to: ["b@example.com"], subject: "Old" } })]);
    await click(rowOf("Old"));
    expect(button("撤回…")).toBeUndefined();
    await click(button("完成"));
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await click(rowOf("o/r#1"));
    await click(button("撤回…"));
    expect(recalls()).toHaveLength(0); // asked first
    expect(text()).toContain("撤回这条消息");
    await click(button("撤回消息"));
    expect(recalls().map((c) => c.path)).toEqual(["/connectors/sent/5/recall"]);
    const before = reads().length;
    await answer(recalls()[0], { ok: true, item: row(5, { connector: "github", target: { ref: "o/r#1" }, recalled_at: minutesAgo(0), can_recall: false }) });
    expect(button("撤回…")).toBeUndefined();
    expect(text()).toContain("已于");
    expect(reads().length).toBe(before + 1); // the list is read again
  });

  it("shows why the hub refused and reads the list again", async () => {
    await loaded([row(5, { connector: "github", target: { ref: "o/r#1" }, can_recall: true })]);
    await click(rowOf("o/r#1"));
    await click(button("撤回…"));
    await click(button("撤回消息"));
    const before = reads().length;
    await refuse(recalls()[0], new ApiError(409, "too old", { error: "too old", code: "too_old" }));
    expect(text()).toContain("太久");
    expect(reads().length).toBe(before + 1);
  });
});
