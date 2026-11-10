import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { lastEditablePrompt, promptsOf, rewindBlock, rewindDone, rewindFailure, rewindSession, rewindTarget } from "./rewind";
import { ApiError } from "./api";
import type { Item, Session } from "./types";

const user = (id: string, text: string, extra: Partial<Item> = {}): Item => ({ id, seq: 0, role: "user", kind: "text", text, ...extra });
const reply = (id: string): Item => ({ id, seq: 0, role: "assistant", kind: "text", text: "好" });
const tool = (id: string): Item => ({ id, seq: 0, role: "user", kind: "tool_result", tool_use_id: "t1", text: "ok" });

const session = (extra: Partial<Session> = {}): Session => ({ machine_id: "m", session_id: "s", title: "t", cwd: "/x", status: "idle", agent: "claude", ...extra }) as Session;

describe("which message", () => {
  const items = [user("a#0", "继续"), reply("a1#0"), user("b#0", "继续"), tool("b1#0"), reply("b2#0"), user("c#0", "改一下"), reply("c1#0"), user("d#0", "继续")];

  it("counts the messages with the same words from the end, and the ones after it", () => {
    expect(rewindTarget(items, "d#0")).toEqual({ prompt: "继续", occurrence: 1, promptsAfter: 0 });
    expect(rewindTarget(items, "c#0")).toEqual({ prompt: "改一下", occurrence: 1, promptsAfter: 1 });
    expect(rewindTarget(items, "b#0")).toEqual({ prompt: "继续", occurrence: 2, promptsAfter: 2 });
    expect(rewindTarget(items, "a#0")).toEqual({ prompt: "继续", occurrence: 3, promptsAfter: 3 });
  });

  it("takes tool results, queued messages and replies for no message of yours", () => {
    const got = promptsOf([...items, user("e#0", "排队的", { queued: true })]);
    expect(got.map((p) => p.id)).toEqual(["a#0", "b#0", "c#0", "d#0"]);
  });

  it("joins the blocks of one transcript line, and lists an image alone as a message without words", () => {
    const img: Item = { id: "i#1", seq: 0, role: "user", kind: "image", image: { mime: "image/png" } };
    const got = promptsOf([user("i#0", "看图"), img, reply("r#0"), { ...img, id: "j#0" }, reply("r2#0"), user("k#0", "再看")]);
    expect(got).toEqual([{ id: "i#0", raw: "看图" }, { id: "j#0", raw: "" }, { id: "k#0", raw: "再看" }]);
    // the one with no words cannot be named to the node
    expect(rewindTarget([{ ...img, id: "j#0" }, user("k#0", "再看")], "j#0")).toBeNull();
    expect(rewindTarget([user("k#0", "再看")], "nope")).toBeNull();
  });

  it("ignores spacing when it tells identical messages apart", () => {
    expect(rewindTarget([user("a#0", "fix  the\ntest"), user("b#0", "fix the test")], "a#0")?.occurrence).toBe(2);
  });
});

describe("rewindBlock", () => {
  it("is off with the reason", () => {
    expect(rewindBlock(session(), false)).toBe("");
    expect(rewindBlock(session({ agent: undefined }), false)).toBe("");
    expect(rewindBlock(session({ agent: "codex" }), false)).toBe("仅 Claude");
    expect(rewindBlock(session({ source_kind: "claude_bg" }), false)).toBe("仅 Claude");
    expect(rewindBlock(session({ assistant: true }), false)).toBe("仅 Claude");
    expect(rewindBlock(session({ state: "working" }), false)).toBe("工作中");
    expect(rewindBlock(session(), true)).toBe("工作中");
    expect(rewindBlock(session({ state: "needs_you" }), false)).toBe("需要你");
    expect(rewindBlock(session({ status: "offline" }), false)).toBe("未运行");
    expect(rewindBlock(undefined, false)).toBe("仅 Claude");
  });
});

describe("the last editable prompt", () => {
  it("recalls the full last entry including quotes, context and multiple text blocks", () => {
    const text = '> Quoted text\n\n<persona-context>Context</persona-context>\nRevise it';
    const items = [user("a#0", "Earlier"), user("b#0", text), user("b#1", "Second block"), tool("result#0"), reply("answer#0")];
    expect(lastEditablePrompt(items)).toEqual({ id: "b#0", raw: text + "\nSecond block" });
  });

  it.each([
    { kind: "image" as const, text: undefined, image: { mime: "image/png" } },
    { full_size: 99999 },
    { from: "Other conversation" },
    { injected: { kind: "followup", line: "Scheduled input" } },
    { source: { kind: "supervision" } },
    { source: { kind: "conversation", conversation_id: "other-chat" } },
  ])("does not rewind an earlier input when the last entry cannot be restored: %j", (extra) => {
    expect(lastEditablePrompt([user("a#0", "Earlier"), user("b#0", "Last", extra)])).toBeNull();
  });

  it("refuses a text entry with a binary image", () => {
    expect(lastEditablePrompt([user("a#0", "Look at this"), { id: "a#1", seq: 1, role: "user", kind: "image" }])).toBeNull();
  });

  it("refuses inbound provenance carried only in durable or legacy context tags", () => {
    const durable = '<persona-context kind="message-source">{"kind":"conversation","conversation_id":"other-chat"}</persona-context>\nIncoming prompt';
    const legacy = '<persona-context kind="context" title="来自对话「Other chat」">\n这条消息不是用户本人发的，是 Persona 的另一段对话（other-chat）用 conversation_send 发来的，第 1 跳（最多 3 跳）。\n按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 other-chat；只是确认收到就不要回，免得两段对话来回发。\n</persona-context>\nIncoming prompt'; // i18n-ignore: canonical hub relay data
    expect(lastEditablePrompt([user("a#0", durable)])).toBeNull();
    expect(lastEditablePrompt([user("b#0", legacy)])).toBeNull();
    expect(lastEditablePrompt([user("c#0", legacy, { client_msg_id: "sent-here" })])?.raw).toBe(legacy);
    const quoted = '<persona-context kind="message-source">{"author":"user"}</persona-context>\n' + legacy;
    expect(lastEditablePrompt([user("d#0", quoted)])?.raw).toBe(quoted);
  });
});

describe("the request", () => {
  beforeEach(() => vi.stubGlobal("localStorage", { getItem: () => "jd_test" }));
  afterEach(() => vi.unstubAllGlobals());

  it("posts the target and the choice, and answers what the node did", async () => {
    const calls: { url: string; init: RequestInit }[] = [];
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return new Response(JSON.stringify({ prompt: "继续", restore: "conversation", code_restored: false, input_cleared: true }), { status: 200 });
    });
    const res = await rewindSession("m 1", "s/1", { prompt: "继续", occurrence: 2, promptsAfter: 2 }, "conversation");
    expect(res.input_cleared).toBe(true);
    expect(calls[0].url).toBe("/api/v1/sessions/m%201/s%2F1/rewind");
    expect(calls[0].init.method).toBe("POST");
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ prompt: "继续", occurrence: 2, prompts_after: 2, restore: "conversation" });
  });

  it("rejects with the node's own words", async () => {
    vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ error: "Claude 正在工作", code: "rewind_busy" }), { status: 409 }));
    await expect(rewindSession("m", "s", { prompt: "x", occurrence: 1, promptsAfter: 0 }, "conversation")).rejects.toThrow("Claude 正在工作");
  });

  it("says when the hub or the device cannot", () => {
    expect(rewindFailure(new ApiError(404, "not found", {}))).toContain("不支持回退");
    expect(rewindFailure(new ApiError(403, "forbidden", {}))).toContain("完整权限");
    expect(rewindFailure(new ApiError(409, "这台机器上的 personad 太旧", { code: "node_outdated" }))).toContain("太旧");
  });

  it("tells whether the files went back", () => {
    const res = { prompt: "x", restore: "conversation" as const, code_restored: false, input_cleared: true };
    expect(rewindDone(res, "conversation").text).toBe("已回退对话");
    expect(rewindDone(res, "code_and_conversation").text).toContain("没有代码改动");
    expect(rewindDone({ ...res, restore: "code_and_conversation", code_restored: true }, "code_and_conversation").text).toBe("已回退代码和对话");
  });
});
