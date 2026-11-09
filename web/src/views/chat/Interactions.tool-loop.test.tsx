// @vitest-environment jsdom
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
import { apiOp } from "../../lib/ops";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";

const question = "Is this agent stuck?";
const ix: Interaction = {
  id: "loop:1", machine_id: "m", session_id: "s", kind: "question", tool_name: "persona_tool_loop",
  questions: [{ question, options: [{ label: "Stop now", description: "" }, { label: "Keep going", description: "" }] }],
};
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  calls.length = 0;
  vi.mocked(apiOp).mockClear();
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("tool loop card", () => {
  it.each(["Stop now", "Keep going"])("sends the server-provided choice %s through the interaction route", async (label) => {
    await act(async () => root.render(<InteractionCard ix={ix} />));
    expect(host.textContent).toContain(question);
    expect([...host.querySelectorAll("button")].map((b) => b.textContent?.trim())).toEqual(["Stop now", "Keep going"]);
    expect(host.querySelector("input")).toBeNull();
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!.click());
    expect(calls).toEqual([{ path: "/interactions/loop%3A1/resolve", body: { answers: { [question]: label } } }]);
  });

  it("admits only one answer while the request is pending", async () => {
    let finish!: (value: unknown) => void;
    vi.mocked(apiOp).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }) as ReturnType<typeof apiOp>);
    await act(async () => root.render(<InteractionCard ix={ix} />));
    const buttons = [...host.querySelectorAll("button")];
    await act(async () => { buttons[0].click(); buttons[1].click(); });
    expect(apiOp).toHaveBeenCalledTimes(1);
    expect(buttons.every((button) => button.disabled)).toBe(true);
    await act(async () => finish({}));
  });

  it("does not offer actions for a malformed card", async () => {
    await act(async () => root.render(<InteractionCard ix={{ ...ix, questions: [{ question, options: [{ label: "Stop now", description: "" }] }] }} />));
    expect(host.querySelectorAll("button")).toHaveLength(0);
  });

  it("respects session approval permissions", async () => {
    Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s", source_kind: "runtime", capabilities: { approve: false } } as Session] } });
    await act(async () => root.render(<InteractionCard ix={ix} />));
    expect([...host.querySelectorAll("button")].every((button) => button.matches(":disabled"))).toBe(true);
  });
});
