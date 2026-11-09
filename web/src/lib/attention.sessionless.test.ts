import { expect, it } from "vitest";
import { answeredTags, detectNewCards } from "./attention";
import type { Interaction } from "./types";

const card = (id: string, session_id = ""): Interaction => ({ id, machine_id: "example-computer", session_id, kind: "permission", tool_name: "personad_ask", created_at: "2026-01-01T10:00:00Z" });
const now = Date.parse("2026-01-01T10:01:00Z");
const detect = (known: Map<string, number> | undefined, cards: Interaction[]) => detectNewCards(known, cards, new Map(), new Map(), [], new Set(), now);

it("announces each new sessionless request once and settles its own notification", () => {
  const cards = [card("request-one"), card("request-two")];
  const fresh = detect(new Map(), cards);
  expect(fresh.events.map((e) => e.key)).toEqual(["card:request-one", "card:request-two"]);
  expect(detect(fresh.ids, cards).events).toEqual([]);
  expect(answeredTags(new Map([["card:request-one", "request-one"], ["card:request-two", "request-two"]]), [cards[1]])).toEqual(["card:request-one#needs"]);
});
it("does not announce initial snapshots or cards with missing nonempty sessions", () => {
  expect(detect(undefined, [card("request-one")]).events).toEqual([]);
  expect(detect(new Map(), [card("request-two", "unknown-session")]).events).toEqual([]);
});
