// @vitest-environment jsdom
// Allow / Deny on a permission card's system notification (#4464): which card a click from the service worker is about,
// what it sends (the card's own body, through the card's own answer), and when the page leaves it to the worker.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CardAnswer } from "./card-answer";
import type { Interaction } from "./types";

let interactions: Interaction[] = [];
let answers: ((r: CardAnswer) => void)[] = [];
const answerCard = vi.fn((_ix: Interaction, _body: Record<string, unknown>) => new Promise<CardAnswer>((done) => answers.push(done)));
const posted: { title: string; body: string; url: string }[] = [];
const toasts: unknown[][] = [];
const navigate = vi.fn();
vi.mock("./card-answer", () => ({ answerCard: (ix: Interaction, body: Record<string, unknown>) => answerCard(ix, body) }));
vi.mock("./store", () => ({ getState: () => ({ interactions }) }));
vi.mock("./notify", () => ({ postNotification: (n: { title: string; body: string; url: string }) => posted.push(n) }));
vi.mock("../ui/atoms", () => ({ toast: (...a: unknown[]) => toasts.push(a) }));
vi.mock("./router", async (original) => ({ ...(await original<typeof import("./router")>()), navigate: (r: unknown) => navigate(r) }));

const { failedTagsOver, NOTE_MESSAGE, onWorkerMessage, planClick } = await import("./notification-answer");
const worker = await import("./sw-click");
const { allowBody, denyBody } = await import("./quick-answers");

const perm: Interaction = { id: "p1", machine_id: "m", session_id: "a", kind: "permission", tool_name: "Bash", tool_input: { command: "go test ./..." } };
const ask: Interaction = { id: "q1", machine_id: "m", session_id: "a", kind: "question" };
const note = (action: string, ix = "p1", over: Record<string, unknown> = {}) => ({ type: "persona.notification", action, ix, url: "/s/m/a", until: Date.now() + 400, ...over });
const deliver = (data: unknown) => {
  const port = { postMessage: vi.fn() };
  onWorkerMessage({ data, ports: [port as unknown as MessagePort] });
  return port;
};
const settle = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  interactions = [perm, ask];
  answers = [];
  posted.length = 0;
  toasts.length = 0;
  answerCard.mockClear();
  navigate.mockClear();
});
afterEach(() => {
  failedTagsOver([]);
  vi.restoreAllMocks();
});

describe("哪张卡、发什么", () => {
  it("和 worker 说的是同一种消息", () => expect(NOTE_MESSAGE).toBe(worker.NOTE_MESSAGE));

  it("允许 / 拒绝发的和卡片上的按钮一样", () => {
    expect(planClick(note("allow"), interactions, Date.now())).toEqual({ ix: perm, allow: true, body: { allow: true } });
    expect(planClick(note("deny"), interactions, Date.now())).toEqual({ ix: perm, allow: false, body: denyBody() });
    expect(planClick(note("allow"), interactions, Date.now())).toEqual({ ix: perm, allow: true, body: allowBody() });
  });

  it("不是授权卡、页面上没有这张卡、消息来晚了、不认识的消息：不接，交给 worker 打开对话", () => {
    const now = Date.now();
    expect(planClick(note("allow", "q1"), interactions, now)).toBeUndefined();
    expect(planClick(note("allow", "gone"), interactions, now)).toBeUndefined();
    expect(planClick(note("allow"), [{ ...perm, fields: [{ key: "pw", label: "密码" }] }], now)).toBeUndefined();
    expect(planClick(note("allow", "p1", { until: now - 1 }), interactions, now)).toBeUndefined();
    expect(planClick(note("always"), interactions, now)).toBeUndefined();
    expect(planClick({ type: "other", action: "allow", ix: "p1", at: now }, interactions, now)).toBeUndefined();
  });

  it("点通知本身：只转到本站的地址", () => {
    expect(planClick(note("open"), interactions, Date.now())).toEqual({ open: "/s/m/a" });
    expect(planClick(note("open", "p1", { url: "https://example.com/x" }), interactions, Date.now())).toBeUndefined();
    expect(planClick(note("open", "p1", { url: "//example.com/x" }), interactions, Date.now())).toBeUndefined();
  });
});

describe("页面收到 worker 的消息", () => {
  it("接下就回话，用卡片的回答函数答，不切页面", async () => {
    const port = deliver(note("allow"));
    expect(port.postMessage).toHaveBeenCalledWith(true);
    expect(answerCard).toHaveBeenCalledWith(perm, { allow: true });
    answers[0]!({ ok: true, already: false });
    await settle();
    expect(navigate).not.toHaveBeenCalled();
    expect(posted).toEqual([]);
  });

  it("同一张卡正在答时再来一次：不再发", () => {
    deliver(note("allow"));
    const second = deliver(note("deny"));
    expect(second.postMessage).toHaveBeenCalledWith(false);
    expect(answerCard).toHaveBeenCalledTimes(1);
    answers[0]!({ ok: true, already: false });
  });

  it("页面上没有这张卡：回话说不接，worker 去打开对话", () => {
    const port = deliver(note("allow", "gone"));
    expect(port.postMessage).toHaveBeenCalledWith(false);
    expect(answerCard).not.toHaveBeenCalled();
  });

  it("没答上（页面在后台）：弹一条「没能允许 + 原因」，点开进对话", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    deliver(note("allow"));
    answers[0]!({ ok: false, error: "这个请求已经过期，回答没有送到" });
    await settle();
    expect(posted).toEqual([expect.objectContaining({ title: "没能允许", body: "这个请求已经过期，回答没有送到", url: "/s/m/a" })]);
    expect(toasts).toEqual([]);
  });

  it("answers a sessionless notification and keeps a failed answer linked to its request", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    const script = { ...perm, id: "script-request", session_id: "", tool_name: "personad_ask" };
    interactions = [script];
    expect(deliver(note("allow", script.id)).postMessage).toHaveBeenCalledWith(true);
    expect(answerCard).toHaveBeenCalledWith(script, { allow: true });
    answers[0]!({ ok: false, error: "Example connection failure" });
    await settle();
    expect(posted).toEqual([expect.objectContaining({ url: "/requests/script-request" })]);
  });

  it("没答上的通知：卡后来在别处答掉了就收回，卡还在就留着，卡本来就结束了不收回（#5070）", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    deliver(note("allow"));
    answers[0]!({ ok: false, error: "没送到：hub 连不上" });
    await settle();
    expect(failedTagsOver([perm, ask])).toEqual([]);
    expect(failedTagsOver([ask])).toEqual(["answer-failed#p1"]);
    expect(failedTagsOver([ask])).toEqual([]);
    // over already when it failed (410: answerCard took the card off): its notification is the only word of why
    deliver(note("deny"));
    interactions = [ask];
    answers[1]!({ ok: false, error: "这个请求已经过期，回答没有送到" });
    await settle();
    expect(failedTagsOver([ask])).toEqual([]);
  });

  it("同一张卡先没送到、再点一次时卡已经结束：后一条说明原因的通知不被收回", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    deliver(note("allow"));
    answers[0]!({ ok: false, error: "没送到：hub 连不上" });
    await settle();
    deliver(note("allow"));
    interactions = [ask];
    answers[1]!({ ok: false, error: "这个请求已经过期，回答没有送到" });
    await settle();
    expect(posted).toHaveLength(2);
    expect(failedTagsOver([ask])).toEqual([]);
  });

  it("没答上（页面就在前台）：提示条", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    deliver(note("deny"));
    answers[0]!({ ok: false, error: "没送到：hub 连不上" });
    await settle();
    expect(toasts).toEqual([["没送到：hub 连不上", "error"]]);
    expect(posted).toEqual([]);
  });

  it("点通知本身：转到对话", () => {
    const port = deliver(note("open"));
    expect(port.postMessage).toHaveBeenCalledWith(true);
    expect(navigate).toHaveBeenCalledWith("/s/m/a");
  });
});
