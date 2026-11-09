import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import type { LarkMessage } from "./inbox";
import {
  HIDDEN_DISPLAY, LarkDisplayModel, quoteSummary, showsBody, showsHistory, versionParts, type LarkDisplayDeps, type LarkMessageDisplay,
} from "./lark-display";
import { pinSummary } from "./lark-pins";

const on: LarkMessageDisplay = { account_id: "acct", anti_recall: true, show_edit_history: true };
const msg = (extra: Partial<LarkMessage> = {}): LarkMessage => ({ id: "m1", position: "1", text: "评审改到周五", parts: [{ kind: "text", text: "评审改到周五" }], ...extra });
const history = [{ edited_at: "2026-11-05T10:00:00Z", text: "评审改到周四" }];

describe("what a recalled or edited message shows", () => {
  it("shows a recalled message's content only with 防撤回 on and the content kept", () => {
    expect(showsBody(HIDDEN_DISPLAY, msg())).toBe(true);
    expect(showsBody(HIDDEN_DISPLAY, msg({ recalled: true, retained: true }))).toBe(false);
    expect(showsBody(on, msg({ recalled: true, retained: true }))).toBe(true);
    expect(showsBody(on, msg({ recalled: true, retained: false }))).toBe(false);
    expect(showsBody(on, msg({ recalled: true }))).toBe(false);
    expect(showsBody(on, msg({ recalled: true, retained: true, text: "", parts: [] }))).toBe(false);
    expect(showsBody(on, msg({ recalled: true, retained: true, type: "SYSTEM" }))).toBe(false);
  });

  it("opens 编辑历史 only with the switch on, versions kept and the content shown", () => {
    expect(showsHistory(on, msg({ edited: true, edit_history: history }))).toBe(true);
    expect(showsHistory({ ...on, show_edit_history: false }, msg({ edited: true, edit_history: history }))).toBe(false);
    expect(showsHistory(on, msg({ edited: true }))).toBe(false);
    expect(showsHistory({ ...on, anti_recall: false }, msg({ recalled: true, retained: true, edit_history: history }))).toBe(false);
  });

  it("shows an encrypted version only by its public face", () => {
    expect(versionParts({ edited_at: "", text: "旧版本" })).toEqual([{ kind: "text", text: "旧版本" }]);
    expect(versionParts({ edited_at: "", text: "解密正文", olk: { state: "locked", publicParts: [{ kind: "text", text: "稍后确认" }] } }))
      .toEqual([{ kind: "text", text: "稍后确认" }]);
    expect(versionParts({ edited_at: "", text: "解密正文", olk: { state: "locked" } })).toEqual([{ kind: "text", text: "加密消息" }]);
  });

  it("summarises a quote and a Pin by the same rule, marked 已撤回", () => {
    const q = { id: "m1", text: "旧评审时间", recalled: true, retained: true };
    expect(quoteSummary(HIDDEN_DISPLAY, q)).toBe("消息已撤回");
    expect(quoteSummary(on, q)).toBe("旧评审时间 · 已撤回");
    expect(quoteSummary(on, { ...q, retained: false })).toBe("消息已撤回");
    expect(quoteSummary(HIDDEN_DISPLAY, { text: "正常引用" })).toBe("正常引用");
    const pin = { id: "p1", message_id: "m1", message: msg({ recalled: true, retained: true }) };
    expect(pinSummary(pin)).toBe("消息已撤回");
    expect(pinSummary(pin, on)).toBe("评审改到周五 · 已撤回");
  });
});

function deps(reads: (() => Promise<LarkMessageDisplay>)[], write?: LarkDisplayDeps["write"]): LarkDisplayDeps & { calls: number } {
  const d = {
    calls: 0,
    read: () => reads[Math.min(d.calls++, reads.length - 1)](),
    write: write ?? (async () => on),
  };
  return d;
}

describe("LarkDisplayModel", () => {
  it("hides everything until read, and again after a failed read", async () => {
    const d = deps([async () => on, async () => { throw new ApiError(502, "x", { code: "lark_not_logged_in" }); }]);
    const m = new LarkDisplayModel(d);
    expect(m.policy).toBe(HIDDEN_DISPLAY);
    await m.refresh();
    expect(m.policy).toEqual(on);
    await m.refresh();
    expect(m.policy).toBe(HIDDEN_DISPLAY);
    expect(m.state.problem).toContain("没有登录飞书");
  });

  it("does not trust a reply without an account, and says a hub without the route needs updating", async () => {
    const m = new LarkDisplayModel(deps([async () => ({ ...on, account_id: "" })]));
    await m.refresh();
    expect(m.state.snapshot).toBeUndefined();
    const old = new LarkDisplayModel(deps([async () => { throw new ApiError(404, "not found", {}); }]));
    await old.refresh();
    expect(old.state.problem).toContain("先更新 hub");
  });

  it("reads again when a refresh is asked for during a read, publishing only the later read", async () => {
    let release!: (v: LarkMessageDisplay) => void;
    const first = new Promise<LarkMessageDisplay>((r) => (release = r));
    const d = deps([() => first, async () => ({ ...on, anti_recall: false })]);
    const m = new LarkDisplayModel(d);
    const running = m.refresh();
    void m.refresh();
    release(on);
    await running;
    expect(d.calls).toBe(2);
    expect(m.policy.anti_recall).toBe(false);
  });

  it("writes one switch with the account and its old value; a conflict hides and is not retried", async () => {
    const bodies: unknown[] = [];
    const m = new LarkDisplayModel(deps([async () => ({ ...on, anti_recall: false })], async (b) => {
      bodies.push(b);
      throw new ApiError(409, "changed", {});
    }));
    await m.refresh();
    await m.change("anti_recall", true);
    expect(bodies).toEqual([{ account_id: "acct", key: "anti_recall", value: true, expected: false }]);
    expect(m.state.snapshot).toBeUndefined();
    expect(m.state.problem).toContain("重新加载一下");
    await m.change("anti_recall", true); // nothing known: nothing written
    expect(bodies).toHaveLength(1);
  });

  it("does not write a switch the extension does not have", async () => {
    const bodies: unknown[] = [];
    const m = new LarkDisplayModel(deps([async () => on], async (b) => (bodies.push(b), on)));
    await m.refresh();
    await m.change("unsupported_setting" as never, true);
    expect(bodies).toHaveLength(0);
  });
});

describe("message source text", () => {
  const text = "**raw source text**";

  it("keeps the quote and pin text exactly as the source sent it", () => {
    for (const display of [on, HIDDEN_DISPLAY]) {
      expect(quoteSummary(display, { text })).toBe(text);
      expect(pinSummary({ id: "p1", message_id: "m1", message: msg({ text }) }, display)).toBe(text);
    }
  });
});
