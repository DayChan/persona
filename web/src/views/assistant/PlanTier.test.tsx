// @vitest-environment jsdom
// The plan card says what each task runs on (#2225): a task on an Agent preset shows the preset, not "Default model"; an
// explicit model still wins.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { PlanTask, TaskPlan } from "../../lib/feed";

const { getState } = await import("../../lib/store");
const { PlanCard } = await import("./Tasks");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };
const task = (o: Partial<PlanTask> = {}): PlanTask => ({ title: "补测试", prompt: "补", machine: "m1", cwd: "/w", ...o });
const plan = (tasks: PlanTask[]): TaskPlan => ({ id: "p1", conversation: "c1", state: "open", tasks });
const render = async (p: TaskPlan) => {
  await act(async () => root.render(<PlanCard plan={p} />));
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  seed({ machines: [{ id: "m1", name: "工作机" }] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  seed(before);
});

it("shows a task's preset, and an explicit model over it", async () => {
  await render(plan([task({ tier: "review" }), task({ title: "重构", tier: "review", model: "gw/model-x" }), task({ title: "文档" })]));
  const text = host.textContent ?? "";
  expect(text).toContain("预设 review");
  expect(text).toContain("gw/model-x");
  expect(text).toContain("默认模型");
});
