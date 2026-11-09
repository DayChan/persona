import { describe, expect, it } from "vitest";
import { othersWaiting, stateCounts } from "./sessions";
import type { AgentState, Session } from "./types";

const session = (id: string, state: AgentState, patch: Partial<Session> = {}): Session => ({
  machine_id: "m", session_id: id, title: id, cwd: "/tmp/p", status: state, state, updated_at: "2026-01-01T10:00:00Z", ...patch,
});

describe("侧边栏的需要你角标（#1731）", () => {
  it("安静任务的 Agent 照样按状态计数，但不进角标", () => {
    const all = [session("a", "needs_you"), session("q", "needs_you", { quiet: true }), session("w", "working", { quiet: true }), session("u", "unread")];
    expect(stateCounts(all)).toEqual({ needs: 2, working: 1, unread: 1, badge: 1 });
  });
});

describe("othersWaiting", () => {
  it("leaves out the open agent only when the badge counted it", () => {
    expect(othersWaiting(3, session("a", "needs_you"))).toBe(2);
    expect(othersWaiting(1, session("a", "needs_you"))).toBe(0);
    expect(othersWaiting(3, session("a", "working"))).toBe(3);
    expect(othersWaiting(3, session("q", "needs_you", { quiet: true }))).toBe(3);
    expect(othersWaiting(1, session("q", "needs_you", { quiet: true }), true)).toBe(0);
    expect(othersWaiting(3, undefined)).toBe(3);
  });
});
