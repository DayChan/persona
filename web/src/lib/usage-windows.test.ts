import { describe, expect, it } from "vitest";
import { countdown, fullest, liveAccounts, pace, planNote, resetLabel, sampleAge, sourceNames, topAccounts, windowKey, windowMinutes, type UsageAccount, type UsageWindow } from "./usage-windows";

const NOW = new Date(2026, 10, 5, 10, 0, 0).getTime(); // local time, so same-day checks do not depend on the zone
const at = (ms: number) => new Date(NOW + ms).toISOString();
const H = 3600_000;
const win = (label: string, minutes: number, used: number, resetsIn?: number): UsageWindow => ({ label, window_minutes: minutes, used_percent: used, resets_at: resetsIn === undefined ? undefined : at(resetsIn) });

describe("window length", () => {
  it("reads the label", () => {
    expect(windowMinutes({ label: "5h", window_minutes: 0 })).toBe(300);
    expect(windowMinutes({ label: "7d", window_minutes: 0 })).toBe(10080);
    expect(windowMinutes({ label: "45m", window_minutes: 0 })).toBe(45);
    expect(windowMinutes({ label: "1w", window_minutes: 0 })).toBe(10080);
  });
  it("falls back to the given minutes", () => {
    expect(windowMinutes({ label: "月", window_minutes: 600 })).toBe(600);
    expect(windowMinutes({ label: "月", window_minutes: 0 })).toBe(0);
  });
});

describe("pace", () => {
  it("is over when use is more than 5 points above an even spread", () => {
    // 5h window, 2h elapsed = 40% expected
    expect(pace(win("5h", 300, 50, 3 * H), NOW)).toEqual({ expected: 40, state: "over" });
  });
  it("is under when more than 5 below", () => {
    expect(pace(win("5h", 300, 30, 3 * H), NOW)?.state).toBe("under");
  });
  it("is on pace within 5 points, edges included", () => {
    expect(pace(win("5h", 300, 45, 3 * H), NOW)?.state).toBe("on");
    expect(pace(win("5h", 300, 35, 3 * H), NOW)?.state).toBe("on");
    expect(pace(win("5h", 300, 45.1, 3 * H), NOW)?.state).toBe("over");
  });
  it("says nothing in the first 5% of the window", () => {
    // 7d window: 5% is 8.4h
    expect(pace(win("7d", 10080, 90, 7 * 24 * H - 8 * H), NOW)).toBeNull();
    expect(pace(win("7d", 10080, 90, 7 * 24 * H - 9 * H), NOW)?.state).toBe("over");
  });
  it("needs a reset time and a length, and a time inside the window", () => {
    expect(pace(win("5h", 300, 50), NOW)).toBeNull();
    expect(pace(win("?", 0, 50, H), NOW)).toBeNull();
    expect(pace(win("5h", 300, 50, 6 * H), NOW)).toBeNull(); // not started: the reset is further than the length
    expect(pace(win("5h", 300, 50, -H), NOW)).toBeNull(); // already reset
  });
});

describe("countdown", () => {
  it("is minutes within the hour", () => {
    expect(countdown(at(45 * 60_000), NOW)).toBe("45m");
    expect(countdown(at(30_000), NOW)).toBe("1m");
  });
  it("is the clock time later the same day", () => {
    expect(countdown(at(3 * H + 5 * 60_000), NOW)).toBe("13:05");
  });
  it("is days and hours beyond today", () => {
    expect(countdown(at(2 * 24 * H + 3 * H), NOW)).toBe("2d 3h");
    expect(countdown(at(15 * H), NOW)).toBe("15h"); // tomorrow, under a day
  });
  it("handles a due or missing time", () => {
    expect(countdown(at(-1000), NOW)).toBe("即将重置");
    expect(countdown(undefined, NOW)).toBe("");
    expect(countdown("soon", NOW)).toBe("");
  });
});

describe("reset phrase", () => {
  it("is relative for minutes and days, a clock time for later today", () => {
    expect(resetLabel(at(45 * 60_000), NOW)).toBe("45m 后重置");
    expect(resetLabel(at(3 * H + 5 * 60_000), NOW)).toBe("13:05 重置");
    expect(resetLabel(at(2 * 24 * H + 3 * H), NOW)).toBe("2d 3h 后重置");
    expect(resetLabel(at(-1), NOW)).toBe("即将重置");
    expect(resetLabel(undefined, NOW)).toBe("");
  });
});

describe("sample age", () => {
  it("counts seconds first", () => {
    expect(sampleAge(at(-12_000), NOW)).toBe("12秒钟前");
    expect(sampleAge(at(5_000), NOW)).toBe("0秒钟前"); // a clock a little ahead
    expect(sampleAge(at(-5 * 60_000), NOW)).toBe("5分钟前");
    expect(sampleAge(at(-2 * H), NOW)).toBe("2小时前");
    expect(sampleAge(at(-50 * H), NOW)).toBe("2天前");
    expect(sampleAge("x", NOW)).toBe("");
  });
});

const account = (id: string, used: number, o: Partial<UsageAccount> = {}): UsageAccount => ({ id, provider: "codex", name: id, windows: [win("7d", 10080, used, 24 * H)], sampled_at: at(-60_000), ...o });

describe("which accounts show", () => {
  it("drops samples older than three days and windows that reset", () => {
    const got = liveAccounts(
      [account("old", 10, { sampled_at: at(-73 * H) }), account("ok", 10), { ...account("mixed", 10), windows: [win("5h", 300, 99, -H), win("7d", 10080, 3, 24 * H)] }, { ...account("none", 1), windows: [win("5h", 300, 99, -H)] }],
      NOW,
    );
    expect(got.map((a) => a.id)).toEqual(["ok", "mixed"]);
    expect(got[1].windows.map((w) => w.label)).toEqual(["7d"]);
  });
  it("ages kept numbers by stale_at, whatever the hub said when it answered (#1905)", () => {
    const kept = account("kept", 10, { stale_at: at(-H) }); // fresh when the hub answered, an hour past its time now
    const fresh = account("fresh", 10, { stale_at: at(H) });
    const older = account("older", 10); // an older hub sends no stale_at: its own mark stands
    const got = liveAccounts([kept, fresh, older], NOW);
    expect(got.map((a) => Boolean(a.stale))).toEqual([true, false, false]);
    expect(topAccounts(got).map((a) => a.id)).toEqual(["fresh", "older"]); // stale last
    expect(liveAccounts([account("flagged", 10, { stale: true, stale_at: at(H) })], NOW)[0].stale).toBe(true);
  });
  it("takes the two fullest, stale last", () => {
    const list = [account("a", 20), account("b", 90, { stale: true }), account("c", 60), account("d", 40)];
    expect(topAccounts(list).map((a) => a.id)).toEqual(["c", "d"]);
    expect(topAccounts([account("a", 20), account("b", 90, { stale: true })]).map((a) => a.id)).toEqual(["a", "b"]);
    expect(fullest({ ...account("x", 1), windows: [win("5h", 300, 30), win("7d", 10080, 70)] })).toBe(70);
  });
});

describe("accounts with several sources and per-model windows (#2595)", () => {
  const account = (o: Partial<UsageAccount> = {}): UsageAccount => ({ id: "a", provider: "claude", name: "seat", sampled_at: at(-1000), windows: [win("5h", 300, 30, 2 * H), win("7d", 10080, 40, 24 * H)], ...o });
  it("sorts by the plan's own fullest window, not a model's that ran out", () => {
    const scoped = { ...win("7d", 10080, 100, 24 * H), model: "Fable" };
    const a = account({ windows: [win("7d", 10080, 40, 24 * H), scoped] });
    expect(fullest(a)).toBe(40);
    expect(fullest(account({ windows: [scoped] }))).toBe(100); // nothing else to go by
    expect(topAccounts([account({ id: "x", windows: [scoped, win("7d", 10080, 10, 24 * H)] }), account({ id: "y" })], 1)[0].id).toBe("y");
  });
  it("names where an account was seen", () => {
    expect(sourceNames(account({ local: true, machines: ["MacBook", "Studio"], sources: ["Relay"] }))).toEqual(["MacBook", "Studio", "Relay"]);
    expect(sourceNames(account({ local: true, sources: ["Relay"] }))).toEqual(["电脑", "Relay"]); // an older hub: no names, never "this computer" (#1907)
    expect(sourceNames(account({ sources: ["Relay", "Team pool"] }))).toEqual(["Relay", "Team pool"]);
    expect(sourceNames(account())).toEqual([]); // an older hub says nothing about it
  });
  it("shows the plan unless the name has it", () => {
    expect(planNote(account({ name: "Codex Pro", plan: "pro" }))).toBe("");
    expect(planNote(account({ name: "work seat", plan: "Pro" }))).toBe("Pro");
    expect(planNote(account({ plan: " " }))).toBe("");
  });
  it("tells windows of one length apart by model", () => {
    expect(windowKey(win("7d", 10080, 1))).not.toBe(windowKey({ ...win("7d", 10080, 1), model: "Fable" }));
  });
});
