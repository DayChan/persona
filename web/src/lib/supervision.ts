import { t } from "../i18n";
import type { Todo } from "./todos";

// A task the main conversation supervises (#3124, docs/assistant/unified-entry.md "Supervision"): what done means, where its review
// stands, and the reviews so far. Only such tasks carry `supervision`; an older hub sends none.

export type ReviewState = "" | "pending_review" | "rework" | "needs_user" | "accepted" | "cancelled";

export interface ReviewCheck {
  criterion: string;
  met: boolean;
  evidence?: string;
}

export interface TaskReview {
  at: string;
  verdict: "accepted" | "rework" | "needs_user" | "cancelled";
  /** "reviewer": a rework the independent reviewer's findings started (#2173); absent: the supervisor's. */
  by?: string;
  round?: number;
  checks?: ReviewCheck[];
  note?: string;
}

export interface Supervision {
  supervisor: string;
  done_when?: string[];
  expect_minutes?: number;
  state?: ReviewState;
  round?: number;
  reviews?: TaskReview[];
  updated_at?: string;
  /** What its agent last reported to Persona (report_to_supervisor); kept by the hub while it runs. */
  progress?: TaskProgress;
  /** The latest independent review of its change (#2173); absent when none ran (or on an older hub). */
  auto_review?: AutoReview;
}

/** An independent reviewer's run over a supervised task's change: its state, counts and report. */
export interface AutoReview {
  id: string;
  state: "running" | "clean" | "blocking" | "failed" | "cancelled";
  round?: number;
  count?: number;
  blocking?: number;
  findings?: string;
  started_at?: string;
  ended_at?: string;
}

/** The independent review in a line for the task's page ("Nothing blocking · 2 findings"); undefined when there is
 *  none to show (none ran, or it was cancelled because the work went on). */
export function autoReviewLine(sup: Supervision | undefined): string | undefined {
  const a = sup?.auto_review;
  if (!a) return undefined;
  const findings = a.count ? t("assistant-tasks.auto_review_findings", { count: a.count }) : "";
  switch (a.state) {
    case "running":
      return t("assistant-tasks.auto_review_running");
    case "clean":
      return [t("assistant-tasks.auto_review_clean"), findings].filter(Boolean).join(" · ");
    case "blocking":
      return [t("assistant-tasks.auto_review_blocking", { count: a.blocking ?? 1 }), findings].filter(Boolean).join(" · ");
    case "failed":
      return t("assistant-tasks.auto_review_failed");
  }
  return undefined;
}

export type ReportKind = "progress" | "question" | "blocked" | "done";

/** A supervised task's agent's latest report: the step of how many (0 or absent: not said), what it said in a line. */
export interface TaskProgress {
  kind: ReportKind;
  step?: number;
  steps?: number;
  text: string;
  at?: string;
}

/** The review state that holds now: an accepted or cancelled task the user reopened goes on again. */
export function reviewState(todo: Pick<Todo, "done_at"> & { supervision?: Supervision }): ReviewState {
  const st = todo.supervision?.state ?? "";
  if (!todo.done_at && (st === "accepted" || st === "cancelled")) return "";
  return st;
}

export type ReviewTone = "accent" | "red" | "orange" | "green" | "muted";

/** What the state is called and how it is coloured; undefined for a task that is not supervised. */
export function reviewBadge(todo: Pick<Todo, "done_at"> & { supervision?: Supervision }): { label: string; tone: ReviewTone } | undefined {
  const sup = todo.supervision;
  if (!sup) return undefined;
  switch (reviewState(todo)) {
    case "pending_review":
      return { label: t("assistant-tasks.review_pending"), tone: "accent" };
    case "rework":
      return { label: t("assistant-tasks.review_rework", { round: sup.round ?? 1 }), tone: "orange" };
    case "needs_user":
      return { label: t("assistant-tasks.review_needs_user"), tone: "orange" };
    case "accepted":
      return { label: t("assistant-tasks.review_accepted"), tone: "green" };
    case "cancelled":
      return { label: t("assistant-tasks.review_cancelled"), tone: "muted" };
  }
  return { label: t("assistant-tasks.review_ongoing"), tone: "muted" };
}

export type CheckMark = "met" | "missing" | "open";

/**
 * Each criterion with how the latest review that judged it found it: met, missing (with its evidence or gap), or
 * open (not reviewed yet). Criteria a review judged that are not in done_when (a task without any) follow.
 */
export function checklist(sup: Supervision | undefined): { criterion: string; mark: CheckMark; evidence?: string }[] {
  if (!sup) return [];
  const latest = new Map<string, ReviewCheck>();
  for (const r of sup.reviews ?? []) for (const c of r.checks ?? []) latest.set(c.criterion, c);
  const out: { criterion: string; mark: CheckMark; evidence?: string }[] = [];
  const seen = new Set<string>();
  for (const criterion of sup.done_when ?? []) {
    seen.add(criterion);
    const c = latest.get(criterion);
    out.push({ criterion, mark: c ? (c.met ? "met" : "missing") : "open", evidence: c?.evidence || undefined });
  }
  for (const [criterion, c] of latest) {
    if (!seen.has(criterion)) out.push({ criterion, mark: c.met ? "met" : "missing", evidence: c.evidence || undefined });
  }
  return out;
}

/** The latest review, if any. */
export function latestReview(sup: Supervision | undefined): TaskReview | undefined {
  const list = sup?.reviews ?? [];
  return list[list.length - 1];
}

/** "2/3": criteria met of all, for a card; "" without criteria. */
export function checkCount(sup: Supervision | undefined): string {
  const list = checklist(sup);
  if (list.length === 0) return "";
  return `${list.filter((c) => c.mark === "met").length}/${list.length}`;
}

/** "Step 3/5 · …", "Asked Persona: …": the agent's latest report in a line for a card; undefined when it has none. */
export function progressLine(sup: Supervision | undefined): string | undefined {
  const p = sup?.progress;
  if (!p || !p.text) return undefined;
  return reportLine(p.kind, p.text, p.step, p.steps);
}

/** A report (on a card, or a feed item of kind "report") in a line. */
export function reportLine(kind: string, text: string, step?: number, steps?: number): string {
  switch (kind) {
    case "question":
      return t("assistant-tasks.report_question", { text });
    case "blocked":
      return t("assistant-tasks.report_blocked", { text });
    case "done":
      return t("assistant-tasks.report_done", { text });
  }
  if (step && steps) return t("assistant-tasks.report_step_of", { step, steps, text });
  if (step) return t("assistant-tasks.report_step", { step, text });
  return text;
}

/** A review in the conversation's feed (kind "review"), in a line. */
export function reviewVerdictLabel(verdict: string, round?: number): string {
  switch (verdict) {
    case "accepted":
      return t("assistant-tasks.review_accepted");
    case "rework":
      return t("assistant-tasks.review_rework", { round: round ?? 1 });
    case "needs_user":
      return t("assistant-tasks.review_needs_user");
    case "cancelled":
      return t("assistant-tasks.review_cancelled");
  }
  return t("assistant-tasks.review_line");
}
