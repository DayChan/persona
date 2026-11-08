import { beforeEach, describe, expect, it, vi } from "vitest";

const request = vi.hoisted(() => vi.fn());
vi.mock("./api", async (orig) => ({ ...(await orig<typeof import("./api")>()), api: (...args: unknown[]) => request(...args) }));

import { ApiError } from "./api";
import { delegatedHistory, delegateLines, initialPicks, isDelegating, reanswer, reanswerChanged, reanswerError, recommendedLabels } from "./delegate";
import { formatDate } from "../i18n";
import type { Interaction, Question } from "./types";

const q = (question: string, labels: string[], multi = false): Question => ({ question, multi_select: multi, options: labels.map((label) => ({ label })) });
const ix = (patch: Partial<Interaction> = {}): Interaction => ({ id: "ix_1", machine_id: "m", session_id: "s", kind: "question", questions: [q("用哪个库？", ["pgx", "sqlc"])], ...patch });

beforeEach(() => {
  request.mockReset();
});

describe("isDelegating", () => {
  it("只有助手还在看的问题卡算", () => {
    expect(isDelegating(ix({ delegating: true }))).toBe(true);
    expect(isDelegating(ix())).toBe(false);
    expect(isDelegating(ix({ kind: "permission", delegating: true }))).toBe(false);
  });
});

describe("delegateLines", () => {
  it("按固定顺序列出助手补齐的几项，空的不列", () => {
    const deadline = "2026-11-08T10:00:00Z";
    const lines = delegateLines({ what: "换数据库驱动", why_now: "", recommend: "pgx", cost: "要重写查询", reason: "影响上线时间", deadline });
    expect(lines.map((l) => l.key)).toEqual(["what", "recommend", "cost", "reason", "deadline"]);
    expect(lines[0]).toEqual({ key: "what", label: "这件事", text: "换数据库驱动" });
    expect(lines[4].text).toBe(formatDate(deadline, { dateStyle: "medium", timeStyle: "short" }));
  });
  it("没有上下文或只有空白：一行也没有", () => {
    expect(delegateLines(undefined)).toEqual([]);
    expect(delegateLines({ what: "  ", deadline: "不是时间" })).toEqual([]);
  });
});

describe("recommendedLabels", () => {
  it("单选：推荐就是选项原文时标出那一个", () => {
    expect(recommendedLabels([q("用哪个库？", ["pgx", "sqlc"])], "sqlc")).toEqual([["sqlc"]]);
  });
  it("多选：逗号连起来的几个都标出", () => {
    expect(recommendedLabels([q("改哪些？", ["A", "B", "C"], true)], "A, C")).toEqual([["A", "C"]]);
  });
  it("几个问题：按全角分号一一对应", () => {
    const qs = [q("库？", ["pgx", "sqlc"]), q("先测吗？", ["是", "否"])];
    expect(recommendedLabels(qs, "pgx；否")).toEqual([["pgx"], ["否"]]);
  });
  it("推荐是助手自己的话：不标任何选项", () => {
    expect(recommendedLabels([q("用哪个库？", ["pgx", "sqlc"])], "先别换，等压测")).toEqual([[]]);
    expect(recommendedLabels([q("用哪个库？", ["pgx", "sqlc"])], undefined)).toEqual([[]]);
  });
  it("a free-text question (options null, #1573) has no option to mark", () => {
    expect(recommendedLabels([{ question: "Branch name?", options: null }], "release/2026-11")).toEqual([[]]);
  });
});

describe("改答的表单", () => {
  it("助手的答案是选项时预先选上，不是选项时放进自己写的框", () => {
    expect(initialPicks([q("库？", ["pgx", "sqlc"]), q("改哪些？", ["A", "B"], true), q("备注？", ["无"])], { "库？": "pgx", "改哪些？": "A, B", "备注？": "周五前" })).toEqual({
      picked: { 0: ["pgx"], 1: ["A", "B"], 2: [] },
      custom: { 2: "周五前" },
    });
  });
  it("和助手的答案一样就不算改了", () => {
    expect(reanswerChanged({ "库？": "pgx" }, { "库？": " pgx " })).toBe(false);
    expect(reanswerChanged({ "库？": "pgx" }, { "库？": "sqlc" })).toBe(true);
  });
  it("多选只换了顺序不算改了；单选的选项里有逗号照原文比", () => {
    const multi = [q("改哪些？", ["A", "B", "C"], true)];
    expect(reanswerChanged({ "改哪些？": "A, B" }, { "改哪些？": "B, A" }, multi)).toBe(false);
    expect(reanswerChanged({ "改哪些？": "A, B" }, { "改哪些？": "A, C" }, multi)).toBe(true);
    expect(reanswerChanged({ "库？": "A, B" }, { "库？": "B, A" }, [q("库？", ["A, B", "B, A"])])).toBe(true);
    expect(reanswerChanged({ "改哪些？": "先 A, 再 B" }, { "改哪些？": "再 B, 先 A" }, multi)).toBe(true); // own words: as written
  });
});

describe("delegatedHistory", () => {
  it("只留助手代答的问题，新的在前", () => {
    const rec = (id: string, at: string, resolution: Record<string, unknown>, kind: Interaction["kind"] = "question") => ({ interaction: ix({ id, kind }), status: "resolved", resolution, resolved_at: at });
    const out = delegatedHistory([
      rec("a", "2026-11-07T10:00:00Z", { answers: { "用哪个库？": "pgx" }, answered_by: "assistant", reason: "现有代码都用 pgx" }),
      rec("b", "2026-11-07T11:00:00Z", { answers: { "用哪个库？": "sqlc" } }),
      rec("c", "2026-11-07T12:00:00Z", { answers: { "用哪个库？": "sqlc" }, answered_by: "assistant" }),
      rec("d", "2026-11-07T13:00:00Z", { allow: true, answered_by: "assistant" }, "permission"),
    ]);
    expect(out.map((d) => d.ix.id)).toEqual(["c", "a"]);
    expect(out[1]).toMatchObject({ answers: { "用哪个库？": "pgx" }, reason: "现有代码都用 pgx", at: Date.parse("2026-11-07T10:00:00Z") });
  });
});

describe("reanswer", () => {
  it("把新答案 POST 给 …/reanswer", async () => {
    request.mockResolvedValue({ ok: true });
    expect(await reanswer("ix/1", { "用哪个库？": "sqlc" })).toEqual({ ok: true });
    expect(request).toHaveBeenCalledWith("/interactions/ix%2F1/reanswer", { method: "POST", json: { answers: { "用哪个库？": "sqlc" } } });
  });
  it("失败时说清楚为什么", async () => {
    request.mockRejectedValue(new ApiError(503, "assistant offline", { code: "assistant_offline" }));
    expect(await reanswer("ix_1", { a: "b" })).toEqual({ ok: false, error: "助手不在线，稍后再试" });
  });
  it("按协议的错误码各说各的", () => {
    expect(reanswerError(new ApiError(403, "x", {}))).toBe("这台设备只能按卡片上的选项回答，不能改答");
    expect(reanswerError(new ApiError(409, "x", {}))).toBe("这个问题不是助手代答的，不能在这里改答");
    expect(reanswerError(new ApiError(400, "x", {}))).toBe("先选一个答案");
    expect(reanswerError(new ApiError(502, "node unreachable", {}))).toBe("新答案没能发给 Agent：node unreachable");
    expect(reanswerError(new ApiError(404, "404 page not found", {}, false))).toBe("这个 hub 还不支持改答，先更新 hub");
    expect(reanswerError(new ApiError(500, "boom", {}))).toContain("没能改答");
  });
});
