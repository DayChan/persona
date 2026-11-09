import { describe, expect, it } from "vitest";
import { chatLines } from "./turns";
import type { Item } from "./types";
import { NAV_RAIL_PREVIEW_MAX, activeNavRailOffset, navRailExcerpt, navRailPreview, navRailTicks, pickActiveTick, planNavRailTicks } from "./nav-rail";

const u = (seq: number, text: string, extra: Partial<Item> = {}): Item => ({ id: `u${seq}#0`, seq, role: "user", kind: "text", text, ...extra });
const a = (seq: number, text: string): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text });
const ask = (n: number, q: string, answer = `第 ${n} 个回答：改好了，测试也过了。`): Item[] => [u(n * 2 - 1, q), a(n * 2, answer)];

describe("navRailTicks", () => {
  it("makes one tick per question of yours, with the answer's start", () => {
    const lines = chatLines([...ask(1, "第一个问题"), ...ask(2, "第二个问题"), ...ask(3, "第三个问题"), ...ask(4, "第四个问题")]);
    const ticks = navRailTicks(lines);
    expect(ticks.map((x) => x.preview)).toEqual(["第一个问题", "第二个问题", "第三个问题", "第四个问题"]);
    expect(ticks.map((x) => x.answerExcerpt)).toEqual([1, 2, 3, 4].map((n) => `第 ${n} 个回答：改好了，测试也过了。`));
    expect(ticks.map((x) => x.id)).toEqual(lines.filter((l) => l.kind === "segment" && l.seg.user).map((l) => l.id));
  });

  it("skips injected lines, messages from others, loop wake-ups and lines that are not turns", () => {
    const wake: Item = { id: "w#0", seq: 90, role: "assistant", kind: "notice", wake: true, text: "后台任务完成" };
    const items = [
      ...ask(1, "第一个问题"),
      u(3, "替我记一下这件事", { injected: { line: "scheduled: 定时任务", kind: "task" } }),
      ...ask(2, "第二个问题"),
      u(7, "另一个会话发来的", { from: "session-b" }),
      ...ask(3, "第三个问题"),
      u(11, "/loop 继续盯着"),
      u(13, "第四个问题"), // its answer has not arrived yet
      wake,
    ];
    const ticks = navRailTicks(chatLines(items));
    expect(ticks.map((x) => x.preview)).toEqual(["第一个问题", "第二个问题", "第三个问题", "第四个问题"]);
    // a question not answered yet has a tick with no excerpt; the wake itself is not a turn
    expect(ticks[3].answerExcerpt).toBeUndefined();
  });

  it("keeps a question that is only pictures or picked contexts, counting the attachments", () => {
    const img: Item = { id: "u1#1", seq: 2, role: "user", kind: "image", image: { mime: "image/png", lazy: true } };
    const lines = chatLines([
      { id: "u1#0", seq: 1, role: "user", kind: "text", text: "" } as Item,
      img,
      a(3, "看到了"),
      ...ask(2, "第二个问题"),
      ...ask(3, "第三个问题"),
      ...ask(4, "第四个问题"),
    ]);
    const ticks = navRailTicks(lines);
    expect(ticks[0]).toMatchObject({ preview: "", attachmentsOnly: 1, answerExcerpt: "看到了" });
  });

  it("drops a question with no words and nothing attached", () => {
    const lines = chatLines([...ask(1, "第一个问题"), { id: "u3#0", seq: 3, role: "user", kind: "text", text: "" } as Item, ...ask(2, "第二个问题"), ...ask(3, "第三个问题")]);
    expect(navRailTicks(lines).map((x) => x.preview)).toEqual(["第一个问题", "第二个问题", "第三个问题"]);
  });
});

describe("navRailPreview", () => {
  it("takes the first own line, skipping quote blocks", () => {
    expect(navRailPreview("> 引用的原文\n> 第二行\n这是我的问题\n还有下文")).toBe("这是我的问题");
  });

  it("falls back to the quote's first line when the message is all quote", () => {
    expect(navRailPreview("> 只有引用\n> 没有别的")).toBe("只有引用");
  });

  it("skips leading blank lines and caps the line", () => {
    expect(navRailPreview("\n\n  \n问题")).toBe("问题");
    const long = "字".repeat(NAV_RAIL_PREVIEW_MAX + 5);
    const out = navRailPreview(long);
    expect(out).toHaveLength(NAV_RAIL_PREVIEW_MAX);
    expect(out.endsWith("…")).toBe(true);
  });
});

describe("navRailExcerpt", () => {
  it("strips Markdown marks and folds the answer into one capped line", () => {
    const raw = "## 看完了\n\n**整体没问题**，有三处建议：\n\n- `WeatherClient` 的重试没有上限\n- 缓存的 key 只用了城市名\n\n[见这里](https://example.com/x) 的讨论。\n<!-- a marker -->";
    expect(navRailExcerpt(raw)).toBe("看完了 整体没问题，有三处建议： WeatherClient 的重试没有上限 缓存的 key 只用了城市名 见这里 的讨论。");
    expect(navRailExcerpt("  \n\n")).toBe("");
  });

  it("caps the excerpt at its limit", () => {
    expect(navRailExcerpt("答".repeat(500))).toHaveLength(200);
  });
});

describe("planNavRailTicks", () => {
  it("keeps the normal pitch while the ticks fit", () => {
    expect(planNavRailTicks(10, 200)).toEqual({ start: 0, pitch: 9, hidden: 0, count: 10 });
    expect(planNavRailTicks(0, 0)).toEqual({ start: 0, pitch: 9, hidden: 0, count: 0 });
  });

  it("compresses the pitch before windowing ticks", () => {
    expect(planNavRailTicks(20, 100)).toEqual({ start: 0, pitch: 5, hidden: 0, count: 20 });
  });

  it("keeps every selected input accessible in a bounded moving window", () => {
    expect(planNavRailTicks(30, 40)).toEqual({ start: 22, pitch: 5, hidden: 22, count: 8 });
    for (let focus = 0; focus < 1000; focus++) {
      const plan = planNavRailTicks(1000, 200, focus);
      expect(plan.count).toBe(40);
      expect(plan.start).toBeLessThanOrEqual(focus);
      expect(plan.start + plan.count).toBeGreaterThan(focus);
    }
  });
});

describe("pickActiveTick", () => {
  const tops = [100, 200, 300, 400];
  const at = (i: number) => tops[i];

  it("is the last question that started above the threshold", () => {
    expect(pickActiveTick(4, 250, at)).toBe(1);
    expect(pickActiveTick(4, 1000, at)).toBe(3);
  });

  it("the first above the view, without treating missing anchors as already read", () => {
    expect(pickActiveTick(4, 50, at)).toBe(0);
    expect(pickActiveTick(4, 250, (i) => (i < 2 ? null : tops[i]))).toBe(2);
    expect(pickActiveTick(4, 250, (i) => (i === 3 ? null : tops[i]))).toBe(1);
    expect(pickActiveTick(4, 250, () => null)).toBeNull();
  });
});

describe("activeNavRailOffset", () => {
  it("tracks the last input above the viewport from cached offsets", () => {
    const offsets = [{ index: 0, top: 100 }, { index: 2, top: 300 }, { index: 4, top: 900 }];
    expect(activeNavRailOffset(offsets, 50)).toBe(0);
    expect(activeNavRailOffset(offsets, 300)).toBe(2);
    expect(activeNavRailOffset(offsets, 800)).toBe(2);
    expect(activeNavRailOffset(offsets, 1500)).toBe(4);
    expect(activeNavRailOffset([], 100)).toBeNull();
  });
});
