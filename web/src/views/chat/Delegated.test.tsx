// @vitest-environment jsdom
// The assistant answering agents' questions first (#544), as the web shows it: a question card it is still looking at,
// the context it filled in when it left the question to the user, and a question it answered, whose answer the user may
// change, from the chat and from the session's history. The hub is faked (api, apiOp).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const request = vi.hoisted(() => vi.fn());
const resolved = vi.hoisted(() => [] as { path: string; body: unknown }[]);
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: (...args: unknown[]) => request(...args) }));
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: unknown }) => {
    resolved.push({ path, body: init.json });
    return {};
  }),
}));
import { ApiError } from "../../lib/api";
import { InteractionCard } from "./Interactions";
import { DelegatedCard, DelegatedHistory } from "./Delegated";
import { getState, noteReanswer } from "../../lib/store";
import { t } from "../../i18n";
import type { DelegatedAnswer } from "../../lib/delegate";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const choice = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")].find((b) => b.textContent?.startsWith(label));
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};
function type(input: HTMLInputElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const Q = "用哪个驱动？";
const question: Interaction = {
  id: "ix1",
  machine_id: "m",
  session_id: "s",
  kind: "question",
  questions: [{ question: Q, header: "驱动", multi_select: false, options: [{ label: "pgx", description: "现有代码在用" }, { label: "lib/pq" }] }],
  created_at: "2026-11-07T10:00:00Z",
};
const sessionRow = (s: Partial<Session> = {}) => ({ machine_id: "m", session_id: "s", ...s }) as Session;
const session = (s: Partial<Session> = {}) => {
  Object.assign(getState(), { sessions: { m: [sessionRow(s)] }, interactions: [], answered: {}, delegated: {}, reanswered: {} });
};
const answered: DelegatedAnswer = { ix: question, answers: { [Q]: "pgx" }, reason: "仓库里都用 pgx", at: Date.parse("2026-11-07T10:01:00Z") };

beforeEach(() => {
  request.mockReset();
  resolved.length = 0;
  session();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("助手在看的问题卡", () => {
  it("标出助手在看；也可以直接答，照常发出", async () => {
    act(() => root.render(<InteractionCard ix={{ ...question, delegating: true }} />));
    expect(host.querySelector("[data-delegating]")?.textContent).toContain("助手在看");
    expect(host.textContent).toContain("助手正在判断能不能替你答");
    await click(choice("lib/pq"));
    await click(host.querySelector(`button[aria-label="${t("chat-interactions.send_answer")}"]`));
    expect(resolved).toEqual([{ path: "/interactions/ix1/resolve", body: { answers: { [Q]: "lib/pq" } } }]);
  });

  it("交给你之后不再标，卡片和原来一样", () => {
    act(() => root.render(<InteractionCard ix={question} />));
    expect(host.querySelector("[data-delegating]")).toBeNull();
    expect(host.textContent).not.toContain("助手在看");
    expect(host.textContent).toContain("驱动");
    expect(host.querySelector("[data-delegate-context]")).toBeNull();
  });
});

describe("助手补充的情况", () => {
  it("按顺序列出助手补齐的几项；推荐是选项原文时标在选项上，不预先选上", () => {
    const delegate = { what: "换数据库驱动", why_now: "要开始写查询了", recommend: "pgx", cost: "以后再换要重写", reason: "影响依赖", deadline: "2026-11-08T10:00:00Z" };
    act(() => root.render(<InteractionCard ix={{ ...question, delegate }} />));
    const box = host.querySelector("[data-delegate-context]")!;
    expect(box.textContent).toContain("助手补充的情况");
    expect([...box.querySelectorAll("dt")].map((e) => e.textContent)).toEqual(["这件事", "为什么现在问", "不选的代价", "为什么要你定", "截止"]);
    expect(box.textContent).toContain("换数据库驱动");
    expect(choice("pgx")!.querySelector("[data-assistant-pick]")?.textContent).toBe("助手推荐");
    expect(choice("lib/pq")!.querySelector("[data-assistant-pick]")).toBeNull();
    expect(choice("pgx")!.getAttribute("aria-pressed")).toBe("false");
  });

  it("推荐是助手自己的话：照原样列出，不标选项", () => {
    act(() => root.render(<InteractionCard ix={{ ...question, delegate: { recommend: "先别换，等压测", reason: "要你定方向" } }} />));
    const box = host.querySelector("[data-delegate-context]")!;
    expect([...box.querySelectorAll("dt")].map((e) => e.textContent)).toEqual(["推荐", "为什么要你定"]);
    expect(box.textContent).toContain("先别换，等压测");
    expect(host.querySelector("[data-assistant-pick]")).toBeNull();
  });
});

describe("助手代答的问题", () => {
  const render = (d: DelegatedAnswer = answered) => act(() => root.render(<DelegatedCard d={d} />));

  it("说助手答了什么、为什么；改答只发改了的，发出后标出你改答了", async () => {
    render();
    expect(host.textContent).toContain("助手代答");
    expect(host.textContent).toContain("pgx");
    expect(host.textContent).toContain("理由：仓库里都用 pgx");
    await click(button("改答"));
    expect(host.textContent).toContain("Agent 已经按助手的答案往下做了");
    expect(choice("pgx")!.getAttribute("aria-pressed")).toBe("true");
    expect(button("发送新答案")!.disabled).toBe(true);
    request.mockResolvedValue({ ok: true });
    await click(choice("lib/pq"));
    expect(button("发送新答案")!.disabled).toBe(false);
    await click(button("发送新答案"));
    expect(request).toHaveBeenCalledWith("/interactions/ix1/reanswer", { method: "POST", json: { answers: { [Q]: "lib/pq" } } });
    expect(getState().reanswered.ix1.answers).toEqual({ [Q]: "lib/pq" });
    expect(button("发送新答案")).toBeUndefined();
    expect(host.textContent).toContain("你改答了");
    expect(host.querySelector("[data-delegated-answer]")?.textContent).toContain("lib/pq");
  });

  it("自己写的答案也能发", async () => {
    render();
    await click(button("改答"));
    request.mockResolvedValue({ ok: true });
    type(host.querySelector<HTMLInputElement>(`input[aria-label="${t("chat-interactions.custom_answer")}"]`)!, "等压测完再定");
    expect(choice("pgx")!.getAttribute("aria-pressed")).toBe("false");
    await click(button("发送新答案"));
    expect(request).toHaveBeenCalledWith("/interactions/ix1/reanswer", { method: "POST", json: { answers: { [Q]: "等压测完再定" } } });
  });

  it("没发出去：在表单里说为什么，选的答案留着", async () => {
    render();
    await click(button("改答"));
    request.mockRejectedValue(new ApiError(503, "assistant offline", { code: "assistant_offline" }));
    await click(choice("lib/pq"));
    await click(button("发送新答案"));
    expect(host.querySelector("[role=alert]")?.textContent).toBe("助手不在线，稍后再试");
    expect(choice("lib/pq")!.getAttribute("aria-pressed")).toBe("true");
    expect(getState().reanswered.ix1).toBeUndefined();
  });

  it("取消改答：表单收起，什么都不发", async () => {
    render();
    await click(button("改答"));
    await click(button(t("common.cancel")));
    expect(button("发送新答案")).toBeUndefined();
    expect(request).not.toHaveBeenCalled();
  });

  it("别处改的答案（hub 推来）也跟上", () => {
    render();
    act(() => noteReanswer("ix1", { [Q]: "lib/pq" }));
    expect(host.textContent).toContain("你改答了");
    expect(host.querySelector("[data-delegated-answer]")?.textContent).toContain("lib/pq");
  });

  it("新答案是作为消息发给 Agent 的：不能发消息的会话不能改答，只是不能审批的可以", () => {
    session({ source_kind: "runtime", capabilities: { approve: true, send: false } } as Partial<Session>);
    render();
    expect(button("改答")!.disabled).toBe(true);
    expect(button("改答")!.title).toBe(t("agents-agentsview.cannot_send"));
    session({ source_kind: "runtime", capabilities: { approve: false, send: true } } as Partial<Session>);
    render({ ...answered });
    expect(button("改答")!.disabled).toBe(false);
  });

  it("别的管家的会话：改答发给那个管家", async () => {
    render({ ...answered, ix: { ...question, manager: "manager-2" } });
    await click(button("改答"));
    request.mockResolvedValue({ ok: true });
    await click(choice("lib/pq"));
    await click(button("发送新答案"));
    expect(request).toHaveBeenCalledWith("/interactions/ix1/reanswer", { method: "POST", json: { answers: { [Q]: "lib/pq" } }, headers: { "X-Persona-Manager": "manager-2" } });
  });

  it("别处只换了多选的顺序：不算改了，不显示助手原来答的", () => {
    const multi = { ...question, questions: [{ question: Q, header: "", multi_select: true, options: [{ label: "pgx" }, { label: "lib/pq" }] }] };
    render({ ...answered, ix: multi, answers: { [Q]: "pgx, lib/pq" } });
    act(() => noteReanswer("ix1", { [Q]: "lib/pq, pgx" }));
    expect(host.textContent).not.toContain("助手原来答的");
    act(() => noteReanswer("ix1", { [Q]: "lib/pq" }));
    expect(host.textContent).toContain("助手原来答的：pgx, lib/pq");
  });

  it("收起后不再留着", async () => {
    getState().delegated = { ix1: answered };
    render();
    await click(host.querySelector(`button[aria-label="${t("chat-interactions.collapse")}"]`));
    expect(getState().delegated.ix1).toBeUndefined();
  });
});

describe("会话里看助手代答过的问题", () => {
  const history = {
    interactions: [
      { interaction: question, status: "resolved", resolution: { answers: { [Q]: "pgx" }, answered_by: "assistant", reason: "仓库里都用 pgx" }, resolved_at: "2026-11-07T10:01:00Z" },
      { interaction: { ...question, id: "ix2" }, status: "resolved", resolution: { answers: { [Q]: "lib/pq" } }, resolved_at: "2026-11-07T11:00:00Z" },
    ],
  };
  const render = () => act(async () => root.render(<DelegatedHistory session={sessionRow()} />));

  it("只列助手代答的，可以在这里改答", async () => {
    request.mockImplementation(async (path: string) => (path.startsWith("/interactions/history") ? history : { ok: true }));
    await render();
    await flush();
    expect(request).toHaveBeenCalledWith("/interactions/history?machine_id=m&session_id=s&limit=500", {});
    expect(host.textContent).toContain("助手代答过的问题");
    expect(host.querySelectorAll("[data-delegated-row]")).toHaveLength(1);
    expect(host.textContent).toContain("理由：仓库里都用 pgx");
    await click(button("改答"));
    await click(choice("lib/pq"));
    await click(button("发送新答案"));
    expect(request).toHaveBeenLastCalledWith("/interactions/ix1/reanswer", { method: "POST", json: { answers: { [Q]: "lib/pq" } } });
    expect(host.textContent).toContain("你改答了");
  });

  it("读不到：说为什么，可以重试", async () => {
    request.mockRejectedValueOnce(new ApiError(500, "boom", {}));
    await render();
    await flush();
    expect(host.querySelector("[role=alert]")).toBeTruthy();
    request.mockResolvedValue(history);
    await click(button("重试"));
    expect(host.querySelectorAll("[data-delegated-row]")).toHaveLength(1);
  });

  it("别的管家的会话：问那个管家", async () => {
    request.mockResolvedValue(history);
    await act(async () => root.render(<DelegatedHistory session={sessionRow({ manager: "manager-2" })} />));
    await flush();
    expect(request).toHaveBeenCalledWith("/interactions/history?machine_id=m&session_id=s&limit=500", { headers: { "X-Persona-Manager": "manager-2" } });
  });

  it("没有代答过的：整段不显示", async () => {
    request.mockResolvedValue({ interactions: [] });
    await render();
    await flush();
    expect(host.textContent).toBe("");
  });
});
