// @vitest-environment jsdom
// A question with no options (#1573): Codex's request_user_input asks in free text, and the hub sends its options as
// `null` (a Go nil slice). The card draws the question with its free-answer box and sends what is typed, as iOS and
// Android do; before, `q.options.map` threw while rendering and the page boundary replaced the whole page. The hub is
// faked (apiOp).
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
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

// As the hub sends it: JSON.parse of `"options":null`.
const freeText = JSON.parse(
  '{"id":"cxq_1","machine_id":"m","session_id":"s","kind":"question","agent":"codex","tool_name":"request_user_input",' +
    '"questions":[{"question":"What should the release branch be called?","header":"Branch","multi_select":false,"options":null}]}',
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

describe("a question with no options", () => {
  it("draws the question with its free-answer box and sends what is typed", async () => {
    act(() => root.render(<InteractionCard ix={freeText} />));
    expect(host.textContent).toContain("What should the release branch be called?");
    const input = host.querySelector("input") as HTMLInputElement;
    expect(input).toBeTruthy();
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    act(() => {
      set.call(input, "release/2026-11");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(calls).toEqual([{ path: "/interactions/cxq_1/resolve", body: { answers: { "What should the release branch be called?": "release/2026-11" } } }]);
  });
});
