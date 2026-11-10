// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getSession, getState, upsertSession } from "./store";
import type { Session } from "./types";
import type { UsageAccount } from "./usage-windows";
import {
  askSpent, dismissUltracodeLost, isUltracodeQueued, lostShown, quotaAge, quotaText, quotaWarnShown, resetUltracodeAsks, setUltracode, shownOn, startBlocked,
  ultracodeErrorText, ultracodeOffered, ultracodeOn, ultracodeQueueDone, ultracodeQuota, ultracodeRefused, ultracodeStartOffered, ultracodeSwitch,
  useUltracodeAsk, versionTooOld, type UltracodeAsk,
} from "./ultracode";
import { ApiError } from "./api";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const claude = (over: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: "s1", title: "t", status: "idle", agent: "claude", ...over }) as Session;
const FEATURES = ["ultracode"];

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("where the switch is offered", () => {
  it("a Claude Code agent whose effort can change, on a hub that has it", () => {
    expect(ultracodeOffered(claude(), FEATURES)).toBe(true);
    expect(ultracodeOffered(claude({ agent: undefined }), FEATURES)).toBe(true); // an older node names no agent: Claude Code
  });

  it("not on an older hub, nor before the hub said what it can do", () => {
    expect(ultracodeOffered(claude(), [])).toBe(false);
    expect(ultracodeOffered(claude(), ["terminal_stream"])).toBe(false);
  });

  it("not in a Persona conversation (the main one included), another agent, an offline one or a read-only source", () => {
    expect(ultracodeOffered(claude({ assistant: true }), FEATURES)).toBe(false);
    expect(ultracodeOffered(claude({ agent: "codex" }), FEATURES)).toBe(false);
    expect(ultracodeOffered(claude({ agent: "agy" }), FEATURES)).toBe(false);
    expect(ultracodeOffered(claude({ status: "offline" }), FEATURES)).toBe(false);
    expect(ultracodeOffered(claude({ source_kind: "claude_bg" }), FEATURES)).toBe(false);
    // Orca's Claude has the mode chips (capabilities.modes) but no terminal to type the switch into (#1843)
    expect(ultracodeOffered(claude({ source_kind: "runtime", capabilities: { read: true, send: true, modes: true } }), FEATURES)).toBe(false);
    expect(ultracodeOffered(undefined, FEATURES)).toBe(false);
  });

  it("the mark goes on what the node saw on; unknown is not on", () => {
    expect(ultracodeOn(claude({ ultracode: true }))).toBe(true);
    expect(ultracodeOn(claude({ ultracode: false }))).toBe(false);
    expect(ultracodeOn(claude())).toBe(false);
    expect(ultracodeOn(claude({ ultracode: true, assistant: true }))).toBe(false);
  });

  it("New agent offers it to Claude Code on a hub that has it", () => {
    expect(ultracodeStartOffered(FEATURES, "claude")).toBe(true);
    expect(ultracodeStartOffered(FEATURES, "codex")).toBe(false);
    expect(ultracodeStartOffered([], "claude")).toBe(false);
  });
});

describe("version gate", () => {
  it("a known version before 2.1.284 is too old; an unknown one may be tried", () => {
    expect(versionTooOld("2.1.283")).toBe(true);
    expect(versionTooOld("2.1.203")).toBe(true);
    expect(versionTooOld("1.9.999")).toBe(true);
    expect(versionTooOld("2.1.284")).toBe(false);
    expect(versionTooOld("2.1.320")).toBe(false);
    expect(versionTooOld("v2.2.0-beta")).toBe(false);
    expect(versionTooOld("")).toBe(false);
    expect(versionTooOld(undefined)).toBe(false);
    expect(versionTooOld("2.1.250 (Claude Code)")).toBe(false); // not one the node reads either: tried
  });

  it("says why New agent grays it", () => {
    expect(startBlocked("2.1.250")).toBe("这台电脑的 Claude Code 是 2.1.250，要 2.1.284 或更新才能开 ultracode");
    expect(startBlocked("2.1.320")).toBe("");
    expect(startBlocked(undefined)).toBe("");
  });
});

describe("the switch as drawn", () => {
  it("off: what it does, usable", () => {
    expect(ultracodeSwitch(claude({ ultracode: false }))).toEqual({ on: false, disabled: false, pending: false, line: "每个任务都编排多 Agent workflow · 很费额度 · 只对本会话", caution: false });
  });

  it("the node's reason takes the line's place in the caution colour, and the switch stays usable", () => {
    // fixed elsewhere and only cleared by new terminal evidence: the web must be able to try again
    for (const note of ["需要先在 Claude Code 设置中开启 dynamic workflows（工作流）", "未能确认 ultracode 状态，请在终端里确认", "电脑上的输入框里有没发出去的字"]) {
      expect(ultracodeSwitch(claude({ ultracode: false, ultracode_note: note }))).toEqual({ on: false, disabled: false, pending: false, line: note, caution: true });
    }
    // one that can't be fixed from here (an old Claude Code): the node refuses again at once, nothing typed
    expect(ultracodeSwitch(claude({ ultracode: false, ultracode_note: "请升级到 Claude Code 2.1.284 或更新版本，旧版不支持独立的 ultracode 开关" })).disabled).toBe(false);
    // on with a reason (a mismatch it could not confirm): still turns off
    expect(ultracodeSwitch(claude({ ultracode: true, ultracode_note: "ultracode 的实际状态与请求不一致，请在终端里确认" })).disabled).toBe(false);
  });

  it("a queued request this page made: \"Applies next turn\" beside where it was put, and it can be taken back", () => {
    const ask: UltracodeAsk = { on: true, sending: false, answered: undefined };
    const s = claude({ ultracode: false, ultracode_pending: true, status: "working" });
    expect(ultracodeSwitch(s, ask)).toMatchObject({ on: true, pending: true, disabled: false, line: "下一轮生效" });
    // a queued one held by the computer's draft (#5510): the reason after it
    expect(ultracodeSwitch({ ...s, ultracode_note: "终端里有草稿" }, ask).line).toBe("下一轮生效 · 终端里有草稿");
  });

  it("a change queued elsewhere: the node's value stands, and the line says a change waits without tying it to the switch", () => {
    const s = claude({ ultracode: false, ultracode_pending: true, status: "working" });
    expect(ultracodeSwitch(s)).toEqual({ on: false, disabled: false, pending: true, line: "有一次切换在排队，这一轮结束后生效", caution: false });
  });

  it("grayed only while its request is out", () => {
    expect(ultracodeSwitch(claude(), { on: true, sending: true })).toMatchObject({ on: true, disabled: true });
    expect(ultracodeSwitch(claude(), { on: true, sending: false, answered: claude() }).disabled).toBe(false);
  });

  it("the quota warning goes with a switch that is on or can simply be turned on", () => {
    expect(quotaWarnShown(ultracodeSwitch(claude({ ultracode: true })))).toBe(true);
    expect(quotaWarnShown(ultracodeSwitch(claude({ ultracode: false })))).toBe(true);
    // under a reason it can't be turned on for
    expect(quotaWarnShown(ultracodeSwitch(claude({ ultracode: false, ultracode_note: "请升级到 Claude Code 2.1.284 或更新版本" })))).toBe(false);
    // being turned off: on its way, or queued for the turn
    const on = claude({ ultracode: true });
    expect(quotaWarnShown(ultracodeSwitch(on, { on: false, sending: true }))).toBe(false);
    expect(quotaWarnShown(ultracodeSwitch({ ...on, ultracode_pending: true }, { on: false, sending: false, answered: on }))).toBe(false);
    // being turned on and queued: it will run on
    const off = claude({ ultracode: false, ultracode_pending: true });
    expect(quotaWarnShown(ultracodeSwitch(off, { on: true, sending: false, answered: off }))).toBe(true);
  });
});

describe("where the switch stands after a request", () => {
  it("a queued change holds what was asked through every push of the queue, not the value the node still sees", () => {
    // the node pushes the session (still at the old value, pending) before the request returns, then more pushes
    // while the turn runs (status, rev), then the result
    const on = claude({ ultracode: true, status: "working" });
    const queued = { ...on, ultracode_pending: true, rev: 2 };
    const sending: UltracodeAsk = { on: false, sending: true };
    expect(shownOn(queued, sending)).toBe(false);
    const answered: UltracodeAsk = { on: false, sending: false, answered: queued };
    for (const s of [queued, { ...queued, rev: 3 }, { ...queued, rev: 4, status: "working" }]) {
      expect(shownOn(s, answered)).toBe(false);
      expect(askSpent(s, answered)).toBe(false);
      expect(ultracodeSwitch(s, answered)).toMatchObject({ on: false, line: "下一轮生效" });
    }
    const applied = { ...on, ultracode: false, ultracode_pending: undefined, rev: 5, status: "idle" };
    expect(shownOn(applied, answered)).toBe(false);
    expect(askSpent(applied, answered)).toBe(true);
  });


  it("what was asked until the node's push answers it; then what the node saw", () => {
    const before = claude({ ultracode: false });
    const ask: UltracodeAsk = { on: true, sending: false, answered: before };
    expect(shownOn(before, ask)).toBe(true); // the push with the result has not come
    expect(askSpent(before, ask)).toBe(false);
    const after = { ...before, ultracode: true };
    expect(shownOn(after, ask)).toBe(true);
    expect(askSpent(after, ask)).toBe(true);
    const refusedLater = { ...before, ultracode_note: "需要先在 Claude Code 设置中开启 dynamic workflows（工作流）" };
    expect(shownOn(refusedLater, ask)).toBe(false);
  });
});

describe("requests", () => {
  beforeEach(() => {
    resetUltracodeAsks();
    Object.assign(getState(), { sessions: {} });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("turning it on sends only {ultracode: true} to …/modes", async () => {
    const fetcher = vi.fn(async () => response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    const s = claude({ ultracode: false });
    upsertSession(s);
    expect(await setUltracode(s, true)).toEqual({ ok: true });
    const [url, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toMatch(/\/api\/v1\/sessions\/mac\/s1\/modes$/);
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ ultracode: true });
  });

  it("turning it off sends {ultracode: false}", async () => {
    const fetcher = vi.fn(async () => response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    await setUltracode(claude({ ultracode: true }), false);
    expect(JSON.parse(String((fetcher.mock.calls[0] as unknown as [string, RequestInit])[1].body))).toEqual({ ultracode: false });
  });

  it("a refusal says the hub's words once and keeps nothing: the switch is usable again, and asking again asks the hub", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => response({ code: "node_outdated", error: "这台机器上的 personad 太旧，还不支持 ultracode，需要更新 node" }, 409)));
    const s = claude();
    upsertSession(s);
    expect(await setUltracode(s, true)).toEqual({ ok: false, error: "这台机器上的 personad 太旧，还不支持 ultracode，需要更新 node" });
    // nothing held for the session: once the computer's node is updated the switch works without reloading the page
    const seen: (UltracodeAsk | undefined)[] = [];
    function Probe() {
      seen.push(useUltracodeAsk(s));
      return null;
    }
    const host = document.createElement("div");
    const root = createRoot(host);
    act(() => root.render(createElement(Probe)));
    expect(seen.at(-1)).toBeUndefined();
    expect(ultracodeSwitch(s, seen.at(-1))).toMatchObject({ on: false, disabled: false });
    act(() => root.unmount());
    vi.stubGlobal("fetch", vi.fn(async () => response({ code: "ultracode_unsupported", error: "" }, 409)));
    expect(await setUltracode(claude({ session_id: "s2" }), true)).toEqual({ ok: false, error: "这个会话不能开 ultracode" });
    vi.stubGlobal("fetch", vi.fn(async () => response({ error: "未能确认 ultracode 状态，请在终端里确认" }, 502)));
    expect(await setUltracode(claude({ session_id: "s3" }), true)).toEqual({ ok: false, error: "未能确认 ultracode 状态，请在终端里确认" });
  });

  it("knows the refusals a start can't get past by asking again", () => {
    expect(ultracodeRefused(new ApiError(409, "旧", { code: "node_outdated", error: "旧" }))).toBe(true);
    expect(ultracodeRefused(new ApiError(409, "不能", { code: "ultracode_unsupported", error: "不能" }))).toBe(true);
    expect(ultracodeRefused(new ApiError(409, "草稿", { code: "input_draft", error: "草稿" }))).toBe(false);
    expect(ultracodeRefused(new Error("x"))).toBe(false);
  });

  it("dismissing the resume notice sends {ultracode_dismiss_lost: true} and takes it away at once; it comes back on failure", async () => {
    const fetcher = vi.fn(async () => response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    const s = claude({ ultracode: false, ultracode_lost_at: "2026-11-06T08:00:00Z" });
    upsertSession(s);
    const job = dismissUltracodeLost(s);
    expect(getSession("mac", "s1")?.ultracode_lost_at).toBeUndefined();
    expect(await job).toEqual({ ok: true });
    expect(JSON.parse(String((fetcher.mock.calls[0] as unknown as [string, RequestInit])[1].body))).toEqual({ ultracode_dismiss_lost: true });

    vi.stubGlobal("fetch", vi.fn(async () => response({ error: "offline" }, 503)));
    upsertSession(s);
    expect((await dismissUltracodeLost(s)).ok).toBe(false);
    expect(getSession("mac", "s1")?.ultracode_lost_at).toBe("2026-11-06T08:00:00Z");
  });

  it("names the hub's own refusals and a node's reason, not \"hub unavailable\"", () => {
    expect(ultracodeErrorText(new ApiError(502, "Claude Code 未接受 ultracode 开关", { error: "Claude Code 未接受 ultracode 开关" }))).toBe("Claude Code 未接受 ultracode 开关");
    expect(ultracodeErrorText(new ApiError(502, "HTTP 502", { error: "<html>" }, false))).not.toBe("<html>");
  });
});

describe("the resume notice", () => {
  it("shows while lost_at is set and it is not back on", () => {
    expect(lostShown(claude({ ultracode: false, ultracode_lost_at: "2026-11-06T08:00:00Z" }), FEATURES)).toBe(true);
    expect(lostShown(claude({ ultracode: true, ultracode_lost_at: "2026-11-06T08:00:00Z" }), FEATURES)).toBe(false);
    expect(lostShown(claude({ ultracode: false }), FEATURES)).toBe(false);
    expect(lostShown(claude({ ultracode: false, ultracode_lost_at: "2026-11-06T08:00:00Z" }), [])).toBe(false); // an older hub
    expect(lostShown(claude({ ultracode: false, ultracode_lost_at: "2026-11-06T08:00:00Z", assistant: true }), FEATURES)).toBe(false);
  });
});

describe("quota under the switch", () => {
  const now = Date.parse("2026-11-06T10:00:00Z");
  const iso = (ms: number) => new Date(ms).toISOString();
  const win = (label: string, mins: number, used: number, model?: string) => ({ label, window_minutes: mins, used_percent: used, resets_at: iso(now + 3600_000), ...(model ? { model } : {}) });
  const account = (over: Partial<UsageAccount>): UsageAccount => ({ id: "x", provider: "claude", name: "Claude", windows: [], sampled_at: iso(now - 60_000), ...over });

  it("nothing without a Claude account", () => {
    expect(ultracodeQuota([], now)).toBeNull();
    expect(ultracodeQuota([account({ provider: "codex", windows: [win("5h", 300, 40)] })], now)).toBeNull();
    expect(ultracodeQuota([account({ windows: [win("7d", 10080, 10, "Fable")] })], now)).toBeNull(); // only a per-model allowance
  });

  it("the 5-hour and weekly use, and the warning past 80%", () => {
    const q = ultracodeQuota([account({ windows: [win("5h", 300, 42.4), win("7d", 10080, 61)] })], now)!;
    expect(q).toEqual({ name: "Claude", windows: [{ kind: "5h", percent: 42.4 }, { kind: "week", percent: 61 }], warn: false, stale: false, sampled_at: iso(now - 60_000) });
    expect(quotaAge(q, now)).toBe("");
    expect(quotaText(q)).toBe("5 小时 42% · 每周 61%");
    expect(ultracodeQuota([account({ windows: [win("5h", 300, 86), win("7d", 10080, 41)] })], now)!.warn).toBe(true);
  });

  it("an account read on the computers first, then the fullest", () => {
    const gateway = account({ id: "g", name: "pool", sources: ["Relay"], windows: [win("5h", 300, 95)] });
    const local = account({ id: "l", name: "work", local: true, windows: [win("5h", 300, 20)] });
    expect(ultracodeQuota([gateway, local], now)!.name).toBe("work");
    const fuller = account({ id: "f", name: "fuller", local: true, windows: [win("5h", 300, 70)] });
    expect(ultracodeQuota([local, fuller], now)!.name).toBe("fuller");
  });

  it("says when its numbers are out of date, and puts any fresh account first (#1906)", () => {
    const old = account({ id: "o", name: "work", local: true, stale: true, sampled_at: iso(now - 2 * 3600_000), windows: [win("7d", 10080, 62)] });
    const q = ultracodeQuota([old], now)!;
    expect(q.stale).toBe(true);
    expect(quotaAge(q, now)).toBe("已过期 · 2小时前");
    const pool = account({ id: "p", name: "pool", sources: ["Relay"], windows: [win("7d", 10080, 30)] }); // fresh, only a gateway has it
    expect(ultracodeQuota([old, pool], now)!.name).toBe("pool");
    const staleGateway = account({ id: "g", name: "stale pool", sources: ["Relay"], stale: true, windows: [win("7d", 10080, 90)] });
    expect(ultracodeQuota([staleGateway, old], now)!.name).toBe("work"); // both old: the computer's still first
  });
});

describe("a message while a change waits for the turn", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Object.assign(getState(), { sessions: {} });
  });
  afterEach(() => vi.useRealTimers());

  it("knows the node's hold-back", () => {
    expect(isUltracodeQueued(new ApiError(409, "等", { code: "ultracode_pending", error: "ultracode 等这一轮结束后生效，请稍后再发送消息" }))).toBe(true);
    expect(isUltracodeQueued(new ApiError(409, "草稿", { code: "input_draft" }))).toBe(false);
  });

  it("goes on once the session's queue is empty, not before a push could have said so", async () => {
    upsertSession(claude({ ultracode_pending: true, status: "working" }));
    let through: boolean | undefined;
    void ultracodeQueueDone("mac", "s1").then((v) => (through = v));
    await vi.advanceTimersByTimeAsync(5000);
    expect(through).toBeUndefined();
    upsertSession(claude({ ultracode: true, ultracode_pending: false, status: "idle" }));
    await vi.advanceTimersByTimeAsync(1000);
    expect(through).toBe(true);
    // already through when asked: still waits the first beat
    let early: boolean | undefined;
    void ultracodeQueueDone("mac", "s1").then((v) => (early = v));
    await vi.advanceTimersByTimeAsync(1000);
    expect(early).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1000);
    expect(early).toBe(true);
  });

  it("gives up after its time, or when the session is gone", async () => {
    upsertSession(claude({ ultracode_pending: true }));
    let late: boolean | undefined;
    void ultracodeQueueDone("mac", "s1", { maxMs: 10_000 }).then((v) => (late = v));
    await vi.advanceTimersByTimeAsync(11_000);
    expect(late).toBe(false);
    let gone: boolean | undefined;
    void ultracodeQueueDone("mac", "nope").then((v) => (gone = v));
    await vi.advanceTimersByTimeAsync(1000);
    expect(gone).toBe(false);
  });
});
