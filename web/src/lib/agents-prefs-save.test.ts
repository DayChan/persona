// A Personal agents list prefs change the hub did not store is put back and said, instead of looking saved until the
// next read quietly undoes it (#1880).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../ui/atoms", () => ({ toast: vi.fn() }));
vi.mock("react", async (orig) => ({
  ...(await orig<typeof import("react")>()),
  useSyncExternalStore: (_sub: unknown, get: () => unknown) => get(),
}));

import { toast } from "../ui/atoms";
import { togglePin, updateAgentPrefs, useAgentPrefs, type AgentPrefs } from "./agents";

type Put = { pins: string[]; answer: (status: number) => void };
let puts: Put[] = [];
let gets: Array<(pins: string[]) => void> = [];
let hubPins: string[] = [];

const prefsOf = (pins: string[]): AgentPrefs => ({ pins, view: {}, sections: [], assign: {} });

beforeEach(() => {
  puts = [];
  gets = [];
  hubPins = [];
  vi.mocked(toast).mockClear();
  vi.stubGlobal("localStorage", { getItem: () => "tok", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init?: RequestInit) => {
      if (!String(url).endsWith("/agents/prefs")) return Promise.resolve(new Response("{}"));
      if ((init?.method ?? "GET").toUpperCase() === "PUT") {
        const pins = (JSON.parse(String(init?.body)) as AgentPrefs).pins;
        return new Promise<Response>((done) =>
          puts.push({
            pins,
            answer: (status) => {
              if (status === 200) hubPins = pins;
              done(new Response(status === 200 ? JSON.stringify(prefsOf(pins)) : JSON.stringify({ error: "database is down" }), { status }));
            },
          }),
        );
      }
      return new Promise<Response>((done) => gets.push((pins) => done(new Response(JSON.stringify(prefsOf(pins))))));
    }),
  );
});

afterEach(async () => {
  // settle anything still out and start the next test from no pins
  while (puts.length || gets.length) {
    puts.splice(0).forEach((p) => p.answer(200));
    gets.splice(0).forEach((g) => g([]));
    await flush();
  }
  updateAgentPrefs(() => prefsOf([]));
  puts.splice(0).forEach((p) => p.answer(200));
  await flush();
  gets.splice(0).forEach((g) => g([]));
  await flush();
  vi.unstubAllGlobals();
});

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("updateAgentPrefs when the hub does not store it", () => {
  it("puts the change back and says it was not saved", async () => {
    updateAgentPrefs((p) => togglePin(p, "s1"));
    expect(useAgentPrefs().pins).toEqual(["s1"]); // shows at once
    puts[0].answer(500);
    await flush();
    await flush();
    expect(useAgentPrefs().pins).toEqual([]);
    expect(toast).toHaveBeenCalledTimes(1);
    expect(vi.mocked(toast).mock.calls[0][1]).toBe("error");
    // and reads what the hub has
    expect(gets.length).toBe(1);
    gets[0](hubPins);
    await flush();
    expect(useAgentPrefs().pins).toEqual([]);
  });

  it("does not undo a later change that was saved", async () => {
    updateAgentPrefs((p) => togglePin(p, "s1"));
    updateAgentPrefs((p) => togglePin(p, "s2"));
    puts[0].answer(500);
    await flush();
    expect(useAgentPrefs().pins).toEqual(["s2", "s1"]); // the later change is still on its way
    puts[1].answer(200);
    await flush();
    await flush();
    gets[0]?.(hubPins);
    await flush();
    expect(useAgentPrefs().pins).toEqual(["s2", "s1"]);
  });

  it("shows the hub's value when two changes in a row both failed", async () => {
    updateAgentPrefs((p) => togglePin(p, "s1"));
    updateAgentPrefs((p) => togglePin(p, "s2"));
    puts[0].answer(500);
    puts[1].answer(500);
    await flush();
    await flush();
    expect(toast).toHaveBeenCalledTimes(2);
    gets[0](hubPins);
    await flush();
    expect(useAgentPrefs().pins).toEqual([]);
  });

  it("keeps the change here, without a message, on a hub that predates the endpoint", async () => {
    updateAgentPrefs((p) => togglePin(p, "s1"));
    puts[0].answer(404);
    await flush();
    await flush();
    expect(useAgentPrefs().pins).toEqual(["s1"]);
    expect(toast).not.toHaveBeenCalled();
  });
});
