// Answering a card (POST /interactions/{id}/resolve): the card's own buttons (views/chat/Interactions.tsx) and a system
// notification's Allow / Deny (lib/notification-answer.ts) go through here, so both check the same and send the same.
import { t } from "../i18n";
import { ApiError, enc, errorText } from "./api";
import { endedText, type Resolution, type ResolveResult } from "./answered";
import { fields } from "./elicitation";
import { sessionCan } from "./format";
import { apiOp } from "./ops";
import { endInteraction, getSession, keepDelegated } from "./store";
import { isSecretRun } from "./tool-detail";
import type { Interaction } from "./types";

/** Sent (`already`: it had been answered, here or elsewhere), or why not, as a sentence to show. */
export type CardAnswer = { ok: true; already: boolean } | { ok: false; error: string };

/** What a password card (#1137) may send: its own form's answers, a bare decline or an allow with the typed password
 *  alone. Anything else (a quick Allow, a reason, Always allow, hand back, other values) is refused before a request. */
function isSecretRunAnswer(body: Resolution): boolean {
  const keys = Object.keys(body);
  if (body.allow === false) return keys.length === 1;
  const values = (body.values ?? {}) as Record<string, unknown>;
  return body.allow === true && keys.length === 2 && Object.keys(values).length === 1 && typeof values.password === "string" && values.password.trim() !== "";
}

/** A property name that holds a credential whatever the form says about it (the hub's secretName, answered.go). */
const secretName = /pass|secret|token|credential|api[_-]?key|private|otp|pin\b/i;

/** The answer as the answered receipt keeps it, as the hub's history keeps it (#1600): each value filled in only by its
 *  key, a secret card's typed text masked, and an MCP form's secret properties (by schema or by name) masked. */
function redacted(ix: Interaction, body: Resolution): Resolution {
  let r = body;
  if (ix.secret && r.text) r = { ...r, text: "<redacted>" };
  if (r.values && typeof r.values === "object") r = { ...r, values: Object.fromEntries(Object.keys(r.values).map((k) => [k, "<redacted>"])) };
  if (r.content && typeof r.content === "object") {
    const secret = new Set(fields(ix.elicitation?.requested_schema).filter((f) => f.secret).map((f) => f.key));
    r = { ...r, content: Object.fromEntries(Object.entries(r.content).map(([k, v]) => [k, secret.has(k) || secretName.test(k) ? "<redacted>" : v])) };
  }
  return r;
}

export async function answerCard(ix: Interaction, body: Resolution): Promise<CardAnswer> {
  if (!sessionCan(getSession(ix.machine_id, ix.session_id), "approve")) return { ok: false, error: t("chat-interactions.approve_not_allowed") };
  if (isSecretRun(ix) && !isSecretRunAnswer(body)) return { ok: false, error: t("chat-interactions.secret_run_card_only") };
  try {
    // one op id for this answer: sent again through a gap (a hub restart), it is taken once
    const res = await apiOp<ResolveResult>(`/interactions/${enc(ix.id)}/resolve`, { method: "POST", json: body });
    // Answered already (here, its event missed; or elsewhere): the hub says with what, and the card shows that.
    const already = res?.already === true;
    const r: Resolution = already ? (res.resolution ?? {}) : redacted(ix, body);
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
