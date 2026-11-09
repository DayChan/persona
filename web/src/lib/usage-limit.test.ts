import { describe, expect, it } from "vitest";
import { formatDate } from "../i18n";
import { sessionPreview } from "./format";
import { rowLine } from "./row-line";
import { interruptedAgent, makeSessionLookup, todoStatus, type Todo } from "./todos";
import type { Session } from "./types";
import { usageLimitClock, usageLimitLabel } from "./usage-limit";

const now = new Date(2026, 2, 4, 12, 0, 0).getTime(); // local noon, so +4h is still today
const at = (ms: number) => new Date(ms).toISOString();
const clock = { hour: "2-digit" as const, minute: "2-digit" as const, hourCycle: "h23" as const };

const session = (o: Partial<Session> = {}): Session =>
  ({ machine_id: "m", session_id: "s", cwd: "/x", title: "修登录", status: "done", state: "idle", turn_error: "You've hit your usage limit", turn_error_kind: "usage_limit", ...o }) as Session;

const today = at(now + 4 * 3600_000);
const tomorrow = at(new Date(2026, 2, 7, 10, 0, 0).getTime());

describe("usageLimitClock", () => {
  it("is the clock today and the date on another day, in the user's time zone", () => {
    expect(usageLimitClock(today, now)).toBe(formatDate(Date.parse(today), clock));
    expect(usageLimitClock(tomorrow, now)).toBe(formatDate(Date.parse(tomorrow), { month: "2-digit", day: "2-digit", ...clock }));
    expect(usageLimitClock(today, now)).not.toBe(usageLimitClock(tomorrow, now));
  });
});

describe("usageLimitLabel", () => {
  it("says it resumes when the CLI goes on, and when the hub will continue it", () => {
    const time = usageLimitClock(today, now);
    expect(usageLimitLabel(session({ turn_error_resets_at: today, turn_error_resumes: true }), now)).toBe(`用量上限 · ${time} 恢复`);
    expect(usageLimitLabel(session({ turn_error_resets_at: today, work: { phase: "blocked", on: "external", wake_at: today, resets_at: today } }), now)).toBe(`用量上限 · ${time} 恢复`);
  });

  it("says when it resets when nothing will continue it", () => {
    const time = usageLimitClock(tomorrow, now);
    expect(usageLimitLabel(session({ turn_error_resets_at: tomorrow }), now)).toBe(`用量上限 · ${time} 重置`);
    expect(usageLimitLabel(session({ work: { phase: "blocked", on: "external", resets_at: tomorrow } }), now)).toBe(`用量上限 · ${time} 重置`);
    // Another wait's wake is not this limit's continuation.
    expect(usageLimitLabel(session({ turn_error_resets_at: tomorrow, work: { phase: "blocked", on: "transient", wake_at: today } }), now)).toBe(`用量上限 · ${time} 重置`);
  });

  it("has no clock when the reset is not known", () => {
    expect(usageLimitLabel(session({ turn_error_resumes: true }), now)).toBe("用量上限 · 重置后恢复");
    expect(usageLimitLabel(session(), now)).toBe("用量上限");
  });

  it("is nothing for an older hub, a real interruption, a turn that is working, or a reset long past", () => {
    const cut = "API Error: 529 overloaded";
    expect(usageLimitLabel(session({ turn_error: cut, turn_error_kind: undefined, turn_error_resets_at: today }), now)).toBeUndefined();
    expect(usageLimitLabel(session({ turn_error: cut, turn_error_kind: "transient", turn_error_resets_at: today }), now)).toBeUndefined();
    expect(usageLimitLabel(session({ state: "working", status: "working", turn_error_resets_at: today, turn_error_resumes: true }), now)).toBeUndefined();
    const past = at(now - 11 * 60_000);
    expect(usageLimitLabel(session({ turn_error_resets_at: past, turn_error_resumes: true }), now)).toBeUndefined();
  });
});

describe("a usage limit is not interrupted", () => {
  const todo: Todo = {
    id: "t1",
    title: "修登录",
    created_at: "2026-11-02T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    agents: [{ machine_id: "m", session_id: "s", linked_at: "" }],
  };
  // Date.now(), not the frozen noon above: todoStatus and the row read the clock themselves.
  const soon = new Date(Date.now() + 4 * 3600_000).toISOString();
  const limited = session({ turn_error_resets_at: soon, turn_error_resumes: true });

  it("keeps an older hub's turn interrupted and a usage limit on its own status", () => {
    const cut = session({ turn_error: "API Error: 529 overloaded", turn_error_kind: undefined });
    expect(todoStatus(todo, makeSessionLookup([cut]))).toBe("interrupted");
    expect(interruptedAgent(cut)).toBe(true);
    expect(todoStatus(todo, makeSessionLookup([limited]))).toBe("usage_limit");
    expect(interruptedAgent(limited)).toBe(false);
    expect(sessionPreview(limited)).toBe(usageLimitLabel(limited));
    expect(rowLine(limited)?.text).toBe(usageLimitLabel(limited));
    expect(rowLine(session({ turn_error: "好了", turn_error_kind: undefined, last_text: "好了" }))).toBeUndefined();
  });
});
