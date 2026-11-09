import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/attention/cases.json";
import type { AgentMutes } from "./agent-groups";
import { lookingOnly } from "./attention";
import { agentKey } from "./pins";
import { routePath } from "./router";
import { isMuted } from "./session-actions";
import { stateCounts } from "./sessions";
import type { Interaction, Session } from "./types";

// The hub's "Needs you" summary for Persona Link (server/internal/assistant/fleet_attention.go) reads the same fixture:
// on the same sessions its count is the sidebar's badge and its items are the agents the badge counts; an item notifies
// when the web app's system notification would (not muted, not waiting only on the assistant's decision), but only
// once the agent has a card, as the phone is pushed (the web app rings a terminal dialog at once); its routes are the
// router's.
describe("Persona Link's Needs you summary (spec/fixtures/attention)", () => {
  for (const c of fixture.cases) {
    it(c.name, () => {
      const sessions = c.input.sessions as unknown as Session[];
      const interactions = c.input.interactions as unknown as Interaction[];
      const mutes: AgentMutes = c.input.mutes;
      const items: { machine_id: string; session_id: string; muted: boolean; notify: boolean; route: string }[] = c.expected.items;
      const listed = sessions.filter((s) => !s.assistant); // useAgentSessions: the sidebar's list
      expect(stateCounts(listed).badge).toBe(c.expected.needs_you);
      const counted = listed.filter((s) => stateCounts([s]).badge === 1).map(agentKey);
      expect(items.map(agentKey).sort()).toEqual(counted.sort());
      const looking = lookingOnly(interactions);
      const carded = new Set(interactions.map(agentKey));
      for (const item of items) {
        const s = sessions.find((x) => agentKey(x) === agentKey(item))!;
        expect(isMuted(s, mutes)).toBe(item.muted);
        expect(!isMuted(s, mutes) && !looking.has(agentKey(s)) && carded.has(agentKey(s))).toBe(item.notify);
        expect(routePath({ name: "session", mid: s.machine_id, sid: s.session_id })).toBe(item.route);
      }
    });
  }
});
