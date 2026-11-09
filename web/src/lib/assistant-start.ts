// The first message of a new Persona conversation (POST /api/v1/assistant/conversations, docs/protocol/protocol.md
// "Operation receipts (client_ops)"). A hub that keeps a receipt for starts (assistant_start_ops, #1381) gets one op id
// for that message, and a try that died on the way goes again with that same id: a lost answer then opens the
// conversation that was started instead of starting a second one (#1382). An older hub gets neither, since a repeat
// there would start a second conversation.
import { api } from "./api";
import { apiOp } from "./ops";
import { getState } from "./store";
import type { Session } from "./types";

export const START_OPS_FEATURE = "assistant_start_ops";

/**
 * Starts a conversation with `json` (`prompt`, `main`, `launcher`…). `opId` is the first message's: the caller keeps
 * one for every try of the same message (the assistant restarting, retried without a word) and makes a new one when the
 * user sends it again after a refusal, whose receipt would answer the old id with that refusal.
 */
export function startConversation(json: unknown, headers?: HeadersInit, opId?: string): Promise<{ session: Session }> {
  const init = { method: "POST", json, headers };
  if (!getState().features.includes(START_OPS_FEATURE)) return api<{ session: Session }>("/assistant/conversations", init);
  return apiOp<{ session: Session }>("/assistant/conversations", { ...init, opId });
}
