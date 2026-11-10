// Settings › Sent as you's words and rules (#5278): where a send went, how the gate let it through, who asked, the
// ledger's paging and refreshing, the connector chips, and the hub's refusals in plain words. The UI runs in zh-Hans here.
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, errorText } from "./api";
import {
  allowedBy, connectorChoices, connectorName, fieldText, mergeSent, originInfo, previewLines, recallProblem, recallSent, refreshed, ruleText,
  seenConnectors, SENT_TEXT_MOST, sentPage, sentPath, sentProblem, targetFields, targetText, textCut, type SentItem,
} from "./sent";

const item = (over: Partial<SentItem> = {}): SentItem => ({
  id: 1, at: "2026-11-05T12:00:00Z", connector: "mail", action: "mail_send", target: {}, text: "", summary: "", origin: {}, let_through: {}, ...over,
});

describe("reading the ledger", () => {
  it("asks for a page by the cursor and the connector, and for the first page with no query", () => {
    expect(sentPath()).toBe("/connectors/sent");
    expect(sentPath({ before: null, connector: null })).toBe("/connectors/sent");
    expect(sentPath({ before: 41 })).toBe("/connectors/sent?before=41");
    expect(sentPath({ before: 41, connector: "mail" })).toBe("/connectors/sent?before=41&connector=mail");
    expect(sentPath({ limit: 200, connector: "a b&c" })).toBe("/connectors/sent?limit=200&connector=a+b%26c");
  });

  it("takes a page as it comes: the three objects are objects, a row with no id is dropped, the last page has next null", () => {
    const page = sentPage({ items: [{ id: 7, at: "x", connector: "github", target: null, let_through: [], origin: "no" }, { connector: "mail" }, null], next: null });
    expect(page.next).toBeNull();
    expect(page.items).toEqual([
      { id: 7, at: "x", connector: "github", action: "", target: {}, text: "", summary: "", origin: {}, let_through: {}, message_ref: undefined, recalled_at: undefined, can_recall: false },
    ]);
    expect(sentPage({ items: [{ id: 3 }], next: 3 }).next).toBe(3);
    expect(sentPage({})).toEqual({ items: [], next: null });
    expect(sentPage(null)).toEqual({ items: [], next: null });
  });
});

describe("where it went", () => {
  it("mail: the recipients and the subject", () => {
    expect(targetText(item({ target: { to: ["a@example.com", "b@example.com"], subject: "Hi", cc: ["c@example.com"] } }))).toBe("a@example.com, b@example.com · Hi");
    expect(targetText(item({ target: { to: ["a@example.com"], subject: "" } }))).toBe("a@example.com");
  });

  it("GitHub: the repo#number; a review's event stays in the details", () => {
    const g = item({ connector: "github", action: "github_review", target: { ref: "example-org/app#12", event: "APPROVE" } });
    expect(targetText(g)).toBe("example-org/app#12");
    expect(targetFields(g.target)).toEqual([{ key: "ref", value: "example-org/app#12" }, { key: "event", value: "APPROVE" }]);
  });

  it("Lark: the conversation, else the call the node's gate saw", () => {
    expect(targetText(item({ connector: "lark", target: { conversation: "oc_1", method: "POST", path: "/im/v1/conversations/oc_1/messages" } }))).toBe("oc_1");
    expect(targetText(item({ connector: "lark", target: { method: "POST", path: "/docs/v1/comments" } }))).toBe("POST /docs/v1/comments");
    expect(targetText(item({ connector: "lark", target: {} }))).toBe("—");
  });

  it("any other connector: the common fields it named, three at most, in the hub's order", () => {
    expect(targetText(item({ connector: "chatext", target: { thread: "t-9", chat: "general" } }))).toBe("general · t-9");
    expect(targetText(item({ connector: "chatext", target: { number: 4, repo: "r", subject: "s", chat: "c", extra: "x" } }))).toBe("c · s · r");
    expect(targetText(item({ connector: "chatext", target: { recipient: ["ann", "bo"] } }))).toBe("ann, bo");
  });

  it("nothing to show is a dash: only the argument names, an empty target, or a mail with no recipients", () => {
    expect(targetText(item({ connector: "chatext", target: { arg_keys: ["body", "id"] } }))).toBe("—");
    expect(targetText(item({ connector: "chatext", target: {} }))).toBe("—");
    expect(targetText(item({ connector: "mail", target: { subject: "only" } }))).toBe("only");
    expect(targetText(item({ connector: "github", target: {} }))).toBe("—");
  });

  it("the details list every non-empty field: lists joined, numbers and booleans as they are, nested values as JSON", () => {
    expect(targetFields({ to: ["a", "b"], n: 3, ok: false, empty: "", none: null, nested: { a: 1 }, list: [] })).toEqual([
      { key: "to", value: "a, b" },
      { key: "n", value: "3" },
      { key: "ok", value: "false" },
      { key: "nested", value: '{"a":1}' },
    ]);
    expect(targetFields(undefined)).toEqual([]);
    expect(fieldText("  x ")).toBe("x");
  });
});

describe("what it said", () => {
  it("shows the first two lines of the text, blank lines and carriage returns skipped", () => {
    expect(previewLines(item({ text: "one\r\n\r\ntwo\nthree" }))).toEqual(["one", "two"]);
    expect(previewLines(item({ text: "  only  " }))).toEqual(["only"]);
    expect(previewLines(item({ text: "a\nb\nc" }), 3)).toEqual(["a", "b", "c"]);
  });

  it("shows the summary when there is no text, and nothing when there is neither", () => {
    expect(previewLines(item({ text: " \n ", summary: "mail_send to a@example.com" }))).toEqual(["mail_send to a@example.com"]);
    expect(previewLines(item({}))).toEqual([]);
  });

  it("says a text was cut only when it is exactly as long as the ledger keeps, counting characters", () => {
    expect(textCut("x".repeat(SENT_TEXT_MOST))).toBe(true);
    expect(textCut("x".repeat(SENT_TEXT_MOST - 1))).toBe(false);
    // 3999 letters and one emoji are 4000 characters but 4001 UTF-16 units; 2000 emoji are 2000 characters
    expect(textCut("x".repeat(3999) + "😀")).toBe(true);
    expect(textCut("😀".repeat(2000))).toBe(false);
    expect(textCut("")).toBe(false);
  });
});

describe("how the gate let it through", () => {
  it("a rule: by a rule, and the rule written the way connector_gate has it", () => {
    const rule = { connector: "lark", action: "POST /im/v1/messages", match: { chat_id: "oc_1", kind: ["a", "b"] }, decision: "allow" };
    expect(allowedBy({ kind: "rule", rule })).toEqual({
      label: "按规则", tone: "neutral", detail: "lark · POST /im/v1/messages (chat_id=oc_1, kind=a, b) → allow",
    });
    expect(ruleText({ connector: "github", action: "github_comment", decision: "allow" })).toBe("github · github_comment → allow");
    expect(ruleText(undefined)).toBe("");
    // a rule the hub did not give: still a rule
    expect(allowedBy({ kind: "rule" })).toMatchObject({ label: "按规则", detail: "一条网关规则放行了它。" });
  });

  it("always allowed in this chat", () => {
    expect(allowedBy({ kind: "conversation_always" })).toMatchObject({ label: "本对话始终允许", tone: "neutral" });
  });

  it("the card: the user's own answer says no more than that they agreed on a card (the ledger can't say which device)", () => {
    expect(allowedBy({ kind: "card", interaction_id: "ix-1" })).toEqual({ label: "你在确认卡片上同意", tone: "neutral", detail: "弹出了确认卡片，你点了允许。" });
    expect(allowedBy({ kind: "card" })).toMatchObject({ label: "你在确认卡片上同意", tone: "neutral" });
    expect(JSON.stringify(allowedBy({ kind: "card" }))).not.toContain("手机");
  });

  it("the card answered by the assistant: that, in orange", () => {
    expect(allowedBy({ kind: "card", interaction_id: "ix-1", answered_by: "assistant" })).toMatchObject({ label: "助手代答", tone: "orange" });
  });

  it("the card answered by anyone else: named, in orange, and never as the user's confirmation", () => {
    const other = allowedBy({ kind: "card", answered_by: "bot-x" });
    expect(other).toEqual({ label: "由 bot-x 代答", tone: "orange", detail: "确认卡片由 bot-x 作答。" });
    expect(other.label).not.toBe(allowedBy({ kind: "card" }).label);
  });

  it("an extension's own send, in orange, with the setting that let it", () => {
    expect(allowedBy({ kind: "auto", setting: "auto_reply" })).toMatchObject({ label: "扩展自行发送 · auto_reply", tone: "orange" });
    expect(allowedBy({ kind: "auto" })).toMatchObject({ label: "扩展自行发送", tone: "orange" });
  });

  it("a kind this page doesn't know is shown as it is; none at all is a dash", () => {
    expect(allowedBy({ kind: "voice_confirm" })).toEqual({ label: "voice_confirm", tone: "neutral", detail: "" });
    expect(allowedBy({})).toMatchObject({ label: "—" });
    expect(allowedBy(undefined)).toMatchObject({ label: "—" });
  });
});

describe("who asked", () => {
  it("a chat to open needs both ids", () => {
    expect(originInfo({ machine_id: "mac", session_id: "s1" })).toEqual({ conversation: { mid: "mac", sid: "s1" }, extension: "" });
    expect(originInfo({ machine_id: "mac" })).toEqual({ conversation: null, extension: "" });
    expect(originInfo({ session_id: "s1" })).toEqual({ conversation: null, extension: "" });
  });

  it("an extension's own record says so, with the chat it names when it names one", () => {
    expect(originInfo({ extension: "examplechat" })).toEqual({ conversation: null, extension: "examplechat" });
    expect(originInfo({ extension: "examplechat", machine_id: "mac", session_id: "s1" })).toEqual({ conversation: { mid: "mac", sid: "s1" }, extension: "examplechat" });
  });

  it("an empty origin is no chat", () => {
    expect(originInfo({})).toEqual({ conversation: null, extension: "" });
    expect(originInfo(undefined)).toEqual({ conversation: null, extension: "" });
  });
});

describe("the list", () => {
  it("merges by id: one row each, the newer copy wins, newest first", () => {
    const old = item({ id: 5, summary: "old" });
    const merged = mergeSent([item({ id: 9 }), old, item({ id: 2 })], [item({ id: 5, summary: "new", recalled_at: "t" }), item({ id: 12 })]);
    expect(merged.map((i) => i.id)).toEqual([12, 9, 5, 2]);
    expect(merged[2].summary).toBe("new");
  });

  it("a refresh with nothing older loaded is the page alone: what the hub pruned goes", () => {
    const have = [item({ id: 5 }), item({ id: 4 })];
    expect(refreshed(have, { items: [item({ id: 6 }), item({ id: 5 })], next: 5 }, false)).toEqual({ items: [item({ id: 6 }), item({ id: 5 })], next: 5, loadedOlder: false });
  });

  it("a refresh with older pages loaded merges the page in and keeps the older end's cursor", () => {
    const have = [item({ id: 100 }), item({ id: 99 }), item({ id: 51 }), item({ id: 50 }), item({ id: 3 })];
    const out = refreshed(have, { items: [item({ id: 102 }), item({ id: 101 }), item({ id: 100 })], next: 100 }, true);
    expect(out.items.map((i) => i.id)).toEqual([102, 101, 100, 99, 51, 50, 3]);
    expect(out.next).toBeUndefined();
    expect(out.loadedOlder).toBe(true);
  });

  it("a refresh whose page does not reach the newest row held starts over (merging would leave a hole)", () => {
    const have = [item({ id: 100 }), item({ id: 99 })];
    const page = { items: [item({ id: 160 }), item({ id: 159 })], next: 159 };
    expect(refreshed(have, page, true)).toEqual({ items: page.items, next: 159, loadedOlder: false });
  });

  it("a first page that is the whole ledger merges whatever its ids, and an empty one empties the list", () => {
    const have = [item({ id: 100 }), item({ id: 50 })];
    expect(refreshed(have, { items: [item({ id: 200 })], next: null }, true).items.map((i) => i.id)).toEqual([200, 100, 50]);
    expect(refreshed(have, { items: [], next: null }, true)).toEqual({ items: [], next: null, loadedOlder: false });
  });
});

describe("the connector chips", () => {
  it("are Lark, then the connectors rows have shown, then the chosen one, none twice", () => {
    expect(connectorChoices(["mail", "github", "goneext"], "other")).toEqual([
      { id: "lark", name: "飞书" },
      { id: "mail", name: "邮件" },
      { id: "github", name: "GitHub" },
      { id: "goneext", name: "goneext" },
      { id: "other", name: "other" },
    ]);
    expect(connectorChoices(["lark", "mail"], "mail").map((c) => c.id)).toEqual(["lark", "mail"]);
  });

  it("offer only Lark before any row has shown: a connector that only reads (Messages) has no chip", () => {
    expect(connectorChoices([], null)).toEqual([{ id: "lark", name: "飞书" }]);
    expect(connectorChoices([], null).map((c) => c.id)).not.toContain("imessage");
  });

  it("keep the chosen connector's chip while it is chosen, though no row has shown it", () => {
    expect(connectorChoices([], "mail")).toEqual([{ id: "lark", name: "飞书" }, { id: "mail", name: "邮件" }]);
  });

  it("name mail, GitHub and Lark by their usual words, any other connector by its id", () => {
    expect(connectorName("mail")).toBe("邮件");
    expect(connectorName("github")).toBe("GitHub");
    expect(connectorName("lark")).toBe("飞书");
    expect(connectorName("xext")).toBe("xext");
  });

  it("collect the connectors rows show, in the order they first showed, keeping the same list when nothing is new", () => {
    const seen = ["mail"];
    expect(seenConnectors(seen, [item({ connector: "mail" })])).toBe(seen);
    expect(seenConnectors(seen, [item({ connector: "github" }), item({ connector: "mail" }), item({ connector: "github" }), item({ connector: "" })])).toEqual(["mail", "github"]);
  });
});

describe("the hub's refusals", () => {
  it("no database is said as that, not as the hub being unavailable", () => {
    const e = new ApiError(503, "no database: there is no ledger", { error: "x", code: "no_database" });
    expect(sentProblem(e)).toEqual({ kind: "no_database", text: "这台 hub 没有数据库，所以不记录以你的名义发出的内容。" });
    expect(sentProblem(e).text).not.toBe(errorText(e));
  });

  it("a 503 with no such code is the usual unavailable text", () => {
    const p = sentProblem(new ApiError(503, "bad gateway", { error: "bad gateway", code: "node_offline" }));
    expect(p.kind).toBe("error");
    expect(p.text).toContain("HTTP 503");
  });

  it("403 is a device without full access", () => {
    expect(sentProblem(new ApiError(403, "only a device with full rights", { error: "x", code: "forbidden" }))).toMatchObject({ kind: "forbidden", text: expect.stringContaining("完整权限") });
  });

  it("a 404 with no code is a hub from before the ledger; one with a code is an ordinary failure", () => {
    expect(sentProblem(new ApiError(404, "404 page not found", { error: "404 page not found" }, false))).toMatchObject({ kind: "unsupported", text: expect.stringContaining("升级 hub") });
    expect(sentProblem(new ApiError(404, "no such thing", { error: "no such thing", code: "not_found" })).kind).toBe("error");
  });

  it("anything else is the usual error text", () => {
    expect(sentProblem(new ApiError(500, "boom", { error: "boom", code: "internal" })).kind).toBe("error");
    expect(sentProblem(new TypeError("Failed to fetch"))).toMatchObject({ kind: "error", text: expect.stringContaining("hub") });
    expect(sentProblem(new Error("plain"))).toEqual({ kind: "error", text: "plain" });
  });
});

describe("recall", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("posts confirm: true to the row's route and takes the row the hub answers with", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true, item: { id: 9, connector: "github", recalled_at: "2026-11-05T12:01:00Z", can_recall: false } })));
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    const it = await recallSent(9);
    const [url, init] = fetcher.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/v1\/connectors\/sent\/9\/recall$/);
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ confirm: true });
    expect(it).toMatchObject({ id: 9, recalled_at: "2026-11-05T12:01:00Z", can_recall: false });
  });

  it("reads can_recall only as a true boolean", () => {
    expect(sentPage({ items: [{ id: 1, can_recall: true }, { id: 2, can_recall: "yes" }, { id: 3 }] }).items.map((i) => i.can_recall)).toEqual([true, false, false]);
  });

  it("says the hub's refusals in plain words, and the hub's own reason when it refused or the code is new", () => {
    const err = (status: number, code?: string, error = "x") => new ApiError(status, error, code ? { error, code } : { error });
    expect(recallProblem(err(409, "too_old"))).toContain("太久");
    expect(recallProblem(err(409, "already_recalled"))).toContain("已经撤回");
    expect(recallProblem(err(409, "not_recallable"))).toContain("不能撤回");
    expect(recallProblem(err(503, "connector_unavailable"))).toContain("连接器");
    expect(recallProblem(err(504, "recall_unknown"))).toContain("不确定");
    expect(recallProblem(err(403, "own_device_only"))).toContain("完整权限");
    expect(recallProblem(err(404))).toContain("更新 Hub");
    expect(recallProblem(err(502, "recall_refused", "GitHub said no"))).toContain("GitHub said no");
    expect(recallProblem(err(409, "something_new", "a new reason"))).toBe("a new reason");
  });
});
