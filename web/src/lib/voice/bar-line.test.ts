import { describe, expect, it } from "vitest";
import type { Session } from "../types";
import { announced, barLine, barStatus, sinceText, type BarLineInput } from "./bar-line";
import type { VoicePhase } from "./conversation";
import type { VoiceRun } from "./wire";

const live: VoicePhase = { kind: "live", state: "listening" };
const run = (id: string, state: string, extra: Partial<VoiceRun> = {}): VoiceRun => ({ id, task: "修登录页失败的测试", state, msgIDs: [id], baseSeq: 1, ...extra });
const session = (extra: Partial<Session> = {}) => ({ machine_id: "m", session_id: "s", ...extra }) as Session;

function line(o: Partial<BarLineInput>) {
  return barLine({ phase: live, link: "live", runs: [], session: session(), cards: [], muted: false, label: "Claude", ...o });
}

describe("the voice bar's second line", () => {
  it("says to just speak, or that it is muted", () => {
    expect(line({})).toMatchObject({ kind: "ready", text: "直接说，会交给 Claude", actions: [] });
    expect(line({ muted: true })).toMatchObject({ kind: "muted", actions: [] });
  });

  it("hands over, then follows the work with how long and Stop", () => {
    expect(line({ runs: [run("r1", "sending")] })).toMatchObject({ kind: "handing", text: "正在交给 Claude…" });
    expect(line({ runs: [run("r1", "sending")], session: session({ starting: true }) })).toMatchObject({ kind: "handing", text: "正在把 Claude 拉起来…" });
    const since = "2026-11-06T10:00:00Z";
    const working = line({ runs: [run("r1", "working")], session: session({ state: "working", status: "working", activity: "在跑测试", state_since: since }) });
    expect(working).toEqual({ kind: "working", text: "Claude 在跑测试", since: Date.parse(since), actions: [{ kind: "stopAgent" }] });
    expect(sinceText(working.text, working.since, Date.parse(since) + 30_000)).toBe("Claude 在跑测试");
    expect(sinceText(working.text, working.since, Date.parse(since) + 125_000)).toBe("Claude 在跑测试 · 2 分钟");
    expect(sinceText(working.text, working.since, Date.parse(since) + 3_725_000)).toBe("Claude 在跑测试 · 1 小时 2 分钟");
  });

  it("offers no Stop for a conversation that cannot be interrupted from here", () => {
    const s = session({ state: "working", status: "working", source_kind: "codex_history", capabilities: { send: true, interrupt: false } as Session["capabilities"] });
    expect(line({ session: s }).actions).toEqual([]);
  });

  it("says the voice paused while the conversation works, with Stop", () => {
    expect(line({ link: "paused", session: session({ state: "working", status: "working" }) })).toMatchObject({ kind: "paused", text: "在等 Claude · 一开口就接上", actions: [{ kind: "stopAgent" }] });
  });

  it("says what a blocked conversation waits on", () => {
    expect(line({ session: session({ work: { phase: "blocked", on: "external", line: "部署完成" } }) })).toMatchObject({ kind: "waiting", text: "Claude 在等：部署完成" });
    expect(line({ runs: [run("r1", "blocked")] })).toMatchObject({ kind: "waiting", text: "Claude 在等别的事" });
  });

  it("asks for a tap, or marks the card being asked by voice", () => {
    const cards = [{ id: "ix1", kind: "question" as const }];
    expect(line({ cards })).toMatchObject({ kind: "needsTap" });
    expect(line({ cards, asking: "ix1" })).toMatchObject({ kind: "asking", text: "Claude 在问你，直接说出回答" });
    expect(line({ cards, asking: "gone" })).toMatchObject({ kind: "needsTap" });
  });

  it("says the computer is offline", () => {
    expect(line({ notice: { code: "machine_offline", message: "", fatal: false } })).toMatchObject({ kind: "offline" });
  });

  it("says why the last instruction did not go, with what can be done", () => {
    const refused = run("r2", "refused", { code: "input_draft", draft: "[⧉ Selected 3 lines from foo.go] fix it", clearable: "fix it" });
    expect(line({ runs: [run("r1", "working"), refused], resendable: ["r2"] })).toEqual({
      kind: "notSent", text: "Claude 的输入框里有电脑上没发出去的字：『[⧉ Selected 3 lines from foo.go] fix it』",
      actions: [{ kind: "clearAndSend", run: "r2" }, { kind: "dismiss", run: "r2" }],
    });
    // a run the connection up now no longer knows (after a reconnect): only Never Mind
    expect(line({ runs: [refused] }).actions).toEqual([{ kind: "dismiss", run: "r2" }]);
    expect(line({ runs: [run("r3", "refused", { code: "conflict" })], resendable: ["r3"] }).actions).toEqual([{ kind: "resend", run: "r3" }, { kind: "dismiss", run: "r3" }]);
    expect(line({ runs: [run("r4", "unknown")] }).text).toBe("不确定发出去没有，看一下对话");
    expect(line({ runs: [run("r5", "failed")] }).text).toBe("没能交给 Claude");
    expect(line({ runs: [run("r6", "refused", { code: "unprompted" })] }).text).toBe("这条不是你说的，没有发出去");
  });

  it("offers a plan card the refused words as feedback", () => {
    const l = line({ runs: [run("r1", "refused", { code: "dialog_open", task: "跳过第三步" })], cards: [{ id: "plan1", kind: "plan" }] });
    expect(l.actions).toEqual([{ kind: "returnPlan", card: "plan1", feedback: "跳过第三步", run: "r1" }, { kind: "dismiss", run: "r1" }]);
    expect(line({ runs: [run("r1", "refused", { code: "dialog_open" })] }).text).toBe("Claude 在等你先处理上面的卡片");
  });

  it("forgets a refusal put away, and one a later instruction took over", () => {
    const refused = run("r1", "refused", { code: "input_draft" });
    expect(line({ runs: [refused], dismissed: ["r1"] }).kind).toBe("ready");
    expect(line({ runs: [refused, run("r2", "working")], session: session({ state: "working", status: "working" }) }).kind).toBe("working");
    expect(line({ runs: [run("r0", "working"), run("r1", "folded")], session: session({ state: "working" }) }).kind).toBe("working");
  });

  it("says a held answer still works in the background", () => {
    expect(line({ runs: [run("r1", "held")] })).toMatchObject({ kind: "working", text: "Claude 答过了，后台还在跑" });
  });

  it("announces only what asks for the user or stops the voice", () => {
    expect(["needsTap", "asking", "notSent", "offline", "paused"].every((k) => announced(k as never))).toBe(true);
    expect(["ready", "muted", "handing", "working", "waiting"].some((k) => announced(k as never))).toBe(false);
  });
});

describe("the voice bar's first line", () => {
  const status = (phase: VoicePhase, o: Partial<Parameters<typeof barStatus>[0]> = {}) => barStatus({ phase, link: "live", muted: false, working: false, ...o });

  it("follows the call's phase", () => {
    expect(status({ kind: "connecting" })).toEqual({ text: "正在连接…", orb: "connecting" });
    expect(status({ kind: "reconnecting", attempt: 2 })).toEqual({ text: "重新连接中…", orb: "connecting" });
    expect(status({ kind: "ended", end: { kind: "lost" } })).toEqual({ text: "语音已结束", orb: "ended" });
    expect(status(live)).toEqual({ text: "在听", orb: "listening" });
    expect(status(live, { working: true })).toEqual({ text: "在听", orb: "working" });
    expect(status({ kind: "live", state: "hearing" })).toEqual({ text: "在听你说", orb: "hearing" });
    expect(status({ kind: "live", state: "speaking" }, { muted: true })).toEqual({ text: "正在说", orb: "speaking" });
    expect(status(live, { muted: true })).toEqual({ text: "已静音", orb: "muted" });
    expect(status(live, { tool: "Searching the knowledge base" })).toEqual({ text: "Searching the knowledge base…", orb: "listening" });
  });

  it("says when the upstream pauses, wakes or reconnects", () => {
    expect(status(live, { link: "paused" })).toEqual({ text: "语音已暂停", orb: "paused" });
    expect(status(live, { link: "waking" })).toEqual({ text: "正在接上…", orb: "connecting" });
    expect(status(live, { link: "reconnecting" })).toEqual({ text: "重新连接中…", orb: "connecting" });
  });
});
