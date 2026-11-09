// An independent review of a session's work (#1808): started from the chat's ⋯ menu, the hub runs a fresh read-only
// reviewer agent on the same machine and directory, and the findings come back as one pending interaction card
// (kind "review") on the source session's chat — the same machinery as the triage cards, so nothing here watches for
// them. Explicit only: nothing is ever reviewed on its own.
import { t } from "../i18n";
import { api, enc } from "./api";
import { stateOf } from "./format";
import type { Session } from "./types";

/** The hub feature that says this hub starts independent reviews (hello `features`). */
export const AGENT_REVIEW_FEATURE = "agent_review";

/** What POST …/review answers once the reviewer is under way. */
export interface AgentReviewStart {
  running: true;
  reviewer_session_id: string;
  started_at: string;
}

/** Starts a fresh read-only reviewer of the session's work, on its own machine and in its directory. */
export function startAgentReview(mid: string, sid: string): Promise<AgentReviewStart> {
  return api<AgentReviewStart>(`/machines/${enc(mid)}/sessions/${enc(sid)}/review`, { method: "POST" });
}

/** Why a review cannot start yet: the agent is at work, waits on an answer, or is still starting. The menu item stays,
 *  disabled, with this as its hint; anything else the hub refuses with its own words, which the menu shows as they come. */
export function busyReason(session: Session): string | undefined {
  const state = stateOf(session);
  return state === "working" || state === "needs_you" || session.starting ? t("chat-agent-review.busy") : undefined;
}
