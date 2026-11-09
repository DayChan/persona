// Ported from cindy's workGroupDeliveryProse / workGroupDurationAnchor / workGroupRunningSubagent / workGroupHistoryGap
// tests (apps/desktop/src/renderer/__tests__), on transcript items instead of cindy's chat messages.
import { describe, expect, it } from "vitest";
import { exchangeSteps, segments } from "./turns";
import type { Item } from "./types";
import {
  foldsHoldingCalls,
  nestedLineDefault,
  togglesLiveOverride,
  workLineOpen,
  workLineStyle,
  groupsHolding,
  explorationSummary,
  groupOpens,
  groupTitle,
  isDeliveryText,
  isRowStep,
  isWorkflowStep,
  planProgress,
  planSteps,
  planTodos,
  previewSteps,
  thinkingDurations,
  workBlocks,
  workDuration,
  type AgentStatus,
  type ToolStep,
  type WorkGroupNode,
  type WorkNode,
} from "./work-groups";

const T0 = Date.parse("2026-07-01T00:00:00.000Z");
const iso = (offsetMs: number) => new Date(T0 + offsetMs).toISOString();
let seq = 0;
const mk = (role: Item["role"], kind: Item["kind"], at: number | undefined, extra: Partial<Item> = {}): Item => {
  seq++;
  return { id: `${extra.id ?? `i${seq}`}#0`, seq, role, kind, ...(at !== undefined ? { ts: iso(at) } : {}), ...extra, ...(extra.id ? { id: `${extra.id}#0` } : {}) };
};
const user = (id: string, at?: number, text = "巡检待放行的 PR，给一份产品决策简报") => mk("user", "text", at, { id, text });
const say = (id: string, text: string, at?: number) => mk("assistant", "text", at, { id, text });
const think = (id: string, at?: number, text = "Reviewing the repository structure…") => mk("assistant", "thinking", at, { id, text });
const tool = (id: string, name = "Bash", at?: number, resultAt?: number, input: Record<string, unknown> = { command: "gh pr diff 1111" }, result = "ok"): Item[] => [
  mk("assistant", "tool_use", at, { id, tool_name: name, tool_use_id: `tu-${id}`, tool_input: input }),
  mk("user", "tool_result", resultAt ?? at, { id: `${id}-r`, tool_use_id: `tu-${id}`, text: result }),
];

function blocks(items: Item[], live = false, agentStatus?: (s: ToolStep) => AgentStatus): WorkNode[] {
  const seg = segments(items)[0];
  return workBlocks(planSteps(exchangeSteps(seg)), { live, turnStart: seg.user?.ts ?? seg.start, agentStatus });
}

const idOf = (n: WorkNode) => (n.kind === "step" ? n.step.id.replace(/#\d+$/, "") : n.id);
const topSteps = (ns: WorkNode[]) => ns.filter((n) => n.kind === "step").map(idOf);
const groups = (ns: WorkNode[]) => ns.filter((n): n is WorkGroupNode => n.kind === "group");
function folded(ns: WorkNode[], id: string): boolean {
  const has = (n: WorkNode): boolean => (n.kind === "step" ? idOf(n) === id : n.children.some(has));
  return groups(ns).some((g) => g.children.some(has));
}

/** "body → side-effect actions to wrap up → a one-line wrap-up": the shape that folded a 3250-character brief (cindy, 2026-07-31). */
const briefThenNotify = (body: string): Item[] => [
  user("u1"),
  think("th1"),
  ...tool("diff", "Bash", undefined, undefined, { command: "gh pr diff 1111" }, "diff --git a/apps/mobile/app/devices/index.tsx"),
  say("brief", body),
  ...tool("notify", "mcp__scheduler__notify"),
  say("wrap", "本轮有 3 条首次亮相的 PR 需要你决策，已触发通知。"),
];
const longPlain = `本轮 7 条有活动，3 条首次亮相。${"逐条核对了改动落在哪些产品面。".repeat(50)}`;

describe("delivered prose stays out of 「已工作」", () => {
  it("a long text before the closing action stays in the open, the work before it still folds", () => {
    expect(longPlain.length).toBeGreaterThanOrEqual(600);
    const b = blocks(briefThenNotify(longPlain));
    expect(topSteps(b)).toEqual(["brief", "wrap"]);
    expect(folded(b, "brief")).toBe(false);
    expect(folded(b, "th1")).toBe(true);
  });

  it.each([
    ["a heading", "## 产品决策简报\n\n本轮 7 条有活动。"],
    ["a list of three", "需要你决定的三条：\n- #1111 手机离线缓存\n- #692 effort 阻断发送\n- #1080 插件授权重构"],
    ["a table", "PR 一览：\n\n| PR | 建议 |\n| --- | --- |\n| #1111 | 放行 |"],
    ["a Markdown image", "图表如下 ![chart](https://example.com/chart.png)"],
    ["an <img>", '缩略图：<img src="https://example.com/thumb.png" width="150">'],
  ])("so does a short one with %s", (_, body) => {
    expect(topSteps(blocks(briefThenNotify(body)))).toEqual(["brief", "wrap"]);
  });

  it.each([
    ["a one-line note", "我已经读完所有必要信息，现在写简报。"],
    ["a plain link", "先看 [PR](https://github.com/x/y/pull/1) 的 diff。"],
    ["an <img> without src", "说明里提到 <img> 标签。"],
    ["a custom <img-wrapper>", '<img-wrapper src="https://example.com/a.png">'],
    ["a list of two", "接下来两件事：\n- 读 diff\n- 写简报"],
    ["a lone rule", "先跑脚本生成简报数据。\n\n---\n\n然后读结果。"],
  ])("a progress note folds: %s", (_, body) => {
    const b = blocks(briefThenNotify(body));
    expect(topSteps(b)).toEqual(["wrap"]);
    expect(folded(b, "brief")).toBe(true);
  });

  it("isDeliveryText", () => {
    expect(isDeliveryText("")).toBe(false);
    expect(isDeliveryText("x".repeat(599))).toBe(false);
    expect(isDeliveryText("x".repeat(600))).toBe(true);
  });
});

describe("a finished exchange", () => {
  it("keeps the last words; the steps and words before fold into one timeline with inner groups", () => {
    const b = blocks([user("u1", 0), think("th1", 5951), say("a1", "我先快速摸一下代码规模和质量证据，再给评价。", 6062), ...tool("b1", "Bash", 6500, 7000), say("a2", "说下我的看法：这是个认真的项目。", 29000)]);
    expect(topSteps(b)).toEqual(["a2"]);
    const [outer] = groups(b);
    expect(outer.id.startsWith("work-summary-")).toBe(true);
    expect(outer.children.map(idOf).filter((x) => !x.startsWith("work-"))).toEqual(["a1"]);
    const inner = outer.children.filter((c): c is WorkGroupNode => c.kind === "group");
    expect(inner).toHaveLength(2);
    // the first run counts the thinking from your message (6 s), not the 111 ms the block took to arrive
    expect(inner[0].durationMs).toBe(6062);
    expect(inner[1].durationMs).toBe(29000 - 6062);
    expect(outer.durationMs).toBe(29000);
    expect(inner[0].durationMs! + inner[1].durationMs!).toBe(outer.durationMs);
  });

  it("without your message the first run counts from its own step", () => {
    const b = blocks([think("th1", 5951), say("a1", "我先摸一下。", 6062), ...tool("b1", "Bash", 6500, 7000), say("a2", "看法如下。", 29000)]);
    const inner = groups(b)[0].children.filter((c): c is WorkGroupNode => c.kind === "group");
    expect(inner[0].durationMs).toBe(111);
  });

  it("an exchange cut off mid-work (no words after its last step) folds as runs, its words in the open", () => {
    const b = blocks([user("u1", 0), ...tool("t1", "Bash", 1000), say("a1", "接着看。", 2000), ...tool("t2", "Read", 3000)]);
    expect(b.map((n) => n.kind)).toEqual(["group", "step", "group"]);
    expect(groups(b).every((g) => !g.live)).toBe(true);
  });

  it("several words in a row after the last step all stay as the answer", () => {
    const b = blocks([user("u1", 0), ...tool("t1", "Bash", 1000), say("a1", "第一部分。", 5000), say("a2", "第二部分。", 6000)]);
    expect(topSteps(b)).toEqual(["a1", "a2"]);
  });
});

describe("while the agent works", () => {
  it("the tail run is live; its clock starts at your message", () => {
    const b = blocks([user("u1", 0), think("th1", 5951)], true);
    const [g] = groups(b);
    expect(g.live).toBe(true);
    expect(g.startedAt).toBe(T0);
    expect(groupTitle(g)).toBe("正在工作…");
  });

  it("its words stay in the open between runs; only the last run is live", () => {
    const b = blocks([user("u1", 0), ...tool("t1", "Bash", 1000), say("a1", "先看一下目录。", 2000), ...tool("t2", "Bash", 3000)], true);
    expect(b.map((n) => (n.kind === "group" ? `g:${n.live}` : idOf(n)))).toEqual(["g:false", "a1", "g:true"]);
  });

  it("shows its newest five steps; more than that opens", () => {
    const few = groups(blocks([user("u1", 0), ...[1, 2, 3, 4, 5].flatMap((n) => tool(`t${n}`, "Bash", n * 1000, undefined, { command: `echo ${n}` }))], true))[0];
    expect(previewSteps(few).map((s) => s.id.replace(/#0$/, ""))).toEqual(["t1", "t2", "t3", "t4", "t5"]);
    expect(groupOpens(few)).toBe(false);
    const many = groups(blocks([user("u1", 0), ...[1, 2, 3, 4, 5, 6, 7].flatMap((n) => tool(`t${n}`, "Bash", n * 1000, undefined, { command: `echo ${n}` }))], true))[0];
    expect(previewSteps(many).map((s) => s.id.replace(/#0$/, ""))).toEqual(["t3", "t4", "t5", "t6", "t7"]);
    expect(groupOpens(many)).toBe(true);
  });

  it("the preview skips thoughts without words and shows a file read twice once", () => {
    const g = groups(blocks([user("u1", 0), think("e", 500, ""), ...tool("r1", "Read", 1000, undefined, { file_path: "/a.ts" }), ...tool("r2", "Read", 2000, undefined, { file_path: "/a.ts" })], true))[0];
    expect(previewSteps(g).map((s) => s.id.replace(/#0$/, ""))).toEqual(["r2"]);
  });

  it("keeps the latest narration after the tool preview, even after many more calls", () => {
    const items = [user("u1", 0), think("old", 500, "Reading the code"), think("latest", 1000, "The fix is ready; checking it now"),
      ...[1, 2, 3, 4, 5, 6, 7].flatMap((n) => tool(`t${n}`, "Bash", 2000 + n * 1000)), think("empty", 10_000, "  ")];
    const g = groups(blocks(items, true))[0];
    expect(previewSteps(g).map((s) => s.id.replace(/#0$/, ""))).toEqual(["t4", "t5", "t6", "t7", "latest"]);
    expect(groupOpens(g)).toBe(true);
    expect(folded([g], "old")).toBe(true);
    expect(folded([g], "latest")).toBe(true);

    const next = groups(blocks([...items, think("new", 11_000, "All checks passed; reviewing the result")], true))[0];
    expect(previewSteps(next).at(-1)?.id).toBe("new#0");
    expect(previewSteps(next).filter((s) => s.id === "new#0")).toHaveLength(1);
  });

  it("counts a narration pinned from outside the tool window when deciding whether the group opens", () => {
    const g = groups(blocks([user("u1", 0), think("note", 500), ...[1, 2, 3, 4, 5].flatMap((n) => tool(`t${n}`, "Bash", n * 1000))], true))[0];
    expect(previewSteps(g)).toHaveLength(5);
    expect(groupOpens(g)).toBe(true);
    expect(previewSteps(g, 1).map((s) => s.id)).toEqual(["note#0"]);
  });

  it("previews narration from folded history without changing the fold or duplicating it after expansion", () => {
    const thought = think("note", 500, "Checking the deployment result");
    const tools = [1, 2, 3, 4, 5, 6].flatMap((n) => tool(`t${n}`, "Bash", n * 1000));
    const fold = mk("assistant", "fold", 500, { id: "fold:note", fold: { items: 13, tools: 6, thoughts: 1, through_seq: tools.at(-1)!.seq, latest_thinking: thought } });
    const g = groups(blocks([user("u1", 0), fold], true))[0];
    expect(previewSteps(g).map((s) => s.id)).toEqual(["note#0"]);
    expect(g.children).toHaveLength(1);
    expect(groupOpens(g)).toBe(true);
    const opened = groups(blocks([user("u2", 0), thought, ...tools], true))[0];
    expect(previewSteps(opened).filter((s) => s.id === "note#0")).toHaveLength(1);
    const newer = think("new", 8000, "The deployment is healthy");
    expect(previewSteps(groups(blocks([user("u3", 0), fold, newer], true))[0]).at(-1)?.id).toBe("new#0");
  });
});

describe("subagents", () => {
  const items = () => [user("u1", 0), ...tool("pre", "Bash", 500), ...tool("task1", "Agent", 1000, 9000, { description: "回归", prompt: "跑回归" }), say("final", "都做完了。", 10000)];

  it("a running one stays in the open, never in a group", () => {
    const b = blocks(items(), false, () => "running");
    expect(topSteps(b)).toEqual(["task1", "final"]);
    expect(folded(b, "task1")).toBe(false);
    expect(groups(b).length).toBeGreaterThan(0);
  });

  it("a finished one stays where it was, in the open (#5602: it folded once done and the runs around it joined)", () => {
    const same = items();
    const running = blocks(same, false, () => "running");
    const done = blocks(same, false, () => "completed");
    expect(topSteps(done)).toEqual(["task1", "final"]);
    expect(folded(done, "task1")).toBe(false);
    // the same groups, with the same keys, either way
    expect(groups(done).map((g) => g.id)).toEqual(groups(running).map((g) => g.id));
  });

  it("a running one in the middle of live work splits the runs around it", () => {
    const b = blocks([user("u1", 0), ...tool("a", "Bash", 500), ...tool("task1", "Agent", 1000), ...tool("b", "Bash", 2000)], true, () => "running");
    expect(b.map((n) => (n.kind === "group" ? "g" : idOf(n)))).toEqual(["g", "task1", "g"]);
  });
});

describe("history gaps", () => {
  const MIN = 60_000;
  it("steps on either side of a gap over 30 minutes are in different groups, neither spanning it", () => {
    const b = blocks([user("u1", 0), ...tool("t1", "Read", MIN), ...tool("t2", "Read", 2 * MIN), ...tool("t3", "Read", 50 * MIN), ...tool("t4", "Read", 51 * MIN), say("a", "好了。", 52 * MIN)]);
    const gs = groups(b);
    expect(gs.length).toBe(2);
    expect(gs.every((g) => (g.durationMs ?? 0) < 30 * MIN)).toBe(true);
  });

  it("a tool that ran 40 minutes is not a gap: the answer after its result still closes the exchange", () => {
    const b = blocks([user("u1", 0), ...tool("long", "Bash", MIN, 41 * MIN), say("a", "构建好了。", 42 * MIN)]);
    expect(topSteps(b)).toEqual(["a"]);
    expect(groups(b)[0].durationMs).toBe(42 * MIN);
  });

  it("calls each close to the last, an hour in all, stay one group", () => {
    const b = blocks([user("u1", 0), ...Array.from({ length: 7 }, (_, i) => tool(`t${i}`, "Bash", i * 10 * MIN)).flat(), say("a", "完成。", 61 * MIN)]);
    expect(groups(b)).toHaveLength(1);
  });

  it("without words after it, a group's end is its latest result, not its last call", () => {
    const b = blocks([...tool("long", "Bash", 0, 40 * MIN), ...tool("short", "Bash", MIN, 2 * MIN)]);
    expect(groups(b)[0].durationMs).toBe(40 * MIN);
  });
});

describe("titles and plans", () => {
  it("a group that only read and searched says so", () => {
    const b = blocks([user("u1", 0), ...tool("r1", "Read", 1000, undefined, { file_path: "/a.ts" }), ...tool("r2", "Read", 2000, undefined, { file_path: "/b.ts" }), ...tool("r3", "Read", 2500, undefined, { file_path: "/a.ts" }), ...tool("g", "Grep", 3000, undefined, { pattern: "x" }), say("a", "看完了。", 65_000)]);
    const [g] = groups(b);
    expect(explorationSummary(g)).toBe("读取 2 个文件 · 搜索 1 次");
    expect(groupTitle(g)).toBe("已工作 1 分 5 秒 · 读取 2 个文件 · 搜索 1 次");
  });

  it("one that ran a command does not", () => {
    const g = groups(blocks([user("u1", 0), ...tool("r1", "Read", 1000, undefined, { file_path: "/a.ts" }), ...tool("b", "Bash", 2000), say("a", "好。", 6000)]))[0];
    expect(groupTitle(g)).toBe("已工作 6 秒");
  });

  it("formats durations like cindy, in Chinese units", () => {
    expect(workDuration(0)).toBe("1 秒");
    expect(workDuration(6062)).toBe("6 秒");
    expect(workDuration(65_000)).toBe("1 分 5 秒");
    expect(workDuration(120_000)).toBe("2 分钟");
    expect(workDuration(3_900_000)).toBe("1 小时 05 分");
    expect(workDuration(3_600_000)).toBe("1 小时");
    expect(workDuration(93_900_000)).toBe("1 天 02 小时 05 分");
  });

  it("only the latest plan of two or more items draws, in the open", () => {
    const todos = (s: string[]) => ({ todos: s.map((content, i) => ({ content, status: i === 0 ? "completed" : i === 1 ? "in_progress" : "pending" })) });
    const b = blocks([user("u1", 0), ...tool("p1", "TodoWrite", 1000, undefined, todos(["读", "改"])), ...tool("t", "Bash", 2000), ...tool("p2", "TodoWrite", 3000, undefined, todos(["读", "改", "测"])), ...tool("one", "TodoWrite", 3500, undefined, todos(["单项"])), say("a", "好了。", 4000)]);
    expect(topSteps(b)).toEqual(["p2", "a"]);
    const p = b.find((n) => n.kind === "step" && idOf(n) === "p2")!;
    const t = planTodos((p as { step: ToolStep }).step);
    expect(planProgress(t)).toEqual({ done: 1, total: 3, current: "改", step: 2, allDone: false });
  });

  it("thoughts take from the step before to when they were written", () => {
    const seg = segments([user("u1", 0), think("th", 3000, ""), ...tool("t", "Bash", 4000, 9000), think("th2", 12000)])[0];
    const d = thinkingDurations(seg.steps, seg.user?.ts);
    expect(d.get("th#0")).toBe(3000);
    expect(d.get("th2#0")).toBe(3000);
  });
});

describe("planProgress", () => {
  it("names the next pending step when none is in progress, the same one step points at", () => {
    const p = planProgress([
      { content: "A", status: "completed" },
      { content: "B", status: "pending" },
      { content: "C", status: "pending" },
    ]);
    expect(p).toMatchObject({ done: 1, total: 3, step: 2, current: "B", allDone: false });
  });
  it("names the one in progress, else the last when all are done", () => {
    expect(planProgress([{ content: "A", status: "completed" }, { content: "B", status: "in_progress" }]).current).toBe("B");
    expect(planProgress([{ content: "A", status: "completed" }, { content: "B", status: "completed" }])).toMatchObject({ current: "B", step: 2, allDone: true });
  });
});

describe("groupsHolding (#2719: a background task's notice → the call that started it)", () => {
  const items = () => [
    user("u1", 0),
    think("th", 1000),
    ...tool("grep", "Grep", 2000, 2500, { pattern: "x" }),
    ...tool("bg", "Bash", 3000, 3500, { command: "npm test", run_in_background: true }, "Command running in background with ID: b1"),
    ...tool("read", "Read", 4000, 4500, { file_path: "/a.ts" }),
    say("a", "测试在后台跑，跑完我再看。", 5000),
  ];

  it("names the groups to open, outer first, down to the call", () => {
    const b = blocks(items());
    const path = groupsHolding(b, "tu-bg");
    expect(path).not.toBeNull();
    expect(path!.length).toBeGreaterThan(0);
    // every id on the path is a group, each one inside the one before it
    let level: WorkNode[] = b;
    for (const id of path!) {
      const g = groups(level).find((x) => x.id === id);
      expect(g, id).toBeTruthy();
      level = g!.children;
    }
    expect(level.some((n) => n.kind === "step" && n.step.kind === "tool" && n.step.use.tool_use_id === "tu-bg")).toBe(true);
  });

  it("is empty for a call left in the open, null for one not there", () => {
    const b = blocks([user("u1", 0), ...tool("bg", "Bash", 1000, 1500, { command: "npm test" }), say("a", "好了", 2000)], true);
    const open = b.find((n) => n.kind === "step" && n.step.kind === "tool");
    if (open) expect(groupsHolding(b, "tu-bg")).toEqual([]);
    expect(groupsHolding(blocks(items()), "tu-nope")).toBeNull();
    expect(groupsHolding([], "tu-bg")).toBeNull();
  });

  it("looks in the open timeline's inner groups too", () => {
    const g = (id: string, children: WorkNode[]): WorkGroupNode => ({ kind: "group", id, children, live: false });
    const step = blocks(items()).flatMap(function all(n): WorkNode[] { return n.kind === "group" ? n.children.flatMap(all) : [n]; })
      .find((n) => n.kind === "step" && n.step.kind === "tool" && n.step.use.tool_use_id === "tu-bg")!;
    const tree = [g("outer", [g("inner-1", []), g("inner-2", [step])])];
    expect(groupsHolding(tree, "tu-bg")).toEqual(["outer", "inner-2"]);
  });
});

// #3434: a Workflow call is its run's card, not a row; while the hub lists the run (running or ended) it stays out of the
// folded work
describe("workflow calls", () => {
  const launch = () => [
    user("u1", 0),
    ...tool("grep", "Grep", 1000, 2000, { pattern: "x" }),
    ...tool("wf", "Workflow", 3000, 3000, { name: "parity-audit" }, "Workflow launched in background"),
    say("ans", "Started it in the background.", 4000),
  ];

  it("is not an ordinary row, and a group that launched one did more than read", () => {
    const [seg] = segments(launch());
    const wf = exchangeSteps(seg).find((s) => s.kind === "tool" && s.use.tool_name === "Workflow")!;
    expect(isWorkflowStep(wf)).toBe(true);
    expect(isRowStep(wf)).toBe(false);
    const g = groups(blocks(launch()))[0];
    expect(explorationSummary(g)).toBe("");
  });

  it("stays in the open while the hub lists its run, running or ended; folds like any step once it does not", () => {
    const seg = segments(launch())[0];
    const steps = planSteps(exchangeSteps(seg));
    const card = workBlocks(steps, { live: false, turnStart: seg.user?.ts, workflowCard: () => true });
    expect(folded(card, "wf")).toBe(false);
    expect(topSteps(card)).toEqual(["wf", "ans"]);
    expect(folded(card, "grep")).toBe(true);
    // in a live exchange too: the work before it folds, the card stays
    const live = workBlocks(steps.slice(0, -1), { live: true, turnStart: seg.user?.ts, workflowCard: () => true });
    expect(folded(live, "wf")).toBe(false);
    const row = workBlocks(steps, { live: false, turnStart: seg.user?.ts, workflowCard: () => false });
    expect(folded(row, "wf")).toBe(true);
  });

  it("finds the hub's fold that holds a run's call: the one whose span holds when it started", () => {
    const fold = mk("assistant", "fold", 1000, { id: "fold:x", fold: { items: 4, tools: 2, thoughts: 0, until: iso(3000), through_seq: 9, through_id: "x" } });
    const seg = segments([user("u2", 0), fold, say("ans2", "Started.", 4000)])[0];
    const steps = exchangeSteps(seg);
    expect(foldsHoldingCalls(steps, new Map([["tu-wf", T0 + 3000]])).map((f) => f.id)).toEqual(["fold:x#0"]);
    expect(foldsHoldingCalls(steps, new Map([["tu-wf", T0 + 60_000]]))).toEqual([]); // started after it
    expect(foldsHoldingCalls(steps, new Map([["tu-wf", NaN]]))).toEqual([]); // the hub does not say when
    expect(foldsHoldingCalls(steps, new Map())).toEqual([]);
    // the call is already among the steps: nothing to fetch
    const open = exchangeSteps(segments(launch())[0]);
    expect(foldsHoldingCalls(open, new Map([["tu-wf", T0 + 3000]]))).toEqual([]);
  });

  it("does not take a fold that ended just before the call for the one holding it", () => {
    // the call at 3.0 s and its result at 3.8 s, in the second fold; the first ended at 2.5 s, within a second of it
    const first = mk("assistant", "fold", 1000, { id: "fold:a", fold: { items: 3, tools: 1, thoughts: 1, until: iso(2500), through_seq: 3, through_id: "a" } });
    const words = say("mid", "Now the workflow.", 2600);
    const second = mk("assistant", "fold", 3000, { id: "fold:b", fold: { items: 4, tools: 2, thoughts: 0, until: iso(3800), through_seq: 9, through_id: "b" } });
    const steps = exchangeSteps(segments([user("u3", 0), first, words, second, say("ans3", "Started.", 4000)])[0]);
    expect(foldsHoldingCalls(steps, new Map([["tu-wf", T0 + 3000]])).map((f) => f.id)).toEqual(["fold:b#0"]);
    // two folds that both hold the instant (one message's lines split by its words): both, not the wrong one
    const u4 = user("u4", 0);
    const tie = mk("assistant", "fold", 1000, { id: "fold:c", fold: { items: 3, tools: 1, thoughts: 1, until: iso(3000), through_seq: 5, through_id: "c" } });
    const mid2 = say("mid2", "Now the workflow.", 3000);
    const holding = mk("assistant", "fold", 3000, { id: "fold:d", fold: { items: 4, tools: 2, thoughts: 0, until: iso(3800), through_seq: 99, through_id: "d" } });
    const both = exchangeSteps(segments([u4, tie, mid2, holding, say("ans4", "Started.", 4000)])[0]);
    expect(foldsHoldingCalls(both, new Map([["tu-wf", T0 + 3000]])).map((f) => f.id)).toEqual(["fold:c#0", "fold:d#0"]);
  });
});

describe("workLineOpen (#1614)", () => {
  it("assistant: open while the exchange runs, unless the reader folded it during the run", () => {
    expect(workLineOpen({ style: "assistant", turnLive: true, remembered: false })).toBe(true);
    expect(workLineOpen({ style: "assistant", turnLive: true, remembered: false, liveOverride: false })).toBe(false);
    expect(workLineOpen({ style: "assistant", turnLive: true, remembered: true, liveOverride: true })).toBe(true);
  });

  it("assistant: once the exchange ends, the run's toggle is dropped and what is remembered (folded by default) shows", () => {
    expect(workLineOpen({ style: "assistant", turnLive: false, remembered: false, liveOverride: true })).toBe(false);
    expect(workLineOpen({ style: "assistant", turnLive: false, remembered: true })).toBe(true);
    expect(togglesLiveOverride("assistant", true)).toBe(true);
    expect(togglesLiveOverride("assistant", false)).toBe(false);
  });

  it("agents: what the reader left it at, running or not", () => {
    expect(workLineOpen({ style: "agents", turnLive: true, remembered: false })).toBe(false);
    expect(workLineOpen({ style: "agents", turnLive: true, remembered: true })).toBe(true);
    expect(workLineOpen({ style: "agents", turnLive: false, remembered: false, liveOverride: true })).toBe(false);
    expect(togglesLiveOverride("agents", true)).toBe(false);
    expect(nestedLineDefault("agents")).toBe(false);
    expect(nestedLineDefault("assistant")).toBe(true);
  });
});

describe("workLineStyle (#1614)", () => {
  it("a Persona conversation is assistant style on the assistant page and when opened from the Agents page", () => {
    expect(workLineStyle(true, undefined)).toBe("assistant");
    expect(workLineStyle(false, true)).toBe("assistant");
    expect(workLineStyle(undefined, true)).toBe("assistant");
    expect(workLineStyle(false, false)).toBe("agents");
    expect(workLineStyle(undefined, undefined)).toBe("agents");
  });
});
