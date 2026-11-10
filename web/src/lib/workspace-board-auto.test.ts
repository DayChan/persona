import { describe, expect, it } from "vitest";
import { autoActivity, autoFocusSession, autoLaneOf, autoLanes, autoSort } from "./workspace-board-auto";
import type { BoardCard } from "./workspace-board";
import type { Session } from "./types";

function session(partial: Partial<Session>): Session {
  return { machine_id: "m", session_id: "s", title: "s", cwd: "/w", status: "idle", ...partial };
}
function card(id: string, partial: Partial<BoardCard> = {}): BoardCard {
  return { id, machine_id: "m", workspace_id: id, title: id, cwd: "/w", source: "test", column_id: "c", pinned: false, archived: false, ...partial };
}

describe("autoLaneOf", () => {
  it("puts a card where its most urgent agent is", () => {
    expect(autoLaneOf([])).toBe("idle");
    expect(autoLaneOf([session({ status: "offline" })])).toBe("idle");
    expect(autoLaneOf([session({ state: "idle" })])).toBe("review");
    expect(autoLaneOf([session({ state: "idle" }), session({ state: "working" })])).toBe("working");
    expect(autoLaneOf([session({ state: "working" }), session({ state: "needs_you" })])).toBe("needs_you");
    expect(autoLaneOf([session({ state: "needs_you" }), session({ status: "offline" })])).toBe("needs_you");
    expect(autoLaneOf([session({ state: "idle" }), session({ status: "offline" })])).toBe("review");
  });

  it("follows the agents as they change, with nothing stored", () => {
    const sessions = [session({ state: "working" })];
    expect(autoLaneOf(sessions)).toBe("working");
    sessions[0] = session({ state: "needs_you" });
    expect(autoLaneOf(sessions)).toBe("needs_you");
    sessions[0] = session({ state: "idle" });
    expect(autoLaneOf(sessions)).toBe("review");
  });
});

describe("autoFocusSession", () => {
  it("opens the session needing you, then the working one, then the latest", () => {
    const old = session({ session_id: "old", state: "working", updated_at: "2026-11-09T01:00:00Z" });
    const recent = session({ session_id: "recent", state: "working", updated_at: "2026-11-09T02:00:00Z" });
    const needy = session({ session_id: "needy", state: "needs_you", updated_at: "2026-11-08T00:00:00Z" });
    expect(autoFocusSession([old, recent])?.session_id).toBe("recent");
    expect(autoFocusSession([old, recent, needy])?.session_id).toBe("needy");
    expect(autoFocusSession([])).toBeUndefined();
  });
});

describe("autoSort / autoLanes", () => {
  const byCard = new Map<string, Session[]>([
    ["quiet", []],
    ["busy", [session({ state: "working", updated_at: "2026-11-09T02:00:00Z" })]],
    ["stale", [session({ state: "working", updated_at: "2026-11-09T01:00:00Z" })]],
    ["done", [session({ state: "idle", updated_at: "2026-11-09T03:00:00Z" })]],
  ]);
  const sessionsOf = (c: BoardCard) => byCard.get(c.id) ?? [];

  it("orders by recent activity, then pinned, then title", () => {
    const cards = [card("stale"), card("busy"), card("quiet", { pinned: true })];
    expect(autoSort(cards, sessionsOf).map((c) => c.id)).toEqual(["busy", "stale", "quiet"]);
  });

  it("groups every card into exactly one lane", () => {
    const lanes = autoLanes([...byCard.keys()].map((id) => card(id)), sessionsOf);
    expect(lanes.map((l) => l.lane)).toEqual(["needs_you", "working", "review", "idle"]);
    expect(lanes.find((l) => l.lane === "working")?.cards.map((c) => c.id)).toEqual(["busy", "stale"]);
    expect(lanes.find((l) => l.lane === "review")?.cards.map((c) => c.id)).toEqual(["done"]);
    expect(lanes.find((l) => l.lane === "idle")?.cards.map((c) => c.id)).toEqual(["quiet"]);
    expect(lanes.flatMap((l) => l.cards)).toHaveLength(byCard.size);
  });
});

describe("autoActivity", () => {
  it("ignores unparsable dates", () => {
    expect(autoActivity([session({ updated_at: "not a date" })])).toBe(0);
    expect(autoActivity([session({ updated_at: "2026-11-09T02:00:00Z" })])).toBeGreaterThan(0);
  });
});

describe("all work in one panel", () => {
  const bound = [card("ws", { machine_id: "m", cwd: "/code/app" })];

  it("brings in sessions no workspace card claims, but not the main chat", async () => {
    const { unboundSessions, sessionItem } = await import("./workspace-board-auto");
    const inside = session({ session_id: "in", cwd: "/code/app", state: "working" });
    const outside = session({ session_id: "out", cwd: "/code/other", state: "needs_you" });
    const main = session({ session_id: "main", cwd: "/code/other", assistant: true });
    expect(unboundSessions([inside, outside, main], bound).map((s) => s.session_id)).toEqual(["out"]);
    expect(sessionItem(outside).lane).toBe("needs_you");
    expect(sessionItem(inside).lane).toBe("working");
    expect(sessionItem(session({ state: "idle" })).lane).toBe("review");
    expect(sessionItem(session({ status: "offline" })).lane).toBe("idle");
  });

  it("maps to-dos onto the lanes through todoStatus", async () => {
    const { todoItem } = await import("./workspace-board-auto");
    const base = { id: "t1", title: "Write the report", created_at: "2026-11-09T00:00:00Z", updated_at: "2026-11-09T01:00:00Z" };
    expect(todoItem(base).lane).toBe("planned"); // no agent attached
    expect(todoItem({ ...base, done_at: "2026-11-09T02:00:00Z" }).lane).toBe("review");
    expect(todoItem({ ...base, start_error: "no launcher" }).lane).toBe("needs_you");
    expect(todoItem({ ...base, agents: [{ machine_id: "m", session_id: "s1", linked_at: "x" }] }, () => session({ state: "working" })).lane).toBe("working");
    expect(todoItem({ ...base, agents: [{ machine_id: "m", session_id: "s1", linked_at: "x" }] }, () => session({ state: "needs_you" })).lane).toBe("needs_you");
  });

  it("groups every kind of work and keeps the planned lane", async () => {
    const { workspaceItem, sessionItem, todoItem, workLanes } = await import("./workspace-board-auto");
    const items = [
      workspaceItem(card("ws"), [session({ state: "idle" })]),
      sessionItem(session({ session_id: "out", state: "needs_you" })),
      todoItem({ id: "t", title: "todo", created_at: "", updated_at: "" }),
    ];
    const lanes = workLanes(items);
    expect(lanes.map((l) => l.lane)).toEqual(["needs_you", "working", "review", "planned", "idle"]);
    expect(lanes.find((l) => l.lane === "needs_you")?.items[0]?.kind).toBe("session");
    expect(lanes.find((l) => l.lane === "review")?.items[0]?.kind).toBe("workspace");
    expect(lanes.find((l) => l.lane === "planned")?.items[0]?.kind).toBe("todo");
    expect(lanes.flatMap((l) => l.items)).toHaveLength(3);
  });
});
