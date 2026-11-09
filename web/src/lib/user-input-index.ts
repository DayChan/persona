import { lineItemIds } from "./chat-search";
import { navRailExcerpt, navRailPreview, navRailTicks, type NavRailTick } from "./nav-rail";
import { parseUserText, type ChatLine } from "./turns";
import type { Item } from "./types";

export interface UserInputPosition { id: string; seq: number }
export interface UserInputIndex { total: number; inputs: UserInputPosition[]; history: string }

/** This temporary count/position contract is deliberately smaller than #2071; migrate to that API when it ships. */
export function validUserInputIndex(value: unknown): value is UserInputIndex {
  if (!value || typeof value !== "object") return false;
  const index = value as UserInputIndex;
  if (!Number.isSafeInteger(index.total) || index.total < 0 || !Array.isArray(index.inputs) || index.total !== index.inputs.length || typeof index.history !== "string") return false;
  const ids = new Set<string>();
  let last = 0;
  return index.inputs.every((input) => {
    if (!input || typeof input !== "object" || typeof input.id !== "string" || !input.id || ids.has(input.id) || !Number.isSafeInteger(input.seq) || input.seq < last || input.seq <= 0) return false;
    ids.add(input.id); last = input.seq;
    return true;
  });
}

const entry = (id: string) => id.split("#")[0];

/** Refresh on raw user-entry arrivals, including queued inputs merged into an existing bubble.
 * The server owns provenance filtering; tool-result frames never trigger a full-history scan. */
export function userInputRevision(items: Item[]): string | undefined {
  return items.findLast((item) => item.role === "user" && (item.kind === "text" || item.kind === "image") && !item.tool_use_id)?.id;
}

/** Exactly one tick per server position, even when the transcript has loaded only its newest two inputs.
 * Unloaded inputs use an ordinal label; previewing never downloads transcript bodies. */
export function indexedNavTicks(index: UserInputIndex, lines: ChatLine[], items: Item[]): NavRailTick[] {
  const owner = new Map<string, string>();
  for (const line of lines) for (const id of lineItemIds(line)) owner.set(entry(id), line.id);
  const loaded = new Map(navRailTicks(lines).map((tick) => [tick.id, tick]));
  const words = new Map<string, string>();
  for (const item of items) {
    if (item.role === "user" && item.kind === "text") {
      const key = entry(item.id);
      words.set(key, [words.get(key), parseUserText(item.text ?? "").text].filter(Boolean).join("\n"));
    }
  }
  return index.inputs.map((input, i) => {
    const lineId = owner.get(entry(input.id));
    const tick = lineId ? loaded.get(lineId) : undefined;
    return {
      id: `seg:${input.id}`, seq: input.seq, lineId, ordinal: i + 1,
      preview: navRailPreview(words.get(entry(input.id)) ?? "") || tick?.preview || "",
      ...(tick?.attachmentsOnly ? { attachmentsOnly: tick.attachmentsOnly } : {}),
      ...(tick?.answerExcerpt ? { answerExcerpt: navRailExcerpt(tick.answerExcerpt) } : {}),
    };
  });
}

export function containsInput(items: Item[], input: UserInputPosition): boolean {
  return items.some((item) => item.seq === input.seq && entry(item.id) === entry(input.id));
}
