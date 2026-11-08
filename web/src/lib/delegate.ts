// The assistant answering agents' questions first (docs/assistant/assistant.md "Assistant answers questions first";
// docs/protocol/protocol.md `interaction`, `interaction_resolved`, `interaction_reanswered`, POST …/reanswer): a question
// card it is still looking at (`delegating`), the context it filled in when it left a question to the user
// (`delegate`), and the questions it answered, which the user may answer again. The pure parts and the two requests;
// the views are views/chat/Delegated.tsx.
import { formatDate, t } from "../i18n";
import { api, ApiError, enc, errorText } from "./api";
import type { Resolution } from "./answered";
import { itemHeaders } from "./manager-choice";
import type { Delegate, Interaction, Question } from "./types";

/** The assistant is still deciding whether it may answer this question for the user: nothing was pushed yet, and an
 *  answer given here wins. Only questions are ever delegated. */
export function isDelegating(ix: Interaction): boolean {
  return ix.kind === "question" && ix.delegating === true;
}

export interface DelegateLine {
  key: keyof Delegate;
  label: string;
  text: string;
}

const FIELDS = ["what", "why_now", "recommend", "cost", "reason", "deadline"] as const;

function fieldLabel(key: keyof Delegate): string {
  switch (key) {
    case "what":
      return t("chat-delegate.field_what");
    case "why_now":
      return t("chat-delegate.field_why_now");
    case "recommend":
      return t("chat-delegate.field_recommend");
    case "cost":
      return t("chat-delegate.field_cost");
    case "reason":
      return t("chat-delegate.field_reason");
    case "deadline":
      return t("chat-delegate.field_deadline");
  }
}

/** The context the assistant filled in, one line per field it gave, always in the same order; the deadline as a date. */
export function delegateLines(d: Delegate | undefined): DelegateLine[] {
  if (!d) return [];
  return FIELDS.map((key) => {
    const text = key === "deadline" ? (d.deadline ? formatDate(d.deadline, { dateStyle: "medium", timeStyle: "short" }) : "") : (d[key] ?? "").trim();
    return { key, label: fieldLabel(key), text };
  }).filter((l) => l.text);
}

// The hub writes a recommendation for several questions as their answers joined by a full-width semicolon
// (assistant/delegate.go answerLine), and a multi-select answer as its labels joined by ", ", as the apps send it.
const QUESTIONS_SEP = "；"; // i18n-ignore: the hub's wire format
const PICKS_SEP = ", ";

/** The option labels `answer` picks on `q`: the label itself, or for a multi-select every one of its ", "-joined labels;
 *  [] for no answer, null when it is words of its own (or names something that is no option). */
function labelsOf(q: Question, answer: string): string[] | null {
  const a = answer.trim();
  if (!a) return [];
  const labels = (q.options ?? []).map((o) => o.label);
  if (labels.includes(a)) return [a];
  if (!q.multi_select) return null;
  const parts = a.split(PICKS_SEP).map((p) => p.trim()).filter(Boolean);
  return parts.length && parts.every((p) => labels.includes(p)) ? parts : null;
}

/** For each question, the options the assistant recommends, when its recommendation is the options' own words; none
 *  when it said something else (it may recommend not answering at all). */
export function recommendedLabels(questions: Question[], recommend: string | undefined): string[][] {
  const r = (recommend ?? "").trim();
  const parts = questions.length > 1 ? r.split(QUESTIONS_SEP) : [r];
  const fits = parts.length === questions.length;
  return questions.map((q, i) => (fits ? (labelsOf(q, parts[i]) ?? []) : []));
}

/** The "Change answer" form's starting point: the answers given so far picked where they are options, typed out where not. */
export function initialPicks(questions: Question[], answers: Record<string, string>): { picked: Record<number, string[]>; custom: Record<number, string> } {
  const picked: Record<number, string[]> = {};
  const custom: Record<number, string> = {};
  questions.forEach((q, i) => {
    const a = (answers[q.question] ?? "").trim();
    const labels = labelsOf(q, a);
    picked[i] = labels ?? [];
    if (!labels && a) custom[i] = a;
  });
  return { picked, custom };
}

/** The form's answers, by question text, as a card sends them: the typed answer, else the picks joined by ", ". */
export function formAnswers(questions: Question[], picked: Record<number, string[]>, custom: Record<number, string>): Record<string, string> {
  const out: Record<string, string> = {};
  questions.forEach((q, i) => {
    out[q.question] = custom[i]?.trim() || (picked[i] ?? []).join(PICKS_SEP);
  });
  return out;
}

/** Whether two answers to `q` say the same: the same words, or a multi-select's same options picked in any order (an
 *  answer in words of its own is compared as written). */
export function sameAnswer(q: Question | undefined, a: string, b: string): boolean {
  if (a.trim() === b.trim()) return true;
  if (!q?.multi_select) return false;
  const x = labelsOf(q, a), y = labelsOf(q, b);
  return Boolean(x?.length && y?.length) && [...x!].sort().join("\n") === [...y!].sort().join("\n");
}

/** The answers in `after` that say something other than `before`: only those go to the agent (an unchanged one would
 *  only repeat what it already went on with). */
export function changedAnswers(before: Record<string, string>, after: Record<string, string>, questions: Question[] = []): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [q, a] of Object.entries(after)) {
    const now = a.trim();
    if (now && !sameAnswer(questions.find((x) => x.question === q), now, (before[q] ?? "").trim())) out[q] = now;
  }
  return out;
}

export function reanswerChanged(before: Record<string, string>, after: Record<string, string>, questions: Question[] = []): boolean {
  return Object.keys(changedAnswers(before, after, questions)).length > 0;
}

/** A question the assistant answered for the user: what it answered and why, and when. */
export interface DelegatedAnswer {
  ix: Interaction;
  answers: Record<string, string>;
  reason: string;
  /** When it was answered (ms). */
  at: number;
}

/** What the user changed a delegated answer to (interaction_reanswered, or this page's own POST …/reanswer). */
export interface Reanswer {
  answers: Record<string, string>;
  at: number;
}

/** A question's answers as sent on the wire: only the text ones. */
export function answersIn(v: unknown): Record<string, string> {
  if (!v || typeof v !== "object" || Array.isArray(v)) return {};
  return Object.fromEntries(Object.entries(v as Record<string, unknown>).filter((e): e is [string, string] => typeof e[1] === "string"));
}

/** What the answers are now: the assistant's, with what the user changed since. */
export function latestAnswers(d: Pick<DelegatedAnswer, "answers">, re?: Reanswer): Record<string, string> {
  return { ...d.answers, ...re?.answers };
}

/** A row of GET /interactions/history. */
export interface InteractionRecord {
  interaction: Interaction;
  status: string;
  resolution?: Resolution | null;
  resolved_at?: string;
}

/** The questions the history says the assistant answered, newest first. */
export function delegatedHistory(records: InteractionRecord[]): DelegatedAnswer[] {
  return records
    .filter((r) => r.status === "resolved" && r.interaction?.kind === "question" && r.resolution?.answered_by === "assistant")
    .map((r) => ({
      ix: r.interaction,
      answers: answersIn(r.resolution?.answers),
      reason: typeof r.resolution?.reason === "string" ? r.resolution.reason : "",
      at: (r.resolved_at && Date.parse(r.resolved_at)) || 0,
    }))
    .sort((a, b) => b.at - a.at);
}

// The hub's most: its limit counts every card of the session, permissions too, before only the assistant's answers are kept.
const HISTORY_LIMIT = 500;

/** The questions the assistant answered in one agent's session (`manager`: the session's, when not the default). */
export async function loadDelegated(mid: string, sid: string, manager?: string): Promise<DelegatedAnswer[]> {
  const headers = itemHeaders(manager);
  const r = await api<{ interactions?: InteractionRecord[] }>(`/interactions/history?machine_id=${enc(mid)}&session_id=${enc(sid)}&limit=${HISTORY_LIMIT}`, headers ? { headers } : {});
  return delegatedHistory(r.interactions ?? []);
}

export type ReanswerResult = { ok: true } | { ok: false; error: string };

/** "Change answer": the user's answers go to the agent as a message (POST …/reanswer); why not, as a sentence. */
export async function reanswer(id: string, answers: Record<string, string>, manager?: string): Promise<ReanswerResult> {
  const headers = itemHeaders(manager);
  try {
    await api(`/interactions/${enc(id)}/reanswer`, { method: "POST", json: { answers }, ...(headers ? { headers } : {}) });
    return { ok: true };
  } catch (e) {
    return { ok: false, error: reanswerError(e) };
  }
}

/** Why a change of answer did not go, by the hub's status (docs/protocol/protocol.md POST …/reanswer); a hub without the
 *  route answers a bare 404. */
export function reanswerError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) return t("chat-delegate.error_answer_only");
    if (e.status === 409) return t("chat-delegate.error_not_delegated");
    if (e.status === 400) return t("chat-delegate.error_no_answer");
    if (e.status === 502) return t("chat-delegate.error_not_sent", { error: e.message });
    if (e.status === 503 && e.code === "assistant_offline") return t("chat-delegate.error_assistant_offline");
    if (e.status === 404 && !e.code) return t("chat-delegate.error_unsupported");
  }
  return t("chat-delegate.error_failed", { error: errorText(e) });
}
