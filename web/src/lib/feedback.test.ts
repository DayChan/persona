import { describe, expect, it } from "vitest";
import { FeedbackGate, FOLD_MS, isOfflineError } from "./feedback";

describe("FeedbackGate: errors while the hub is out of reach", () => {
  it("turns them into one calm line at most every few seconds", () => {
    const g = new FeedbackGate();
    expect(g.pass("已处理失败：连不上 hub", "error", "hub", 0)).toEqual({ text: "连不上 hub，刚才的操作没有生效", tone: "info" });
    expect(g.pass("发送失败：连不上 hub", "error", "hub", 1_000)).toBeNull();
    expect(g.pass("发送失败：连不上 hub", "error", "network", FOLD_MS + 1)).toEqual({ text: "没有网络，刚才的操作没有生效", tone: "info" });
  });

  it("keeps an error that has nothing to do with the hub as it is, red, while offline too", () => {
    const g = new FeedbackGate();
    expect(g.pass("请输入数字", "error", "hub", 0)).toEqual({ text: "请输入数字", tone: "error" });
    expect(g.pass("复制失败", "error", "network", 100)).toEqual({ text: "复制失败", tone: "error" });
    // and it does not use up the calm line: the next offline failure still says so
    expect(g.pass("发送失败：连不上 hub", "error", "hub", 200)).toEqual({ text: "连不上 hub，刚才的操作没有生效", tone: "info" });
    // the caller can say so explicitly
    expect(g.pass("保存失败", "error", "hub", FOLD_MS + 300, true)?.tone).toBe("info");
  });

  it("knows the texts of a request that never reached the hub", () => {
    expect(isOfflineError("连不上 hub")).toBe(true);
    expect(isOfflineError("已处理失败：连不上 hub")).toBe(true);
    expect(isOfflineError("HTTP 502")).toBe(false);
    expect(isOfflineError("HTTP 500")).toBe(false);
    expect(isOfflineError("名字重复")).toBe(false);
  });

  it("leaves errors alone while reachable, and anything not an error", () => {
    const g = new FeedbackGate();
    expect(g.pass("保存失败：名字重复", "error", null, 0)).toEqual({ text: "保存失败：名字重复", tone: "error" });
    expect(g.pass("已复制", "ok", "hub", 10)).toEqual({ text: "已复制", tone: "ok" });
  });

  it("folds the same line again within a few seconds, not a different one", () => {
    const g = new FeedbackGate();
    expect(g.pass("刷新失败：超时", "error", null, 0)).not.toBeNull();
    expect(g.pass("刷新失败：超时", "error", null, 2_000)).toBeNull();
    expect(g.pass("已复制", "ok", null, 2_100)).not.toBeNull();
    expect(g.pass("刷新失败：超时", "error", null, 2_200)).not.toBeNull(); // another line came in between
    expect(g.pass("刷新失败：超时", "error", null, 2_200 + FOLD_MS)).not.toBeNull();
  });
});
