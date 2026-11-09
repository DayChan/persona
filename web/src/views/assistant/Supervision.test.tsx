import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { FeedItem } from "../../lib/feed";
import type { Todo } from "../../lib/todos";
import { FeedLineView } from "./Tasks";
import { ReviewBadge, ReviewChecklist } from "./Supervision";

const todo: Todo = {
  id: "todo_1",
  title: "部署 hub",
  created_at: "2026-11-05T08:00:00Z",
  updated_at: "2026-11-05T08:00:00Z",
  supervision: {
    supervisor: "main",
    done_when: ["PR 合并到 master", "浅色 / 深色截图贴进 PR"],
    expect_minutes: 30,
    state: "rework",
    round: 1,
    reviews: [{ at: "2026-11-05T09:00:00Z", verdict: "rework", round: 1, note: "补深色截图",
      checks: [{ criterion: "PR 合并到 master", met: true, evidence: "pull/901 merged" }, { criterion: "浅色 / 深色截图贴进 PR", met: false, evidence: "只有浅色" }] }],
  },
};

describe("a supervised task", () => {
  it("tints accepted work green but keeps reviews requiring user input neutral", () => {
    const accepted = renderToStaticMarkup(<ReviewBadge todo={{ ...todo, done_at: "2026-11-05T10:00:00Z", supervision: { ...todo.supervision!, state: "accepted" } }} />);
    expect(accepted).toContain("bg-green-tint text-green");
    const pending = renderToStaticMarkup(<ReviewBadge todo={{ ...todo, supervision: { ...todo.supervision!, state: "needs_user" } }} />);
    expect(pending).toContain("bg-field text-orange");
    expect(pending).not.toContain("bg-orange-tint");
  });

  it("shows its review state and the criteria met on its card", () => {
    const html = renderToStaticMarkup(<ReviewBadge todo={todo} />);
    expect(html).toContain("返工第 1 轮");
    expect(html).toContain("1/2");
    expect(renderToStaticMarkup(<ReviewBadge todo={{ ...todo, supervision: undefined }} />)).toBe("");
  });

  it("lists each criterion with its evidence or gap on its page", () => {
    const html = renderToStaticMarkup(<ReviewChecklist todo={todo} />);
    expect(html).toContain("验收标准");
    expect(html).toContain("预计 30 分钟");
    expect(html).toContain('aria-label="达成"');
    expect(html).toContain('aria-label="缺口"');
    expect(html).toContain("只有浅色");
    expect(html).toContain("督办说明：补深色截图");
  });

  it("says when no criteria were given", () => {
    const html = renderToStaticMarkup(<ReviewChecklist todo={{ ...todo, supervision: { supervisor: "main", done_when: [] } }} />);
    expect(html).toContain("派活时没写验收标准");
  });

  it("draws an agent's report to Persona in the conversation, with its evidence", () => {
    const item: FeedItem = { id: "f2", conversation: "main", at: "2026-11-05T09:00:00Z", kind: "report", ref: "todo_1",
      body: { kind: "question", text: "A 先灰度还是 B 直接全量？", evidence: ["https://github.com/o/r/pull/902"] } };
    const html = renderToStaticMarkup(<FeedLineView item={item} />);
    expect(html).toContain('data-testid="report-line"');
    expect(html).toContain("向 Persona 汇报");
    expect(html).toContain("问 Persona：A 先灰度还是 B 直接全量？");
    expect(html).toContain("pull/902");
  });

  it("marks a question that can wait, and draws it quietly", () => {
    const say = (body: Record<string, unknown>): string =>
      renderToStaticMarkup(<FeedLineView item={{ id: "f4", conversation: "main", at: "2026-11-05T09:00:00Z", kind: "report", ref: "todo_1", body }} />);
    const later = say({ kind: "question", text: "图标要不要换？", later: true });
    expect(later).toContain('data-testid="report-later"');
    expect(later).toContain("不急，等下次早晚报");
    expect(later).not.toContain("text-orange");
    const urgent = say({ kind: "question", text: "图标要不要换？" });
    expect(urgent).not.toContain("report-later");
    expect(urgent).toContain("text-orange");
  });

  it("marks a line to a task that cut its turn short", () => {
    const say = (body: Record<string, unknown>): string =>
      renderToStaticMarkup(<FeedLineView item={{ id: "f3", conversation: "main", at: "2026-11-05T09:00:00Z", kind: "routed", ref: "todo_1", body }} />);
    expect(say({ text: "先修测试", interrupted: true })).toContain("打断「");
    expect(say({ text: "先修测试" })).not.toContain("打断「");
    expect(say({ text: "先修测试" })).toContain("对「");
  });

  it("shows the independent review's result under the criteria, and nothing when none ran", () => {
    expect(renderToStaticMarkup(<ReviewChecklist todo={todo} />)).not.toContain("auto-review");
    const reviewed = (auto_review: NonNullable<Todo["supervision"]>["auto_review"]): string =>
      renderToStaticMarkup(<ReviewChecklist todo={{ ...todo, supervision: { ...todo.supervision!, state: "pending_review", auto_review } }} />);
    const clean = reviewed({ id: "rv_1", state: "clean", count: 2, blocking: 0, findings: "P2 app.ts:3 命名" });
    expect(clean).toContain("独立评审");
    expect(clean).toContain("没有阻断问题 · 2 条发现");
    expect(clean).toContain("展开报告");
    expect(clean).not.toContain("P2 app.ts:3"); // the report opens on a click
    expect(reviewed({ id: "rv_1", state: "blocking", count: 3, blocking: 1, findings: "P0 x" })).toContain("还剩 1 条阻断问题 · 3 条发现");
    expect(reviewed({ id: "rv_1", state: "running" })).toContain("进行中");
    expect(reviewed({ id: "rv_1", state: "failed" })).toContain("没评审完");
    expect(reviewed({ id: "rv_1", state: "cancelled" })).not.toContain("auto-review");
  });

  it("draws a review in the conversation as a line", () => {
    const item: FeedItem = { id: "f1", conversation: "main", at: "2026-11-05T09:00:00Z", kind: "review", ref: "todo_1",
      body: { title: "部署 hub", verdict: "accepted", note: "已部署，/healthz ok" } };
    const html = renderToStaticMarkup(<FeedLineView item={item} />);
    expect(html).toContain("已验收");
    expect(html).toContain("已部署，/healthz ok");
  });
});
