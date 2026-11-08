// Answering a card (POST /interactions/{id}/resolve): the card's own buttons (views/chat/Interactions.tsx) and a system
// notification's Allow / Deny (lib/notification-answer.ts) go through here, so both check the same and send the same.
import { t } from "../i18n";
import { ApiError, enc, errorText } from "./api";
import { endedText, type Resolution, type ResolveResult } from "./answered";
import { sessionCan } from "./format";
import { apiOp } from "./ops";
import { endInteraction, getSession, keepDelegated } from "./store";
import { isSecretRun } from "./tool-detail";
import type { Interaction } from "./types";

/** Sent (`already`: it had been answered, here or elsewhere), or why not, as a sentence to show. */
export type CardAnswer = { ok: true; already: boolean } | { ok: false; error: string };

export async function answerCard(ix: Interaction, body: Resolution): Promise<CardAnswer> {
  if (!sessionCan(getSession(ix.machine_id, ix.session_id), "approve")) return { ok: false, error: t("chat-interactions.approve_not_allowed") };
  // A password card (#1137) is filled in only on the iPhone: from here, only a bare decline, nothing typed.
  if (isSecretRun(ix) && !(body.allow === false && Object.keys(body).length === 1)) return { ok: false, error: t("chat-interactions.secret_run_on_iphone") };
  try {
    // one op id for this answer: sent again through a gap (a hub restart), it is taken once
    const res = await apiOp<ResolveResult>(`/interactions/${enc(ix.id)}/resolve`, { method: "POST", json: body });
    // Answered already (here, its event missed; or elsewhere): the hub says with what, and the card shows that.
    const already = res?.already === true;
    const r: Resolution = already ? (res.resolution ?? {}) : body;
    // The assistant answered this question first (#544): it shows as its answer, which the user may change.
    if (already && r.answered_by === "assistant" && ix.kind === "question") {
      keepDelegated(ix, r);
      return { ok: true, already };
    }
    // Handed back now, or the hub says it was handed back already: the agent waits on the computer (#4877).
    const handedBack = already ? res.status === "dismissed" : r.dismiss === true;
    endInteraction(ix.id, r.dismiss === true && !already ? undefined : { ix, resolution: r, already, handedBack });
    return { ok: true, already };
  } catch (e) {
    if (e instanceof ApiError && e.status === 410) {
      endInteraction(ix.id); // over (expired, withdrawn, handed back): nothing left to answer
      return { ok: false, error: endedText(e.body.status) };
    }
    if (e instanceof ApiError && e.code === "prompt_changed") return { ok: false, error: t("chat-interactions.prompt_changed") };
    return { ok: false, error: t("chat-interactions.send_failed", { error: errorText(e) }) };
  }
}
