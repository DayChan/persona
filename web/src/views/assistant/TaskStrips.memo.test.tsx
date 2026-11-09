// @vitest-environment jsdom
// The strips above a Persona conversation's composer (its tasks' question, the task dock, the route chip) take only
// the conversation and read the rest from the store and the feed themselves. The chat re-renders on every streamed
// item; with the same conversation they do not render with it, and still follow a new task or question on their own.
// (A <Profiler> around them would not tell: React calls its onRender whenever the Profiler itself re-renders, even
// when the memo under it bails out, so each strip's own child counts its renders.)
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const renders = vi.hoisted(() => ({ questions: 0, dock: 0, route: 0 }));
// the question strip draws its first question's card: one per render
vi.mock("../chat/Interactions", async (orig) => ({
  ...(await orig<typeof import("../chat/Interactions")>()),
  InteractionCard: ({ ix }: { ix: { id: string } }) => {
    renders.questions++;
    return <div data-testid="question">{ix.id}</div>;
  },
}));
// the dock's one task pill draws its status mark: one per render
vi.mock("../../ui/StatusMark", async (orig) => ({
  ...(await orig<typeof import("../../ui/StatusMark")>()),
  StatusMark: () => {
    renders.dock++;
    return <span data-testid="mark" />;
  },
}));
// the route chip draws its send icon: one per render
vi.mock("../../ui/icons", async (orig) => {
  const real = await orig<typeof import("../../ui/icons")>();
  return {
    ...real,
    Icon: (p: Parameters<typeof real.Icon>[0]) => {
      if (p.name === "send") renders.route++;
      return real.Icon(p);
    },
  };
});

import { setRoute } from "../../lib/feed";
import { endInteraction, refresh, reset, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import { RouteChip, TaskDock, TaskQuestions } from "./Tasks";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const now = new Date().toISOString();
const later = new Date(Date.now() + 1000).toISOString();
const todo = (id: string, title: string, sid: string, at = now) => ({ id, title, origin: "c1", created_at: at, updated_at: at, agents: [{ machine_id: "m1", session_id: sid }] });
const session = (sid: string) => ({ machine_id: "m1", session_id: sid, state: "needs_you", status: "needs_you", title: sid, updated_at: now });
const question = (id: string, sid: string, at = now) => ({ id, machine_id: "m1", session_id: sid, kind: "permission", tool_name: "Bash", tool_input: { command: "swift test" }, created_at: at });

let hub: { todos: unknown[]; sessions: unknown[]; interactions: unknown[] };
let host: HTMLDivElement;
let root: Root;
let bump: () => void = () => {};

/** The chat: re-renders on bump() and hands the strips the same conversation each time. */
function Chat({ conversation }: { conversation: string }) {
  const [n, setN] = useState(0);
  bump = () => setN((x) => x + 1);
  return (
    <div data-n={n}>
      <TaskQuestions conversation={conversation} />
      <TaskDock conversation={conversation} />
      <RouteChip conversation={conversation} />
    </div>
  );
}

beforeEach(async () => {
  hub = { todos: [todo("t1", "First task", "s1")], sessions: [session("s1")], interactions: [question("ix1", "s1")] };
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      const u = String(url);
      const body = u.includes("/todos") ? { todos: hub.todos } : u.includes("/interactions") ? { interactions: hub.interactions } : u.includes("/sessions") ? { sessions: hub.sessions } : {};
      return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
    }),
  );
  await refresh();
  setRoute("c1", "t1");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Chat conversation="c1" />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setRoute("c1", null);
  reset();
  vi.unstubAllGlobals();
});

const dock = () => host.querySelector('[data-testid="task-dock"]')?.textContent ?? "";
const shown = () => host.querySelector('[data-testid="question"]')?.textContent;
const chip = () => host.firstElementChild?.lastElementChild?.textContent ?? "";

describe("the task strips above a Persona conversation's composer", () => {
  it("do not render again when the chat does with the same conversation", () => {
    // drawn once: the question, the dock's pill and the chip
    expect(renders).toEqual({ questions: 1, dock: 1, route: 1 });
    expect(shown()).toBe("ix1");
    expect(dock()).toContain("First task");
    expect(chip()).toContain("First task");
    renders.questions = renders.dock = renders.route = 0;
    for (let i = 0; i < 10; i++) act(() => bump());
    expect(host.firstElementChild?.getAttribute("data-n")).toBe("10");
    expect(renders).toEqual({ questions: 0, dock: 0, route: 0 });
  });

  it("still show a new task and its question on their own", async () => {
    for (let i = 0; i < 10; i++) act(() => bump());
    // its agent is pushed (the session list is read only once), then the todos and the cards are read again
    act(() => upsertSession(session("s2") as unknown as Session));
    hub = { ...hub, todos: [...hub.todos, todo("t2", "Second task", "s2", later)], interactions: [...hub.interactions, question("ix2", "s2", later)] };
    await act(async () => {
      await refresh();
    });
    expect(dock()).toContain("Second task");
    expect(shown()).toBe("ix1"); // the oldest question first, the new one counted after it
    act(() => endInteraction("ix1"));
    expect(shown()).toBe("ix2");
    act(() => setRoute("c1", "t2"));
    expect(chip()).toContain("Second task");
  });
});
