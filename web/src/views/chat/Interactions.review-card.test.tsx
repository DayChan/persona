// @vitest-environment jsdom
// The independent review's findings card (#1808): the hub's "N findings" line as the kicker, the report as markdown in
// a box that scrolls, Send forwarding it to the agent and Dismiss closing it without sending (both explicit buttons,
// Enter the card's main action, × the same as Dismiss). The hub is faked (apiOp), as the card-keys test's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return {};
  }),
}));
import { InteractionCard } from "./Interactions";
import { answerLines } from "../../lib/answered";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const cardRoot = () => host.querySelector<HTMLElement>("[data-card-keys]")!;
const press = (k: string, at?: string) =>
  act(() => {
    (at ? host.querySelector(at)! : cardRoot()).dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
  });
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;

const review = JSON.parse(
  '{"id":"review_1","machine_id":"m","session_id":"s","kind":"review","tool_name":"persona_review",' +
    '"summary":"3 findings","findings":"## 发现\\n1. **超时未处理**：api.go 里的调用没有截止时间\\n2. 泄漏的连接","findings_count":3,"review_session":"r1"}',
) as Interaction;

beforeEach(() => {
  calls.length = 0;
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("an independent review's findings card", () => {
  it("shows the hub's findings line as the kicker, the report as markdown, and both ways to finish", async () => {
    act(() => root.render(<InteractionCard ix={review} />));
    expect(host.textContent).toContain("3 findings"); // the hub's own line, as the kicker
    expect(host.textContent).toContain("这项工作的独立审查"); // the card's title
    expect(host.textContent).toContain("超时未处理"); // the report itself renders (markdown, bold kept)
    expect(button("发给 Agent")).toBeTruthy();
    expect(button("不发了")).toBeTruthy();
    expect(host.textContent).toContain("发现会作为一条消息发给这个 Agent"); // the note under the buttons
  });

  it("Send forwards the findings to the agent; Dismiss closes the card without sending", async () => {
    act(() => root.render(<InteractionCard ix={review} />));
    act(() => button("不发了").click());
    await flush();
    act(() => button("发给 Agent").click());
    await flush();
    expect(calls).toEqual([
      { path: "/interactions/review_1/resolve", body: { dismiss: true } },
      { path: "/interactions/review_1/resolve", body: { action: "send" } },
    ]);
  });

  it("the × button dismisses too, and Enter sends — never a key away from Dismiss", async () => {
    act(() => root.render(<InteractionCard ix={review} />));
    const handBack = host.querySelector<HTMLButtonElement>("button[aria-label='交回电脑处理']");
    expect(handBack).toBeTruthy(); // the card's ×, as on every card
    act(() => cardRoot().focus());
    await press("Enter");
    await flush();
    expect(calls).toEqual([{ path: "/interactions/review_1/resolve", body: { action: "send" } }]);
    act(() => handBack!.click());
    await flush();
    expect(calls[1]).toEqual({ path: "/interactions/review_1/resolve", body: { dismiss: true } });
  });

  it("the answered receipt says the findings were sent on", () => {
    expect(answerLines(review, { action: "send" })).toEqual([{ label: "", answer: "已发给 Agent" }]);
    expect(answerLines(review, { dismiss: true })).toEqual([{ label: "", answer: "交回电脑处理" }]);
  });
});
