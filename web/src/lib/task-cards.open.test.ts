import { describe, expect, it } from "vitest";
import type { TaskPlan } from "./feed";
import { openTaskCard, tasksToStart, type TaskCardMetadata } from "./task-cards";
import type { Todo } from "./todos";

const at = "2026-11-10T08:00:00Z";
const card = (extra: Partial<TaskCardMetadata>): TaskCardMetadata => ({ id: "slot", at, updated_at: at, state: "not_started", ...extra });
const todo = (extra: Partial<Todo> = {}): Todo => ({ id: "t", title: "Demo", created_at: at, updated_at: at, ...extra });
const plan = (id: string, state: TaskPlan["state"], conversation = "c", n = 1): TaskPlan => ({ id, conversation, state, created_at: at, tasks: Array.from({ length: n }, (_, i) => ({ title: `Task ${i}`, prompt: "p", cwd: "/demo", machine: "demo" })) });

describe("openTaskCard", () => {
  it("keeps a task open until it is closed, whatever its agent is doing", () => {
    expect(openTaskCard(card({ todo_id: "t" }), todo(), {})).toBe(true);
    expect(openTaskCard(card({ todo_id: "t", state: "accepted" }), todo(), {})).toBe(true);
    expect(openTaskCard(card({ todo_id: "t" }), todo({ done_at: at }), {})).toBe(false);
    expect(openTaskCard(card({ todo_id: "t" }), todo({ start_error: "no agent" }), {})).toBe(false);
    // the hub no longer sends it: closed
    expect(openTaskCard(card({ todo_id: "t" }), undefined, {})).toBe(false);
  });

  it("keeps a plan task open while its plan waits for Start", () => {
    const plans = { open: plan("open", "open"), gone: plan("gone", "dismissed") };
    expect(openTaskCard(card({ plan_id: "open", index: 0 }), undefined, plans)).toBe(true);
    expect(openTaskCard(card({ plan_id: "gone", index: 0 }), undefined, plans)).toBe(false);
    expect(openTaskCard(card({ plan_id: "missing", index: 0 }), undefined, plans)).toBe(false);
  });
});

describe("tasksToStart", () => {
  it("counts the tasks of this conversation's open plans only", () => {
    const plans = { a: plan("a", "open", "c", 2), b: plan("b", "open", "c"), c: plan("c", "started", "c", 3), d: plan("d", "open", "other", 4) };
    expect(tasksToStart("c", plans)).toBe(3);
    expect(tasksToStart("none", plans)).toBe(0);
  });
});
