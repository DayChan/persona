// @vitest-environment jsdom
// The personal agent list (views/agents/AgentsView.tsx) with one source failing while another lists agents: the failing
// source's hint shows as a notice above the agents, in the paged and the legacy list (#3098).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentSession } from "../../lib/agents";

const state = vi.hoisted(() => ({
  features: [] as string[],
  list: { sessions: [] as unknown[], loaded: true, error: "", hints: [] as string[], sources: null },
}));
vi.mock("../../lib/agents", async (original) => ({
  ...(await original<typeof import("../../lib/agents")>()),
  useAgentList: () => state.list,
  fetchHiddenSessions: async () => [],
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  useStore: <T,>(select: (s: { features: string[] }) => T): T => select({ features: state.features }),
}));
// the paged list fetches its own rows; the notice is what is under test
vi.mock("./PagedSessionList", () => ({ PagedSessionList: () => null }));
import { AgentsView } from "./AgentsView";
import { t } from "../../i18n";

vi.hoisted(() => {
  (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
});

const agent = { id: "lark:a", title: "Cue", unreadCount: 0, rankTimeMs: 1 } as AgentSession;
const notice = () => document.querySelector("[role=status]")?.textContent ?? "";

let root: Root;
let el: HTMLDivElement;
beforeEach(() => {
  el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
});
afterEach(() => {
  act(() => root.unmount());
  el.remove();
});

describe.each([["paged", ["sessions_list"]], ["legacy", []]])("personal agents, %s list", (_, features) => {
  it("names a failing source above the agents the others list", async () => {
    state.features = features;
    state.list = { ...state.list, sessions: [agent], hints: ["plugin not running"] };
    await act(async () => root.render(<AgentsView onToggleSidebar={() => {}} />));
    expect(notice()).toContain(t("agents-agentsview.sources_unavailable"));
    expect(notice()).toContain("plugin not running");
  });

  it("shows no notice when every source lists", async () => {
    state.features = features;
    state.list = { ...state.list, sessions: [agent], hints: [] };
    await act(async () => root.render(<AgentsView onToggleSidebar={() => {}} />));
    expect(notice()).toBe("");
  });
});
